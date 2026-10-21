package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

func usageOnlyFixture(t *testing.T) (*fixture, domain.Model) {
	t.Helper()
	f := integrationFixture(t)
	priced := f.model
	f.model.BillingMode = domain.BillingUsageOnly
	f.model.InputRate, f.model.OutputRate, f.model.CachedRate, f.model.CacheWriteRate = 0, 0, 0, 0
	f.model.CostInputRate, f.model.CostOutputRate, f.model.CostCachedRate, f.model.CostCacheWriteRate = 0, 0, 0, 0
	var err error
	f.model, err = f.s.SaveModel(f.ctx, f.model)
	if err != nil {
		t.Fatal(err)
	}
	f.cred.BudgetMicros = 0
	if _, err = f.s.SaveCredential(f.ctx, f.cred); err != nil {
		t.Fatal(err)
	}
	return f, priced
}
func assertUsageOnlyMoneyUnchanged(t *testing.T, f *fixture) {
	t.Helper()
	if w := f.wallet(t); w.BalanceMicros != 0 || w.HeldMicros != 0 {
		t.Fatalf("usage-only changed wallet: %+v", w)
	}
	var spent, held, ledger int64
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT spent_micros,held_micros FROM credentials WHERE id=?", f.cred.ID).Scan(&spent, &held); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM ledger_entries WHERE tenant_id=?", f.user.TenantID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if spent != 0 || held != 0 || ledger != 0 {
		t.Fatalf("usage-only fabricated money: spent=%d held=%d ledger=%d", spent, held, ledger)
	}
}
func requestFromSnapshot(t *testing.T, f *fixture, id string) domain.Request {
	t.Helper()
	snapshot, err := f.s.Snapshot(f.ctx, f.user)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snapshot.Requests {
		if r.ID == id {
			return r
		}
	}
	t.Fatal("request absent from snapshot")
	return domain.Request{}
}

func TestMySQLUsageOnlySnapshotControlsSettlementWithoutFundsOrLedger(t *testing.T) {
	f, priced := usageOnlyFixture(t)
	r := f.reservation(0, 0)
	if err := f.s.Reserve(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	var customerHeld, costHeld bool
	var phase string
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT customer_hold_active,cost_hold_active,phase FROM requests WHERE id=?", r.Request.ID).Scan(&customerHeld, &costHeld, &phase); err != nil {
		t.Fatal(err)
	}
	if customerHeld || costHeld || phase != usageOnlyPending {
		t.Fatal("usage-only created money holds or lost crash recovery state")
	}
	assertUsageOnlyMoneyUnchanged(t, f)
	pending := requestFromSnapshot(t, f, r.Request.ID)
	if pending.BillingMode != domain.BillingUsageOnly || pending.CostStatus != "untracked" || pending.Usage.Known {
		t.Fatalf("unknown cost shown as known/free: %+v", pending)
	}
	// Changing catalog configuration after admission never re-prices this request.
	if _, err := f.s.SaveModel(f.ctx, priced); err != nil {
		t.Fatal(err)
	}
	settlement := domain.Settlement{Status: "completed", Usage: domain.Usage{InputTokens: 12, OutputTokens: 8, CachedTokens: 3, CacheWriteTokens: 2, Known: true}, ChargeMicros: 999, CostMicros: 777, LatencyMS: 15}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.s.Settle(f.ctx, r.Request.ID, settlement); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := f.s.Settle(f.ctx, r.Request.ID, domain.Settlement{Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	got := requestFromSnapshot(t, f, r.Request.ID)
	if got.Status != "completed" || !got.Usage.Known || got.Usage.OutputTokens != 8 || got.ChargeMicros != 0 || got.CostMicros != 0 || got.BillingMode != domain.BillingUsageOnly || got.CostStatus != "untracked" {
		t.Fatalf("settlement changed immutable policy/evidence: %+v", got)
	}
	assertUsageOnlyMoneyUnchanged(t, f)
}

func TestMySQLMeteredSnapshotDoesNotBecomeUsageOnlyAfterModelEdit(t *testing.T) {
	f := integrationFixture(t)
	if err := f.s.Credit(f.ctx, f.user.TenantID, 100, "test:credit", "Test funds"); err != nil {
		t.Fatal(err)
	}
	r := f.reservation(60, 30)
	if err := f.s.Reserve(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	// The prior deployed version wrote no billing_mode into its snapshots.
	if _, err := f.s.DB.ExecContext(f.ctx, "UPDATE requests SET price_snapshot=JSON_REMOVE(price_snapshot,'$.billing_mode') WHERE id=?", r.Request.ID); err != nil {
		t.Fatal(err)
	}
	pending := requestFromSnapshot(t, f, r.Request.ID)
	if pending.BillingMode != domain.BillingMetered || pending.CostStatus != "pending" {
		t.Fatalf("legacy request mode changed: %+v", pending)
	}
	model := f.model
	model.BillingMode = domain.BillingUsageOnly
	model.InputRate, model.OutputRate, model.CachedRate, model.CacheWriteRate = 0, 0, 0, 0
	model.CostInputRate, model.CostOutputRate, model.CostCachedRate, model.CostCacheWriteRate = 0, 0, 0, 0
	if _, err := f.s.SaveModel(f.ctx, model); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Settle(f.ctx, r.Request.ID, domain.Settlement{Status: "completed", Usage: domain.Usage{InputTokens: 5, OutputTokens: 2, Known: true}, ChargeMicros: 50, CostMicros: 20}); err != nil {
		t.Fatal(err)
	}
	got := requestFromSnapshot(t, f, r.Request.ID)
	if got.BillingMode != domain.BillingMetered || got.CostStatus != "recorded" || got.ChargeMicros != 50 || got.CostMicros != 20 {
		t.Fatalf("historical priced request lost accounting: %+v", got)
	}
	if w := f.wallet(t); w.BalanceMicros != 50 || w.HeldMicros != 0 {
		t.Fatalf("priced balance changed: %+v", w)
	}
}

func TestMySQLUsageOnlyUnknownUsageCrashRecoveryAndLateEvidence(t *testing.T) {
	f, priced := usageOnlyFixture(t)
	type item struct{ id, status, errorCode string }
	var requests []item
	for _, status := range []string{"completed", "failed", "canceled", "crash", "not_sent"} {
		r := f.reservation(0, 0)
		if err := f.s.Reserve(f.ctx, r); err != nil {
			t.Fatal(err)
		}
		errorCode := ""
		if status == "failed" {
			errorCode = "upstream_test"
		} else if status == "canceled" {
			errorCode = "client_disconnected"
		}
		if status != "crash" {
			if err := f.s.Settle(f.ctx, r.Request.ID, domain.Settlement{Status: status, LatencyMS: 23, ErrorCode: errorCode}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.s.DB.ExecContext(f.ctx, "UPDATE requests SET created_at=? WHERE id=?", time.Now().UTC().Add(-25*time.Hour), r.Request.ID); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, item{r.Request.ID, status, errorCode})
	}
	// Recovery, like settlement, must follow each original request's snapshot.
	if _, err := f.s.SaveModel(f.ctx, priced); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.s.Recover(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range requests {
		got := requestFromSnapshot(t, f, item.id)
		wantStatus := item.status
		if item.status == "crash" {
			wantStatus = "usage_unavailable"
		}
		if got.Status != wantStatus || got.Usage.Known != (item.status == "not_sent") || got.CostStatus != "untracked" {
			t.Fatalf("recovery invented usage or lost execution outcome: %+v", got)
		}
		var phase string
		if err := f.s.DB.QueryRowContext(f.ctx, "SELECT phase FROM requests WHERE id=?", item.id).Scan(&phase); err != nil {
			t.Fatal(err)
		}
		if item.status == "not_sent" {
			if phase != "settled" {
				t.Fatal("not-sent evidence was expired")
			}
			continue
		}
		if phase != usageOnlyExpired {
			t.Fatalf("crash/unknown request did not expire: %s", phase)
		}
		if err := f.s.Settle(f.ctx, item.id, domain.Settlement{Status: "unknown"}); err != nil {
			t.Fatal(err)
		}
		if err := f.s.Settle(f.ctx, item.id, domain.Settlement{Status: "completed", Usage: domain.Usage{InputTokens: 11, OutputTokens: 7, Known: true}, ChargeMicros: 88, CostMicros: 44}); err != nil {
			t.Fatal(err)
		}
		got = requestFromSnapshot(t, f, item.id)
		if item.status == "crash" {
			wantStatus = "completed"
		}
		if !got.Usage.Known || got.Usage.OutputTokens != 7 || got.Status != wantStatus || got.ChargeMicros != 0 || got.CostMicros != 0 {
			t.Fatalf("late evidence retroactively billed or rewrote outcome: %+v", got)
		}
		if got.ErrorCode == "reconciliation_expired" {
			t.Fatal("late verified usage retained its resolved expiration error")
		}
		if got.ErrorCode != item.errorCode || item.status != "crash" && got.LatencyMS != 23 {
			t.Fatalf("late verified usage erased execution evidence: %+v", got)
		}
	}
	assertUsageOnlyMoneyUnchanged(t, f)
}

func TestMySQLUsageOnlyBudgetRaceAndAccessChecksRemainAtomic(t *testing.T) {
	f, _ := usageOnlyFixture(t)
	r := f.reservation(0, 0)
	// This simulates an operator imposing a money cap after routing read the key.
	f.cred.BudgetMicros = 100
	if _, err := f.s.SaveCredential(f.ctx, f.cred); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Reserve(f.ctx, r); !errors.Is(err, ErrUntrackedCostBudget) {
		t.Fatalf("unknown cost admitted under local dollar cap: %v", err)
	}
	var count int
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM requests WHERE id=?", r.Request.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("budget rejection left request state")
	}
	f.cred.BudgetMicros = 0
	if _, err := f.s.SaveCredential(f.ctx, f.cred); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name, disable, restore string
		args                   []any
	}{
		{"key", "UPDATE access_keys SET enabled=FALSE WHERE id=?", "UPDATE access_keys SET enabled=TRUE WHERE id=?", []any{f.key.ID}},
		{"tenant", "UPDATE tenants SET status='suspended' WHERE id=?", "UPDATE tenants SET status='active' WHERE id=?", []any{f.user.TenantID}},
		{"credential", "UPDATE credentials SET config=JSON_SET(config,'$.enabled',FALSE) WHERE id=?", "UPDATE credentials SET config=JSON_SET(config,'$.enabled',TRUE) WHERE id=?", []any{f.cred.ID}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if _, err := f.s.DB.ExecContext(f.ctx, check.disable, check.args...); err != nil {
				t.Fatal(err)
			}
			defer f.s.DB.ExecContext(f.ctx, check.restore, check.args...)
			if err := f.s.Reserve(f.ctx, r); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("usage-only bypassed %s lifecycle: %v", check.name, err)
			}
		})
	}
	nonzero := r
	nonzero.Request.ReservedMicros = 1
	if err := f.s.Reserve(f.ctx, nonzero); err == nil {
		t.Fatal("usage-only accepted a money hold")
	}
	nonzero = r
	nonzero.Model.CostInputRate = 1
	if err := f.s.Reserve(f.ctx, nonzero); err == nil {
		t.Fatal("usage-only accepted a priced snapshot")
	}
	assertUsageOnlyMoneyUnchanged(t, f)
}

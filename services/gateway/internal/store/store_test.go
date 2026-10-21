package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

func TestBudgetLimitsDoNotOverflow(t *testing.T) {
	cases := []struct {
		budget, spent, held, reserve int64
		want                         bool
	}{{100, 20, 30, 50, true}, {100, 20, 30, 51, false}, {100, 101, 0, 0, false}, {0, 0, math.MaxInt64, 1, false}, {0, 0, 0, -1, false}, {0, 50, 30, 20, true}}
	for _, c := range cases {
		if got := budgetAllows(c.budget, c.spent, c.held, c.reserve); got != c.want {
			t.Errorf("budgetAllows(%+v)=%v", c, got)
		}
	}
}
func TestCustomerChargeCapAndFailurePolicy(t *testing.T) {
	for _, v := range []struct {
		reserve, charge int64
		status          string
		want            int64
	}{{100, 101, "completed", 100}, {100, 40, "completed", 40}, {100, 90, "error", 0}, {100, 90, "not_sent", 0}, {100, 30, "canceled", 30}} {
		if got := customerCharge(v.reserve, v.charge, v.status); got != v.want {
			t.Errorf("customerCharge(%+v)=%d", v, got)
		}
	}
}
func TestEncryptionBoundToConfigurationIdentity(t *testing.T) {
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	s := &Store{aead: aead}
	ciphertext, err := s.encrypt("secret-value", "credential:one")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "secret-value") {
		t.Fatal("plaintext persisted")
	}
	plain, err := s.decrypt(ciphertext, "credential:one")
	if err != nil || plain != "secret-value" {
		t.Fatalf("decrypt: %q %v", plain, err)
	}
	if _, err = s.decrypt(ciphertext, "credential:two"); err == nil {
		t.Fatal("ciphertext accepted under different identity")
	}
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err = s.decrypt(ciphertext, "credential:one"); err == nil {
		t.Fatal("modified ciphertext accepted")
	}
}

type fixture struct {
	s      *Store
	ctx    context.Context
	user   domain.User
	key    domain.AccessKey
	secret string
	cred   domain.Credential
	model  domain.Model
}

func integrationFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN not set; real MySQL integration test skipped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	master := os.Getenv("MASTER_KEY")
	if master == "" {
		master = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	s, err := Open(ctx, dsn, master)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	u, err := s.EnsureSuperAdmin(ctx, "Integration administrator")
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := s.CreateKey(ctx, u, "Integration key", []string{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := s.SaveCredential(ctx, domain.Credential{Name: "Integration credential", EndpointID: "openai-official", Secret: "integration-upstream-secret", Enabled: true, RPM: 100, TPM: 10000, MaxConcurrency: 10, BudgetMicros: 1000000})
	if err != nil {
		t.Fatal(err)
	}
	model, err := s.SaveModel(ctx, domain.Model{Name: "Integration model", Provider: "openai", UpstreamModel: "test-model", Enabled: true, InputRate: 1000000, OutputRate: 2000000, CostInputRate: 500000, CostOutputRate: 1000000, MaxOutputTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean := context.Background()
		for _, q := range []string{"DELETE a FROM attempts a JOIN requests r ON r.id=a.request_id WHERE r.tenant_id=?", "DELETE FROM requests WHERE tenant_id=?", "DELETE FROM resources WHERE tenant_id=?", "DELETE FROM ledger_entries WHERE tenant_id=?", "DELETE FROM access_keys WHERE tenant_id=?", "DELETE s FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.tenant_id=?", "DELETE FROM users WHERE tenant_id=?", "DELETE FROM projects WHERE tenant_id=?", "DELETE FROM wallets WHERE tenant_id=?", "DELETE FROM tenants WHERE id=?"} {
			if _, e := s.DB.ExecContext(clean, q, u.TenantID); e != nil {
				t.Logf("cleanup: %v", e)
			}
		}
		s.DB.ExecContext(clean, "DELETE FROM credentials WHERE id=?", cred.ID)
		s.DB.ExecContext(clean, "DELETE FROM models WHERE id=?", model.ID)
	})
	return &fixture{s: s, ctx: ctx, user: u, key: key, secret: secret, cred: cred, model: model}
}
func (f *fixture) reservation(amount, cost int64) domain.Reservation {
	return domain.Reservation{Request: domain.Request{ID: newID("req_"), TenantID: f.user.TenantID, ProjectID: f.key.ProjectID, AccessKeyID: f.key.ID, CredentialID: f.cred.ID, Provider: "openai", Model: f.model.ID, ReservedMicros: amount}, CredentialID: f.cred.ID, Model: f.model, CostReserve: cost}
}
func (f *fixture) wallet(t *testing.T) domain.Wallet {
	t.Helper()
	var w domain.Wallet
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT balance_micros,held_micros FROM wallets WHERE tenant_id=?", f.user.TenantID).Scan(&w.BalanceMicros, &w.HeldMicros); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestMySQLWalletConcurrentReserveAndIdempotency(t *testing.T) {
	f := integrationFixture(t)
	if err := f.s.Credit(f.ctx, f.user.TenantID, 100, "test:credit", "Test funds"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Credit(f.ctx, f.user.TenantID, 100, "test:credit", "Test funds"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Credit(f.ctx, f.user.TenantID, 200, "test:credit", "Test funds"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed duplicate credit: %v", err)
	}
	var count atomic.Int32
	var mu sync.Mutex
	var winning domain.Reservation
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.reservation(60, 30)
			err := f.s.Reserve(f.ctx, r)
			if err == nil {
				count.Add(1)
				mu.Lock()
				winning = r
				mu.Unlock()
			} else if !errors.Is(err, ErrInsufficientFunds) {
				t.Errorf("Reserve: %v", err)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("concurrent reservations admitted %d; want1", count.Load())
	}
	if w := f.wallet(t); w.BalanceMicros != 100 || w.HeldMicros != 60 {
		t.Fatalf("wallet after reserve: %+v", w)
	}
	v := domain.Settlement{Status: "completed", Usage: domain.Usage{Known: true, InputTokens: 20}, ChargeMicros: 90, CostMicros: 20}
	for i := 0; i < 2; i++ {
		if err := f.s.Settle(f.ctx, winning.Request.ID, v); err != nil {
			t.Fatal(err)
		}
	}
	if w := f.wallet(t); w.BalanceMicros != 40 || w.HeldMicros != 0 {
		t.Fatalf("settlement cap/idempotency failed: %+v", w)
	}
	var spent, held int64
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT spent_micros,held_micros FROM credentials WHERE id=?", f.cred.ID).Scan(&spent, &held); err != nil {
		t.Fatal(err)
	}
	if spent != 20 || held != 0 {
		t.Fatalf("upstream accounting %d,%d", spent, held)
	}
	var snapshot string
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT price_snapshot FROM requests WHERE id=?", winning.Request.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	f.model.InputRate = 999
	if _, err := f.s.SaveModel(f.ctx, f.model); err != nil {
		t.Fatal(err)
	}
	var unchanged string
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT price_snapshot FROM requests WHERE id=?", winning.Request.ID).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged != snapshot {
		t.Fatal("historical price snapshot changed")
	}
}
func TestMySQLUnknownUsageRecoveryDoesNotChargeLate(t *testing.T) {
	f := integrationFixture(t)
	if err := f.s.Credit(f.ctx, f.user.TenantID, 100, "test:credit", "Test funds"); err != nil {
		t.Fatal(err)
	}
	r := f.reservation(60, 30)
	if err := f.s.Reserve(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Settle(f.ctx, r.Request.ID, domain.Settlement{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Recover(f.ctx); err != nil {
		t.Fatal(err)
	}
	if w := f.wallet(t); w.HeldMicros != 60 {
		t.Fatal("fresh uncertain request was refunded")
	}
	if _, err := f.s.DB.ExecContext(f.ctx, "UPDATE requests SET created_at=? WHERE id=?", time.Now().UTC().Add(-25*time.Hour), r.Request.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Recover(f.ctx); err != nil {
		t.Fatal(err)
	}
	if w := f.wallet(t); w.BalanceMicros != 100 || w.HeldMicros != 0 {
		t.Fatalf("expired hold: %+v", w)
	}
	var held int64
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT held_micros FROM credentials WHERE id=?", f.cred.ID).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 30 {
		t.Fatal("unknown upstream cost wrongly released")
	}
	if err := f.s.Settle(f.ctx, r.Request.ID, domain.Settlement{Status: "completed", Usage: domain.Usage{Known: true}, ChargeMicros: 50, CostMicros: 20}); err != nil {
		t.Fatal(err)
	}
	if w := f.wallet(t); w.BalanceMicros != 100 {
		t.Fatalf("late usage charged customer: %+v", w)
	}
}
func TestMySQLTenantIsolationAndSecrets(t *testing.T) {
	f := integrationFixture(t)
	got, err := f.s.AuthenticateKey(f.ctx, f.secret)
	if err != nil || got.TenantID != f.user.TenantID {
		t.Fatalf("authenticate key: %+v %v", got, err)
	}
	var stored string
	if err = f.s.DB.QueryRowContext(f.ctx, "SELECT token_hash FROM access_keys WHERE id=?", f.key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == f.secret || len(stored) != 64 {
		t.Fatal("access token not hashed")
	}
	var encrypted []byte
	if err = f.s.DB.QueryRowContext(f.ctx, "SELECT encrypted_secret FROM credentials WHERE id=?", f.cred.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encrypted), f.cred.Secret) {
		t.Fatal("upstream key not encrypted")
	}
	if err = f.s.RevokeKey(f.ctx, "different-tenant", f.key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross tenant revoke: %v", err)
	}
	r := domain.Resource{ID: "resp_" + newID(""), TenantID: f.user.TenantID, ProjectID: f.key.ProjectID, CredentialID: f.cred.ID, EndpointID: f.cred.EndpointID, Model: f.model.ID, InputTokens: 25}
	if err = f.s.SaveResource(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Resource(f.ctx, "different-tenant", r.ProjectID, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross tenant resource: %v", err)
	}
	if err = f.s.SaveResource(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	r.InputTokens++
	if err = f.s.SaveResource(f.ctx, r); !errors.Is(err, ErrConflict) {
		t.Fatalf("resource overwritten: %v", err)
	}
	snap, err := f.s.Snapshot(f.ctx, f.user)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Wallet.BalanceMicros != 0 {
		t.Fatal("nonzero initial funds")
	}
	for _, credential := range snap.Credentials {
		if credential.Secret != "" {
			t.Fatal("snapshot leaked upstream credential")
		}
	}

}

func TestMySQLUpstreamBudgetRollbackAndFailedCost(t *testing.T) {
	f := integrationFixture(t)
	if err := f.s.Credit(f.ctx, f.user.TenantID, 100, "test:credit", "Test funds"); err != nil {
		t.Fatal(err)
	}
	f.cred.BudgetMicros = 10
	if _, err := f.s.SaveCredential(f.ctx, f.cred); err != nil {
		t.Fatal(err)
	}
	tooLarge := f.reservation(50, 11)
	if err := f.s.Reserve(f.ctx, tooLarge); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("budget admission: %v", err)
	}
	if w := f.wallet(t); w.HeldMicros != 0 || w.BalanceMicros != 100 {
		t.Fatalf("budget rejection changed wallet: %+v", w)
	}
	r := f.reservation(50, 10)
	if err := f.s.Reserve(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Settle(f.ctx, r.Request.ID, domain.Settlement{Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if w := f.wallet(t); w.HeldMicros != 0 || w.BalanceMicros != 100 {
		t.Fatalf("failed request charged customer: %+v", w)
	}
	if err := f.s.Settle(f.ctx, r.Request.ID, domain.Settlement{Status: "completed", Usage: domain.Usage{Known: true}, ChargeMicros: 50, CostMicros: 8}); err != nil {
		t.Fatal(err)
	}
	var status string
	var cost int64
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT status,cost_micros FROM requests WHERE id=?", r.Request.ID).Scan(&status, &cost); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || cost != 8 {
		t.Fatalf("late cost changed terminal customer outcome: %s %d", status, cost)
	}
	if w := f.wallet(t); w.BalanceMicros != 100 {
		t.Fatal("late cost charged failed request")
	}
}

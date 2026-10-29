package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"relaypine.com/gateway/internal/domain"
)

const maxAmount int64 = 9000000000000000

// A local dollar budget cannot safely admit a request whose cost is untracked.
var ErrUntrackedCostBudget = errors.New("usage-only models require a credential without a local monetary cap")

const usageOnlyPending = "usage_only_pending"
const usageOnlyExpired = "usage_only_expired"

// Money transactions lock the request (if any), wallet, then credential. Keep
// this order when composing additional accounting operations with Store.DB.
func (s *Store) Reserve(ctx context.Context, r domain.Reservation) error {
	q := r.Request
	if r.CredentialID == "" {
		r.CredentialID = q.CredentialID
	}
	q.CredentialID = r.CredentialID
	mode := r.Model.EffectiveBillingMode()
	if mode != domain.BillingMetered && mode != domain.BillingUsageOnly {
		return errors.New("invalid billing mode")
	}
	usageOnly := mode == domain.BillingUsageOnly
	if usageOnly && (q.ReservedMicros != 0 || r.CostReserve != 0) {
		return errors.New("usage-only requests cannot reserve money")
	}
	if usageOnly {
		for _, rate := range append([]int64{r.Model.InputRate, r.Model.OutputRate, r.Model.CachedRate, r.Model.CacheWriteRate, r.Model.CostInputRate, r.Model.CostOutputRate, r.Model.CostCachedRate, r.Model.CostCacheWriteRate}, r.Model.ImageRates()...) {
			if rate != 0 {
				return errors.New("usage-only request snapshots cannot contain prices")
			}
		}
	}
	if !validID(q.ID) || !validID(q.TenantID) || !validID(q.ProjectID) || !validID(q.AccessKeyID) || !validID(r.CredentialID) || q.ReservedMicros < 0 || q.ReservedMicros > maxAmount || r.CostReserve < 0 || r.CostReserve > maxAmount {
		return errors.New("invalid reservation")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM requests WHERE id=? FOR UPDATE", q.ID).Scan(&existing)
	if err == nil {
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var balance, held int64
	var status string
	err = tx.QueryRowContext(ctx, "SELECT w.balance_micros,w.held_micros,t.status FROM wallets w JOIN tenants t ON t.id=w.tenant_id WHERE w.tenant_id=? FOR UPDATE", q.TenantID).Scan(&balance, &held, &status)
	if err != nil {
		return dbError(err)
	}
	if status != "active" {
		return ErrUnauthorized
	}
	if q.ReservedMicros > balance-held {
		return ErrInsufficientFunds
	}
	var enabled bool
	var keyTenant, keyProject string
	err = tx.QueryRowContext(ctx, "SELECT tenant_id,project_id,enabled FROM access_keys WHERE id=?", q.AccessKeyID).Scan(&keyTenant, &keyProject, &enabled)
	if err != nil {
		return dbError(err)
	}
	if !enabled || keyTenant != q.TenantID || keyProject != q.ProjectID {
		return ErrUnauthorized
	}
	var budget, spent, costHeld int64
	var raw []byte
	var endpointID string
	err = tx.QueryRowContext(ctx, "SELECT budget_micros,spent_micros,held_micros,config,endpoint_id FROM credentials WHERE id=? FOR UPDATE", r.CredentialID).Scan(&budget, &spent, &costHeld, &raw, &endpointID)
	if err != nil {
		return dbError(err)
	}
	var cred domain.Credential
	if err = json.Unmarshal(raw, &cred); err != nil {
		return err
	}
	if !cred.Enabled {
		return ErrUnauthorized
	}
	if usageOnly && budget > 0 {
		return ErrUntrackedCostBudget
	}
	if !budgetAllows(budget, spent, costHeld, r.CostReserve) {
		return ErrBudgetExceeded
	}
	var endpointRaw []byte
	if err = tx.QueryRowContext(ctx, "SELECT config FROM endpoints WHERE id=?", endpointID).Scan(&endpointRaw); err != nil {
		return dbError(err)
	}
	var endpoint domain.Endpoint
	if err = json.Unmarshal(endpointRaw, &endpoint); err != nil {
		return err
	}
	if !endpoint.Enabled || !endpoint.Serves(q.Provider) {
		return ErrUnauthorized
	}
	now := time.Now().UTC()
	q.CreatedAt = now
	usageRaw, _ := json.Marshal(domain.Usage{})
	modelRaw, err := json.Marshal(r.Model)
	if err != nil {
		return err
	}
	phase := "reserved"
	if usageOnly {
		phase = usageOnlyPending
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE wallets SET held_micros=held_micros+? WHERE tenant_id=?", q.ReservedMicros, q.TenantID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE credentials SET held_micros=held_micros+? WHERE id=?", r.CostReserve, r.CredentialID)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO requests(id,tenant_id,project_id,access_key_id,credential_id,provider,model,status,phase,usage_json,price_snapshot,reserved_micros,cost_reserved_micros,customer_hold_active,cost_hold_active,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'in_progress',?,?,?,?,?,?,?,?,?)`, q.ID, q.TenantID, q.ProjectID, q.AccessKeyID, q.CredentialID, q.Provider, q.Model, phase, usageRaw, modelRaw, q.ReservedMicros, r.CostReserve, !usageOnly, !usageOnly, now, now)
	if err != nil {
		return dbError(err)
	}
	// Send intention is durable before network I/O. A crash from this point can
	// mean the upstream accepted the call, so recovery never assumes no cost.
	_, err = tx.ExecContext(ctx, "INSERT INTO attempts(id,request_id,credential_id,status,reserved_micros,created_at,updated_at) VALUES(?,?,?,'send_intent',?,?,?)", q.ID+"_1", q.ID, r.CredentialID, r.CostReserve, now, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func budgetAllows(budget, spent, held, reserve int64) bool {
	if spent < 0 || held < 0 || reserve < 0 || held > math.MaxInt64-reserve {
		return false
	}
	if budget == 0 {
		return true
	}
	return spent <= budget && held <= budget-spent && reserve <= budget-spent-held
}
func customerCharge(reserved, charge int64, status string) int64 {
	if status == "error" || status == "failed" || status == "not_sent" {
		return 0
	}
	if charge > reserved {
		return reserved
	}
	return charge
}

func (s *Store) Settle(ctx context.Context, requestID string, v domain.Settlement) error {
	if v.ChargeMicros < 0 || v.ChargeMicros > maxAmount || v.CostMicros < 0 || v.CostMicros > maxAmount || v.Usage.InputTokens < 0 || v.Usage.OutputTokens < 0 || v.Usage.CachedTokens < 0 || v.Usage.CacheWriteTokens < 0 || v.Usage.ImageInputTokens < 0 || v.Usage.ImageOutputTokens < 0 || v.Usage.ImageTextTokens < 0 || v.LatencyMS < 0 || len(v.ErrorCode) > 128 {
		return errors.New("invalid settlement")
	}
	switch v.Status {
	case "completed", "success", "canceled", "error", "failed", "unknown", "pending_usage", "not_sent":
	default:
		return errors.New("invalid settlement status")
	}
	if v.Status == "not_sent" {
		v.Usage = domain.Usage{Known: true}
		v.ChargeMicros = 0
		v.CostMicros = 0
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tenant, cred, phase, oldStatus string
	var reserved, costReserved, oldCharge int64
	var customerHeld, costHeld bool
	var modelRaw []byte
	var oldLatency int64
	var oldError string
	err = tx.QueryRowContext(ctx, "SELECT tenant_id,credential_id,phase,status,reserved_micros,cost_reserved_micros,charge_micros,customer_hold_active,cost_hold_active,price_snapshot,latency_ms,error_code FROM requests WHERE id=? FOR UPDATE", requestID).Scan(&tenant, &cred, &phase, &oldStatus, &reserved, &costReserved, &oldCharge, &customerHeld, &costHeld, &modelRaw, &oldLatency, &oldError)
	if err != nil {
		return dbError(err)
	}
	if phase == "settled" {
		return tx.Commit()
	}
	var model domain.Model
	if err = json.Unmarshal(modelRaw, &model); err != nil {
		return err
	}
	if model.EffectiveBillingMode() == domain.BillingUsageOnly {
		if customerHeld || costHeld || reserved != 0 || costReserved != 0 || oldCharge != 0 {
			return errors.New("usage-only monetary state invariant violated")
		}
		prior := domain.Settlement{Status: oldStatus, LatencyMS: oldLatency, ErrorCode: oldError}
		return settleUsageOnly(ctx, tx, requestID, phase, prior, v)
	}
	if model.EffectiveBillingMode() != domain.BillingMetered {
		return errors.New("invalid billing mode in request snapshot")
	}
	customerWasHeld := customerHeld
	var walletHeld, balance int64
	if err = tx.QueryRowContext(ctx, "SELECT balance_micros,held_micros FROM wallets WHERE tenant_id=? FOR UPDATE", tenant).Scan(&balance, &walletHeld); err != nil {
		return dbError(err)
	}
	var upstreamHeld, spent int64
	if err = tx.QueryRowContext(ctx, "SELECT spent_micros,held_micros FROM credentials WHERE id=? FOR UPDATE", cred).Scan(&spent, &upstreamHeld); err != nil {
		return dbError(err)
	}
	failed := v.Status == "error" || v.Status == "failed" || v.Status == "not_sent"
	charge := oldCharge
	status := v.Status
	newPhase := "pending_usage"
	if customerHeld && (v.Usage.Known || failed) {
		charge = customerCharge(reserved, v.ChargeMicros, v.Status)
		if walletHeld < reserved || balance < charge {
			return errors.New("wallet reservation invariant violated")
		}
		_, err = tx.ExecContext(ctx, "UPDATE wallets SET held_micros=held_micros-?,balance_micros=balance_micros-? WHERE tenant_id=?", reserved, charge, tenant)
		if err != nil {
			return err
		}
		if err = insertLedger(ctx, tx, tenant, "charge", -charge, "request:"+requestID, "API usage settlement"); err != nil {
			return err
		}
		customerHeld = false
	}
	if costHeld && v.Usage.Known {
		if upstreamHeld < costReserved || spent > math.MaxInt64-v.CostMicros {
			return errors.New("upstream reservation invariant violated")
		}
		_, err = tx.ExecContext(ctx, "UPDATE credentials SET held_micros=held_micros-?,spent_micros=spent_micros+? WHERE id=?", costReserved, v.CostMicros, cred)
		if err != nil {
			return err
		}
		costHeld = false
	}
	if !customerHeld && !costHeld {
		newPhase = "settled"
	} else if !customerHeld {
		newPhase = "cost_pending"
	}
	if customerHeld {
		status = "pending_usage"
	} else if !customerWasHeld {
		status = oldStatus
	}
	cost := v.CostMicros
	if !v.Usage.Known {
		cost = 0
	}
	usageRaw, err := json.Marshal(v.Usage)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE requests SET status=?,phase=?,usage_json=?,charge_micros=?,cost_micros=?,customer_hold_active=?,cost_hold_active=?,latency_ms=?,error_code=?,updated_at=? WHERE id=?", status, newPhase, usageRaw, charge, cost, customerHeld, costHeld, v.LatencyMS, v.ErrorCode, time.Now().UTC(), requestID)
	if err != nil {
		return err
	}
	attemptStatus := "pending_usage"
	if !costHeld {
		attemptStatus = "settled"
	}
	// Attempts closed by a reroute keep their own final state.
	_, err = tx.ExecContext(ctx, "UPDATE attempts SET status=?,cost_micros=?,error_code=?,updated_at=? WHERE request_id=? AND status<>?", attemptStatus, cost, v.ErrorCode, time.Now().UTC(), requestID, attemptFailedOver)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Usage-only requests persist execution evidence independently of money. A
// trustworthy late usage report can fill an unknown total, but cannot change a
// completed failure/cancellation into success or introduce a charge later.
func settleUsageOnly(ctx context.Context, tx *sql.Tx, id, phase string, prior, v domain.Settlement) error {
	if phase == usageOnlyExpired && !v.Usage.Known {
		return tx.Commit()
	}
	if prior.Status != "in_progress" && prior.Status != "unknown" && prior.Status != "pending_usage" && prior.Status != "usage_unavailable" {
		v.Status, v.LatencyMS, v.ErrorCode = prior.Status, prior.LatencyMS, prior.ErrorCode
	}
	if v.Usage.Known && v.ErrorCode == "reconciliation_expired" {
		// Expiration describes missing usage, not an execution failure. Late
		// evidence resolves it while retaining any genuine upstream error.
		v.ErrorCode = ""
	}
	phase = usageOnlyPending
	attemptStatus := "usage_pending"
	if v.Usage.Known {
		phase, attemptStatus = "settled", "usage_recorded"
	}
	raw, err := json.Marshal(v.Usage)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE requests SET status=?,phase=?,usage_json=?,charge_micros=0,cost_micros=0,latency_ms=?,error_code=?,updated_at=? WHERE id=?", v.Status, phase, raw, v.LatencyMS, v.ErrorCode, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE attempts SET status=?,cost_micros=0,updated_at=? WHERE request_id=? AND status<>?", attemptStatus, time.Now().UTC(), id, attemptFailedOver); err != nil {
		return err
	}
	return tx.Commit()
}

func insertLedger(ctx context.Context, tx *sql.Tx, tenant, kind string, amount int64, reference, reason string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO ledger_entries(id,tenant_id,kind,amount_micros,reference,reason,created_at) VALUES(?,?,?,?,?,?,?)", newID("led_"), tenant, kind, amount, reference, reason, time.Now().UTC())
	return dbError(err)
}
func (s *Store) Credit(ctx context.Context, tenantID string, amountMicros int64, reference, reason string) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.CreditTx(ctx, tx, tenantID, amountMicros, reference, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// CreditTx locks the wallet and writes both its balance and immutable ledger
// entry. The caller must commit its payment/order state in this same transaction.
// Repeating a reference with the same credit is a no-op; changing the amount is
// rejected. References are scoped to a tenant and must be stable business IDs.
func (s *Store) CreditTx(ctx context.Context, tx *sql.Tx, tenantID string, amountMicros int64, reference, reason string) error {
	if amountMicros <= 0 || amountMicros > maxAmount || strings.TrimSpace(reference) == "" || len(reference) > 191 || strings.TrimSpace(reason) == "" || len(reason) > 512 {
		return errors.New("positive credit, business reference, and audit reason required")
	}
	var balance int64
	if err := tx.QueryRowContext(ctx, "SELECT balance_micros FROM wallets WHERE tenant_id=? FOR UPDATE", tenantID).Scan(&balance); err != nil {
		return dbError(err)
	}
	var oldAmount int64
	var oldKind string
	err := tx.QueryRowContext(ctx, "SELECT kind,amount_micros FROM ledger_entries WHERE tenant_id=? AND reference=?", tenantID, reference).Scan(&oldKind, &oldAmount)
	if err == nil {
		if oldKind == "credit" && oldAmount == amountMicros {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if balance > math.MaxInt64-amountMicros {
		return errors.New("wallet balance limit exceeded")
	}
	if err = insertLedger(ctx, tx, tenantID, "credit", amountMicros, reference, reason); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE wallets SET balance_micros=balance_micros+? WHERE tenant_id=?", amountMicros, tenantID)
	return err
}

// Recover releases customer reservations only after the published 24-hour
// reconciliation window. Unknown upstream costs remain held for operator
// reconciliation; late trustworthy usage can settle those costs with Settle.
func (s *Store) Recover(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM requests WHERE (customer_hold_active=TRUE OR phase=?) AND created_at<? ORDER BY created_at LIMIT 500", usageOnlyPending, cutoff)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.recoverRequest(ctx, id, cutoff); err != nil {
			return err
		}
	}
	_, err = s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<?", time.Now().UTC())
	return err
}
func (s *Store) recoverRequest(ctx context.Context, id string, cutoff time.Time) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tenant string
	var reserved int64
	var active, costActive bool
	var created time.Time
	var oldPhase string
	var modelRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT tenant_id,reserved_micros,customer_hold_active,cost_hold_active,created_at,phase,price_snapshot FROM requests WHERE id=? FOR UPDATE", id).Scan(&tenant, &reserved, &active, &costActive, &created, &oldPhase, &modelRaw)
	if err != nil {
		return dbError(err)
	}
	if !created.Before(cutoff) {
		return tx.Commit()
	}
	var model domain.Model
	if err = json.Unmarshal(modelRaw, &model); err != nil {
		return err
	}
	if model.EffectiveBillingMode() == domain.BillingUsageOnly {
		if active || costActive || reserved != 0 {
			return errors.New("usage-only monetary state invariant violated")
		}
		if oldPhase != usageOnlyPending {
			return tx.Commit()
		}
		_, err = tx.ExecContext(ctx, "UPDATE requests SET status=CASE WHEN status IN ('in_progress','unknown','pending_usage') THEN 'usage_unavailable' ELSE status END,phase=?,error_code=CASE WHEN error_code='' THEN 'reconciliation_expired' ELSE error_code END,updated_at=? WHERE id=?", usageOnlyExpired, time.Now().UTC(), id)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE attempts SET status='usage_unavailable',updated_at=? WHERE request_id=? AND status<>?", time.Now().UTC(), id, attemptFailedOver); err != nil {
			return err
		}
		return tx.Commit()
	}
	if model.EffectiveBillingMode() != domain.BillingMetered {
		return errors.New("invalid billing mode in request snapshot")
	}
	if !active {
		return tx.Commit()
	}
	var held int64
	if err = tx.QueryRowContext(ctx, "SELECT held_micros FROM wallets WHERE tenant_id=? FOR UPDATE", tenant).Scan(&held); err != nil {
		return dbError(err)
	}
	if held < reserved {
		return errors.New("wallet reservation invariant violated")
	}
	_, err = tx.ExecContext(ctx, "UPDATE wallets SET held_micros=held_micros-? WHERE tenant_id=?", reserved, tenant)
	if err != nil {
		return err
	}
	if err = insertLedger(ctx, tx, tenant, "charge", 0, "request:"+id, "Unconfirmed usage released after 24 hours; no customer charge"); err != nil {
		return err
	}
	phase := "settled"
	if costActive {
		phase = "cost_pending"
	}
	_, err = tx.ExecContext(ctx, "UPDATE requests SET status='usage_unavailable',phase=?,customer_hold_active=FALSE,charge_micros=0,error_code='reconciliation_expired',updated_at=? WHERE id=?", phase, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if costActive {
		if _, err = tx.ExecContext(ctx, "UPDATE attempts SET status='pending_usage',updated_at=? WHERE request_id=? AND status<>?", time.Now().UTC(), id, attemptFailedOver); err != nil {
			return err
		}
	}
	return tx.Commit()
}

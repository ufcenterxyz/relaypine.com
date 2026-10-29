package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"relaypine.com/gateway/internal/domain"
)

// attemptFailedOver marks an attempt that an upstream refused before any
// output. It is final: settlement and recovery update only the active attempt.
const attemptFailedOver = "failed_over"

// Reroute moves an in-flight metered request from its current credential to
// another one after the current attempt was definitively refused. In one
// transaction it closes that attempt at zero upstream cost, releases its
// upstream budget hold, reserves the next credential's budget and records a
// new send intent. The customer's wallet hold is unchanged, so the customer is
// still charged at most once, at the request's price snapshot.
func (s *Store) Reroute(ctx context.Context, requestID string, v domain.Reroute) error {
	if !validID(requestID) || !validID(v.FromCredentialID) || !validID(v.ToCredentialID) || v.FromCredentialID == v.ToCredentialID || v.CostReserve < 0 || v.CostReserve > maxAmount || len(v.ErrorCode) > 128 {
		return errors.New("invalid reroute")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var credential, provider, phase, status string
	var costReserved int64
	var customerHeld, costHeld bool
	var modelRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT credential_id,provider,phase,status,cost_reserved_micros,customer_hold_active,cost_hold_active,price_snapshot FROM requests WHERE id=? FOR UPDATE", requestID).Scan(&credential, &provider, &phase, &status, &costReserved, &customerHeld, &costHeld, &modelRaw)
	if err != nil {
		return dbError(err)
	}
	var model domain.Model
	if err = json.Unmarshal(modelRaw, &model); err != nil {
		return err
	}
	// Only a request that is still waiting on its current attempt can move. A
	// settled, recovered or already rerouted request must not be sent again.
	if credential != v.FromCredentialID || phase != "reserved" || status != "in_progress" || !customerHeld || !costHeld || model.EffectiveBillingMode() != domain.BillingMetered {
		return ErrConflict
	}
	// Lock both credentials in ID order so concurrent reroutes cannot deadlock.
	first, second := v.FromCredentialID, v.ToCredentialID
	if second < first {
		first, second = second, first
	}
	type budgetRow struct {
		budget, spent, held int64
		config              domain.Credential
		endpointID          string
	}
	rows := map[string]*budgetRow{}
	for _, id := range []string{first, second} {
		var row budgetRow
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT budget_micros,spent_micros,held_micros,config,endpoint_id FROM credentials WHERE id=? FOR UPDATE", id).Scan(&row.budget, &row.spent, &row.held, &raw, &row.endpointID); err != nil {
			return dbError(err)
		}
		if err = json.Unmarshal(raw, &row.config); err != nil {
			return err
		}
		rows[id] = &row
	}
	from, to := rows[v.FromCredentialID], rows[v.ToCredentialID]
	if from.held < costReserved {
		return errors.New("upstream reservation invariant violated")
	}
	if !to.config.Enabled {
		return ErrUnauthorized
	}
	var endpointRaw []byte
	if err = tx.QueryRowContext(ctx, "SELECT config FROM endpoints WHERE id=?", to.endpointID).Scan(&endpointRaw); err != nil {
		return dbError(err)
	}
	var endpoint domain.Endpoint
	if err = json.Unmarshal(endpointRaw, &endpoint); err != nil {
		return err
	}
	if !endpoint.Enabled || !endpoint.Serves(provider) {
		return ErrUnauthorized
	}
	if !budgetAllows(to.budget, to.spent, to.held, v.CostReserve) {
		return ErrBudgetExceeded
	}
	var attempts int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts WHERE request_id=?", requestID).Scan(&attempts); err != nil {
		return err
	}
	now := time.Now().UTC()
	closed, err := tx.ExecContext(ctx, "UPDATE attempts SET status=?,cost_micros=0,error_code=?,updated_at=? WHERE id=? AND request_id=? AND credential_id=? AND status='send_intent'", attemptFailedOver, v.ErrorCode, now, fmt.Sprintf("%s_%d", requestID, attempts), requestID, v.FromCredentialID)
	if err != nil {
		return err
	}
	if n, err := closed.RowsAffected(); err != nil || n != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE credentials SET held_micros=held_micros-? WHERE id=?", costReserved, v.FromCredentialID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE credentials SET held_micros=held_micros+? WHERE id=?", v.CostReserve, v.ToCredentialID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE requests SET credential_id=?,cost_reserved_micros=?,updated_at=? WHERE id=?", v.ToCredentialID, v.CostReserve, now, requestID); err != nil {
		return err
	}
	// The send intent is durable before the next network attempt, exactly as
	// for the first one.
	if _, err = tx.ExecContext(ctx, "INSERT INTO attempts(id,request_id,credential_id,status,reserved_micros,created_at,updated_at) VALUES(?,?,?,'send_intent',?,?,?)", fmt.Sprintf("%s_%d", requestID, attempts+1), requestID, v.ToCredentialID, v.CostReserve, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// listAttempts returns the attempt trail of rerouted requests, keyed by request.
func (s *Store) listAttempts(ctx context.Context, requestIDs []string) (map[string][]domain.Attempt, error) {
	out := map[string][]domain.Attempt{}
	if len(requestIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(requestIDs))
	marks := make([]byte, 0, len(requestIDs)*2)
	for i, id := range requestIDs {
		args[i] = id
		if i > 0 {
			marks = append(marks, ',')
		}
		marks = append(marks, '?')
	}
	// IDs end in the attempt number, at most maxUpstreamAttempts, so they sort
	// in attempt order.
	rows, err := s.DB.QueryContext(ctx, "SELECT request_id,credential_id,status,error_code FROM attempts WHERE request_id IN ("+string(marks)+") ORDER BY request_id,id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var requestID string
		var a domain.Attempt
		if err = rows.Scan(&requestID, &a.CredentialID, &a.Status, &a.ErrorCode); err != nil {
			return nil, err
		}
		out[requestID] = append(out[requestID], a)
	}
	return out, rows.Err()
}

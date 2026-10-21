package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"relaypine.com/gateway/internal/domain"
)

func (s *Store) Snapshot(ctx context.Context, user domain.User) (domain.Snapshot, error) {
	result := domain.Snapshot{User: user, Keys: []domain.AccessKey{}, Models: []domain.Model{}, Requests: []domain.Request{}, Ledger: []domain.LedgerEntry{}, Waitlist: []domain.WaitlistEntry{}}
	if user.ID == "" || user.TenantID == "" || (user.Operator && !s.IsSuperAdmin(user)) {
		return result, ErrUnauthorized
	}
	result.User.PasswordHash = ""
	err := s.DB.QueryRowContext(ctx, "SELECT t.id,t.name,t.status,t.created_at FROM tenants t JOIN users u ON u.tenant_id=t.id WHERE t.id=? AND u.id=? AND BINARY u.email=? AND u.password_hash='!email-otp' AND (u.operator=FALSE OR BINARY u.email=?) AND (?=FALSE OR u.operator=TRUE) AND t.status='active'", user.TenantID, user.ID, NormalizeEmail(user.Email), s.superAdminEmail, user.Operator).Scan(&result.Tenant.ID, &result.Tenant.Name, &result.Tenant.Status, &result.Tenant.CreatedAt)
	if err != nil {
		return result, dbError(err)
	}
	err = s.DB.QueryRowContext(ctx, "SELECT id,tenant_id,name FROM projects WHERE tenant_id=? ORDER BY created_at,id LIMIT 1", user.TenantID).Scan(&result.Project.ID, &result.Project.TenantID, &result.Project.Name)
	if err != nil {
		return result, dbError(err)
	}
	err = s.DB.QueryRowContext(ctx, "SELECT tenant_id,balance_micros,held_micros,currency FROM wallets WHERE tenant_id=?", user.TenantID).Scan(&result.Wallet.TenantID, &result.Wallet.BalanceMicros, &result.Wallet.HeldMicros, &result.Wallet.Currency)
	if err != nil {
		return result, dbError(err)
	}
	if result.Keys, err = s.listKeys(ctx, user.TenantID); err != nil {
		return result, err
	}
	if result.Models, err = s.ListModels(ctx); err != nil {
		return result, err
	}
	if !user.Operator {
		models := []domain.Model{}
		for _, m := range result.Models {
			if !m.Enabled {
				continue
			}
			m.CostInputRate = 0
			m.CostOutputRate = 0
			m.CostCachedRate = 0
			m.CostCacheWriteRate = 0
			m.UpstreamModel = ""
			m.PricingReference = nil
			m.PricingNote = ""
			models = append(models, m)
		}
		result.Models = models
	}
	if result.Routing, err = s.Routing(ctx); err != nil {
		return result, err
	}
	if !user.Operator {
		result.Routing.Strategy = ""
	}
	if result.Requests, err = s.listRequests(ctx, user); err != nil {
		return result, err
	}
	if result.Ledger, err = s.listLedger(ctx, user.TenantID); err != nil {
		return result, err
	}
	if user.Operator {
		if result.Endpoints, err = s.ListEndpoints(ctx); err != nil {
			return result, err
		}
		if result.Credentials, err = s.ListCredentials(ctx); err != nil {
			return result, err
		}
		for i := range result.Credentials {
			result.Credentials[i].Secret = ""
		}
		if result.Proxies, err = s.ListProxies(ctx); err != nil {
			return result, err
		}
		for i := range result.Proxies {
			result.Proxies[i].URL = ""
		}
		if result.Customers, err = s.listTenants(ctx); err != nil {
			return result, err
		}
	}
	return result, nil
}
func (s *Store) listKeys(ctx context.Context, tenant string) ([]domain.AccessKey, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,tenant_id,project_id,name,prefix,enabled,rpm,models,created_at FROM access_keys WHERE tenant_id=? ORDER BY created_at DESC LIMIT 500", tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AccessKey{}
	for rows.Next() {
		var k domain.AccessKey
		var raw []byte
		if err = rows.Scan(&k.ID, &k.TenantID, &k.ProjectID, &k.Name, &k.Prefix, &k.Enabled, &k.RPM, &raw, &k.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &k.Models); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
func (s *Store) listRequests(ctx context.Context, user domain.User) ([]domain.Request, error) {
	query := "SELECT id,tenant_id,project_id,access_key_id,credential_id,provider,model,status,usage_json,charge_micros,cost_micros,reserved_micros,latency_ms,error_code,created_at,price_snapshot,cost_hold_active FROM requests"
	args := []any{}
	if !user.Operator {
		query += " WHERE tenant_id=?"
		args = append(args, user.TenantID)
	}
	query += " ORDER BY created_at DESC LIMIT 100"
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Request{}
	for rows.Next() {
		var r domain.Request
		var raw []byte
		var modelRaw []byte
		var costHeld bool
		if err = rows.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.AccessKeyID, &r.CredentialID, &r.Provider, &r.Model, &r.Status, &raw, &r.ChargeMicros, &r.CostMicros, &r.ReservedMicros, &r.LatencyMS, &r.ErrorCode, &r.CreatedAt, &modelRaw, &costHeld); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &r.Usage); err != nil {
			return nil, err
		}
		var model domain.Model
		if err = json.Unmarshal(modelRaw, &model); err != nil {
			return nil, err
		}
		r.BillingMode = model.EffectiveBillingMode()
		r.CostStatus = "pending"
		if r.BillingMode == domain.BillingUsageOnly {
			r.CostStatus = "untracked"
		} else if r.BillingMode == domain.BillingMetered && !costHeld && r.Usage.Known {
			r.CostStatus = "recorded"
		}
		if !user.Operator {
			r.CredentialID = ""
			r.CostMicros = 0
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) listLedger(ctx context.Context, tenant string) ([]domain.LedgerEntry, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,tenant_id,kind,amount_micros,reference,reason,created_at FROM ledger_entries WHERE tenant_id=? ORDER BY created_at DESC LIMIT 100", tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.LedgerEntry{}
	for rows.Next() {
		var l domain.LedgerEntry
		if err = rows.Scan(&l.ID, &l.TenantID, &l.Kind, &l.AmountMicros, &l.Reference, &l.Reason, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
func (s *Store) listTenants(ctx context.Context) ([]domain.Tenant, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT t.id,t.name,t.status,t.created_at,COALESCE((SELECT u.email FROM users u WHERE u.tenant_id=t.id ORDER BY u.created_at,u.id LIMIT 1),'') FROM tenants t ORDER BY t.created_at DESC,t.id LIMIT 500")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Tenant{}
	for rows.Next() {
		var t domain.Tenant
		if err = rows.Scan(&t.ID, &t.Name, &t.Status, &t.CreatedAt, &t.Email); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SaveResource(ctx context.Context, r domain.Resource) error {
	if len(r.ID) == 0 || len(r.ID) > 255 || !validID(r.TenantID) || !validID(r.ProjectID) || !validID(r.CredentialID) || !validID(r.EndpointID) || len(r.Model) == 0 || len(r.Model) > 200 || r.InputTokens < 0 {
		return errors.New("invalid resource mapping")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? AND tenant_id=?", r.ProjectID, r.TenantID).Scan(&project); err != nil {
		return dbError(err)
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO resources(id,tenant_id,project_id,credential_id,endpoint_id,model,input_tokens,created_at) VALUES(?,?,?,?,?,?,?,?)", r.ID, r.TenantID, r.ProjectID, r.CredentialID, r.EndpointID, r.Model, r.InputTokens, time.Now().UTC())
	if errors.Is(dbError(err), ErrConflict) {
		var old domain.Resource
		err = tx.QueryRowContext(ctx, "SELECT credential_id,endpoint_id,model,input_tokens FROM resources WHERE tenant_id=? AND project_id=? AND id=?", r.TenantID, r.ProjectID, r.ID).Scan(&old.CredentialID, &old.EndpointID, &old.Model, &old.InputTokens)
		if err != nil {
			return err
		}
		if old.CredentialID != r.CredentialID || old.EndpointID != r.EndpointID || old.Model != r.Model || old.InputTokens != r.InputTokens {
			return ErrConflict
		}
	} else if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Resource(ctx context.Context, tenantID, projectID, id string) (domain.Resource, error) {
	var r domain.Resource
	err := s.DB.QueryRowContext(ctx, "SELECT id,tenant_id,project_id,credential_id,endpoint_id,model,input_tokens FROM resources WHERE tenant_id=? AND project_id=? AND id=?", tenantID, projectID, id).Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.CredentialID, &r.EndpointID, &r.Model, &r.InputTokens)
	return r, dbError(err)
}

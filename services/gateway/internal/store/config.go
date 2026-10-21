package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"relaypine.com/gateway/internal/domain"
)

var (
	ErrInvalidWorkspaceID        = errors.New("invalid Anthropic workspace ID")
	ErrWorkspaceProviderMismatch = errors.New("workspace selection requires an Anthropic endpoint")
)

func listConfig[T any](ctx context.Context, db *sql.DB, table string) ([]T, error) {
	// Table is a package constant at every call site, never input from a client.
	rows, err := db.QueryContext(ctx, "SELECT config FROM "+table+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var raw []byte
		var value T
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}
func (s *Store) ListEndpoints(ctx context.Context) ([]domain.Endpoint, error) {
	return listConfig[domain.Endpoint](ctx, s.DB, "endpoints")
}
func (s *Store) ListModels(ctx context.Context) ([]domain.Model, error) {
	return listConfig[domain.Model](ctx, s.DB, "models")
}
func (s *Store) ListCredentials(ctx context.Context) ([]domain.Credential, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT config,encrypted_secret,budget_micros,spent_micros,held_micros FROM credentials ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Credential{}
	for rows.Next() {
		var raw, encrypted []byte
		var c domain.Credential
		var budget, spent, held int64
		if err = rows.Scan(&raw, &encrypted, &budget, &spent, &held); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		c.BudgetMicros = budget
		c.SpentMicros = spent
		c.HeldMicros = held
		c.Secret, err = s.decrypt(encrypted, "credential:"+c.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) ListProxies(ctx context.Context) ([]domain.Proxy, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT config,encrypted_url FROM proxies ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Proxy{}
	for rows.Next() {
		var raw, encrypted []byte
		var p domain.Proxy
		if err = rows.Scan(&raw, &encrypted); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		p.URL, err = s.decrypt(encrypted, "proxy:"+p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) SaveEndpoint(ctx context.Context, e domain.Endpoint) (domain.Endpoint, error) {
	if e.ID == "" {
		e.ID = newID("end_")
	}
	if e.Provider != "openai" && e.Provider != "anthropic" {
		return e, errors.New("unsupported provider")
	}
	if e.BaseURL == "" {
		if e.Provider == "openai" {
			e.BaseURL = "https://api.openai.com"
		} else {
			e.BaseURL = "https://api.anthropic.com"
		}
	}
	u, err := url.Parse(e.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !validID(e.ID) || !validName(e.Name) {
		return e, errors.New("invalid endpoint configuration")
	}
	if e.AuthMode == "" {
		if e.Provider == "anthropic" {
			e.AuthMode = "x-api-key"
		} else {
			e.AuthMode = "bearer"
		}
	}
	if e.AuthMode != "bearer" && e.AuthMode != "x-api-key" {
		return e, errors.New("unsupported authentication mode")
	}
	if len(e.Models) > 200 {
		return e, errors.New("too many endpoint models")
	}
	if e.Models == nil {
		e.Models = []string{}
	}
	e.BaseURL = strings.TrimRight(e.BaseURL, "/")
	raw, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	_, err = s.DB.ExecContext(ctx, "INSERT INTO endpoints(id,config) VALUES(?,?) ON DUPLICATE KEY UPDATE config=VALUES(config)", e.ID, raw)
	return e, dbError(err)
}
func (s *Store) SaveCredential(ctx context.Context, c domain.Credential) (domain.Credential, error) {
	c.WorkspaceID = strings.TrimSpace(c.WorkspaceID)
	if !domain.ValidAnthropicWorkspaceID(c.WorkspaceID) {
		return c, ErrInvalidWorkspaceID
	}
	if c.ID == "" {
		c.ID = newID("cred_")
	}
	if !validID(c.ID) || !validID(c.EndpointID) || !validName(c.Name) || len(c.Secret) > 8192 || c.RPM < 1 || c.RPM > 1000000 || c.TPM < 0 || c.TPM > 1000000000 || c.MaxConcurrency < 1 || c.MaxConcurrency > 10000 || c.BudgetMicros < 0 || c.BudgetMicros > 9000000000000000 || len(c.QuotaGroup) > 128 {
		return c, errors.New("invalid credential configuration")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	var endpointRaw []byte
	if err = tx.QueryRowContext(ctx, "SELECT config FROM endpoints WHERE id=? FOR SHARE", c.EndpointID).Scan(&endpointRaw); err != nil {
		return c, dbError(err)
	}
	var endpoint domain.Endpoint
	if err = json.Unmarshal(endpointRaw, &endpoint); err != nil {
		return c, err
	}
	if c.WorkspaceID != "" && endpoint.Provider != "anthropic" {
		return c, ErrWorkspaceProviderMismatch
	}
	var encrypted []byte
	var oldRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT encrypted_secret,config,spent_micros,held_micros FROM credentials WHERE id=? FOR UPDATE", c.ID).Scan(&encrypted, &oldRaw, &c.SpentMicros, &c.HeldMicros)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	if !exists {
		c.SpentMicros = 0
		c.HeldMicros = 0
	}
	if c.Secret == "" {
		if !exists {
			return c, errors.New("upstream API key required")
		}
		c.Secret, err = s.decrypt(encrypted, "credential:"+c.ID)
		if err != nil {
			return c, err
		}
	}
	if c.BudgetMicros > 0 && (c.SpentMicros > c.BudgetMicros || c.HeldMicros > c.BudgetMicros-c.SpentMicros) {
		return c, ErrBudgetExceeded
	}
	if c.ProxyID != "" {
		var id string
		if err = tx.QueryRowContext(ctx, "SELECT id FROM proxies WHERE id=?", c.ProxyID).Scan(&id); err != nil {
			return c, dbError(err)
		}
	}
	encrypted, err = s.encrypt(c.Secret, "credential:"+c.ID)
	if err != nil {
		return c, err
	}
	c.MaskedKey = maskSecret(c.Secret)
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO credentials(id,endpoint_id,config,encrypted_secret,budget_micros,spent_micros,held_micros) VALUES(?,?,?,?,?,0,0) ON DUPLICATE KEY UPDATE endpoint_id=VALUES(endpoint_id),config=VALUES(config),encrypted_secret=VALUES(encrypted_secret),budget_micros=VALUES(budget_micros)", c.ID, c.EndpointID, raw, encrypted, c.BudgetMicros)
	if err != nil {
		return c, dbError(err)
	}
	return c, tx.Commit()
}
func maskSecret(value string) string {
	if len(value) <= 8 {
		return "••••••••"
	}
	return value[:3] + "••••••••" + value[len(value)-4:]
}
func (s *Store) SaveProxy(ctx context.Context, p domain.Proxy) (domain.Proxy, error) {
	if p.ID == "" {
		p.ID = newID("proxy_")
	}
	if !validID(p.ID) || !validName(p.Name) || len(p.URL) > 4096 {
		return p, errors.New("invalid proxy configuration")
	}
	if p.URL == "" {
		var encrypted []byte
		if err := s.DB.QueryRowContext(ctx, "SELECT encrypted_url FROM proxies WHERE id=?", p.ID).Scan(&encrypted); err != nil {
			return p, dbError(err)
		}
		var err error
		p.URL, err = s.decrypt(encrypted, "proxy:"+p.ID)
		if err != nil {
			return p, err
		}
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.Hostname() == "" || u.Port() == "" || (u.Scheme != "socks5" && u.Scheme != "socks5h") || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return p, errors.New("SOCKS5 URL including host and port required")
	}
	masked := *u
	if u.User != nil {
		masked.User = url.UserPassword("••••", "••••")
	}
	p.MaskedURL = masked.String()
	encrypted, err := s.encrypt(p.URL, "proxy:"+p.ID)
	if err != nil {
		return p, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	_, err = s.DB.ExecContext(ctx, "INSERT INTO proxies(id,config,encrypted_url) VALUES(?,?,?) ON DUPLICATE KEY UPDATE config=VALUES(config),encrypted_url=VALUES(encrypted_url)", p.ID, raw, encrypted)
	return p, dbError(err)
}
func (s *Store) SaveModel(ctx context.Context, m domain.Model) (domain.Model, error) {
	if m.ID == "" {
		m.ID = newID("model_")
	}
	if !validID(m.ID) || !validName(m.Name) || !validName(m.UpstreamModel) || (m.Provider != "openai" && m.Provider != "anthropic") || m.MaxOutputTokens < 1 || m.MaxOutputTokens > 1000000 {
		return m, errors.New("invalid model configuration")
	}
	for _, rate := range []int64{m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate, m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate} {
		if rate < 0 || rate > 1000000000000 {
			return m, errors.New("model rates outside supported range")
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	var version int
	err = tx.QueryRowContext(ctx, "SELECT version FROM models WHERE id=? FOR UPDATE", m.ID).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return m, err
	}
	m.Version = version + 1
	raw, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO models(id,config,version) VALUES(?,?,?) ON DUPLICATE KEY UPDATE config=VALUES(config),version=VALUES(version)", m.ID, raw, m.Version)
	if err != nil {
		return m, dbError(err)
	}
	return m, tx.Commit()
}
func (s *Store) Routing(ctx context.Context) (domain.RouteConfig, error) {
	var r domain.RouteConfig
	var raw []byte
	err := s.DB.QueryRowContext(ctx, "SELECT config FROM settings WHERE id='routing'").Scan(&raw)
	if err != nil {
		return r, dbError(err)
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (s *Store) SaveRouting(ctx context.Context, r domain.RouteConfig) error {
	if r.Strategy != "least_load" && r.Strategy != "highest_balance" && r.Strategy != "balanced" {
		return errors.New("invalid routing strategy")
	}
	if r.TenantRPM < 1 || r.TenantRPM > 1000000 || r.TenantConcurrency < 1 || r.TenantConcurrency > 10000 {
		return errors.New("invalid routing limits")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, "UPDATE settings SET config=? WHERE id='routing'", raw)
	return err
}

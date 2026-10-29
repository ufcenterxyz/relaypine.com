package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/pricing"
)

var (
	ErrInvalidWorkspaceID        = errors.New("invalid Anthropic workspace ID")
	ErrWorkspaceProviderMismatch = errors.New("workspace selection requires an Anthropic endpoint")
	ErrInvalidModelBatch         = errors.New("invalid model import batch")
	// ErrBackupManaged keeps generic endpoint and credential edits away from
	// vendor backups, whose addresses and routing role are fixed presets.
	ErrBackupManaged = errors.New("backup routes are managed in the backup settings")
)

// Backup endpoint and credential IDs are reserved for SaveBackup.
const backupIDPrefix = "backup-"

const MaxModelImportItems = 400

type ModelImportItemResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type ModelImportResult struct {
	CreatedCount int                     `json:"created_count"`
	SkippedCount int                     `json:"skipped_count"`
	Results      []ModelImportItemResult `json:"results"`
}

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
	models, err := listConfig[domain.Model](ctx, s.DB, "models")
	for i := range models {
		models[i].BillingMode = models[i].EffectiveBillingMode()
	}
	return models, err
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
	if e.Role != "" || e.Vendor != "" || strings.HasPrefix(e.ID, backupIDPrefix) {
		return e, ErrBackupManaged
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
	if c.ID == "" {
		c.ID = newID("cred_")
	}
	if strings.HasPrefix(c.ID, backupIDPrefix) {
		return c, ErrBackupManaged
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if c, err = s.saveCredentialTx(ctx, tx, c, false); err != nil {
		return c, err
	}
	return c, tx.Commit()
}

// SaveBackup creates or updates a vendor's backup route, one endpoint and one
// credential, in a single transaction. The endpoint address, routing role,
// name and quota group come from the vendor preset. An empty secret keeps the
// stored key; enabled applies to both rows. management changes the optional
// vendor management key used only to read the account balance: nil keeps it,
// an empty value removes it.
func (s *Store) SaveBackup(ctx context.Context, vendorID string, c domain.Credential, enabled bool, management *string) (domain.Credential, error) {
	vendor, ok := domain.FindBackupVendor(vendorID)
	if !ok {
		return c, errors.New("unsupported backup vendor")
	}
	if management != nil && (len(*management) > 4096 || strings.ContainsAny(*management, "\r\n\x00")) {
		return c, errors.New("invalid management key")
	}
	e := domain.Endpoint{ID: domain.BackupEndpointID(vendor.ID), Name: vendor.Name, BaseURL: vendor.OpenAIBaseURL, AuthMode: "bearer", Enabled: enabled, Models: []string{}, Role: domain.RoleBackup, Vendor: vendor.ID}
	c.ID, c.EndpointID, c.Name, c.QuotaGroup = domain.BackupCredentialID(vendor.ID), e.ID, vendor.Name+" backup", "backup:"+vendor.ID
	c.WorkspaceID, c.Enabled = "", enabled
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	var oldRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT config FROM endpoints WHERE id=? FOR UPDATE", e.ID).Scan(&oldRaw)
	if err == nil {
		var old domain.Endpoint
		if err = json.Unmarshal(oldRaw, &old); err != nil {
			return c, err
		}
		if !old.IsBackup() || old.Vendor != vendor.ID {
			return c, ErrConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	if management != nil && *management == "" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM backup_management_keys WHERE vendor=?", vendor.ID); err != nil {
			return c, err
		}
	} else if management != nil {
		sealed, err := s.encrypt(*management, managementScope+vendor.ID)
		if err != nil {
			return c, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO backup_management_keys(vendor,encrypted_key,updated_at) VALUES(?,?,?) ON DUPLICATE KEY UPDATE encrypted_key=VALUES(encrypted_key),updated_at=VALUES(updated_at)", vendor.ID, sealed, time.Now().UTC()); err != nil {
			return c, err
		}
	}
	var keys int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM backup_management_keys WHERE vendor=?", vendor.ID).Scan(&keys); err != nil {
		return c, err
	}
	e.ManagementKey = keys > 0
	raw, err := json.Marshal(e)
	if err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO endpoints(id,config) VALUES(?,?) ON DUPLICATE KEY UPDATE config=VALUES(config)", e.ID, raw); err != nil {
		return c, dbError(err)
	}
	if c, err = s.saveCredentialTx(ctx, tx, c, true); err != nil {
		return c, err
	}
	return c, tx.Commit()
}

// Management keys are sealed under their vendor, so a row copied to another
// vendor cannot be opened.
const managementScope = "backup-management:"

// BackupManagementKey returns a vendor's saved management key, or an empty
// string when none is saved.
func (s *Store) BackupManagementKey(ctx context.Context, vendor string) (string, error) {
	var sealed []byte
	err := s.DB.QueryRowContext(ctx, "SELECT encrypted_key FROM backup_management_keys WHERE vendor=?", vendor).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return s.decrypt(sealed, managementScope+vendor)
}

// saveCredentialTx validates and upserts a credential. backup states which kind
// of endpoint the credential must belong to.
func (s *Store) saveCredentialTx(ctx context.Context, tx *sql.Tx, c domain.Credential, backup bool) (domain.Credential, error) {
	c.WorkspaceID = strings.TrimSpace(c.WorkspaceID)
	if !domain.ValidAnthropicWorkspaceID(c.WorkspaceID) {
		return c, ErrInvalidWorkspaceID
	}
	if !validID(c.ID) || !validID(c.EndpointID) || !validName(c.Name) || len(c.Secret) > 8192 || c.RPM < 1 || c.RPM > 1000000 || c.TPM < 0 || c.TPM > 1000000000 || c.MaxConcurrency < 1 || c.MaxConcurrency > 10000 || c.BudgetMicros < 0 || c.BudgetMicros > 9000000000000000 || len(c.QuotaGroup) > 128 {
		return c, errors.New("invalid credential configuration")
	}
	var endpointRaw []byte
	err := tx.QueryRowContext(ctx, "SELECT config FROM endpoints WHERE id=? FOR SHARE", c.EndpointID).Scan(&endpointRaw)
	if err != nil {
		return c, dbError(err)
	}
	var endpoint domain.Endpoint
	if err = json.Unmarshal(endpointRaw, &endpoint); err != nil {
		return c, err
	}
	if endpoint.IsBackup() != backup {
		return c, ErrBackupManaged
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
	return c, nil
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
	return s.saveModel(ctx, m, false, false)
}

// CreateModel atomically refuses to overwrite an existing catalog entry. Model
// imports use this path so a concurrent publication cannot replace its prices.
func (s *Store) CreateModel(ctx context.Context, m domain.Model) (domain.Model, error) {
	return s.saveModel(ctx, m, true, false)
}

// SaveManualModel explicitly replaces reference pricing with administrator
// rates, even when the numeric amounts happen to match the reference.
func (s *Store) SaveManualModel(ctx context.Context, m domain.Model) (domain.Model, error) {
	return s.saveModel(ctx, m, false, true)
}

func (s *Store) saveModel(ctx context.Context, m domain.Model, createOnly, manual bool) (domain.Model, error) {
	if m.ID == "" {
		m.ID = newID("model_")
	}
	m.BillingMode = m.EffectiveBillingMode()
	if err := validateModel(m); err != nil {
		return m, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	var version int
	var previous []byte
	// Client edits cannot assert an official reference. Preserve verified metadata
	// only when the authoritative stored price and routing identity are unchanged.
	m.PricingReference = nil
	m.PricingNote = ""
	if !createOnly {
		err = tx.QueryRowContext(ctx, "SELECT config,version FROM models WHERE id=? FOR UPDATE", m.ID).Scan(&previous, &version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return m, err
		}
	}
	if len(previous) > 0 {
		var old domain.Model
		if err = json.Unmarshal(previous, &old); err != nil {
			return m, err
		}
		if !manual && pricing.SameRates(old, m) && old.Provider == m.Provider && old.UpstreamModel == m.UpstreamModel {
			m.PricingReference = old.PricingReference
			m.PricingNote = old.PricingNote
		}
	}
	m.Version = version + 1
	raw, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	query := "INSERT INTO models(id,config,version) VALUES(?,?,?)"
	if !createOnly {
		query += " ON DUPLICATE KEY UPDATE config=VALUES(config),version=VALUES(version)"
	}
	_, err = tx.ExecContext(ctx, query, m.ID, raw, m.Version)
	if err != nil {
		return m, dbError(err)
	}
	return m, tx.Commit()
}

func validateModel(m domain.Model) error {
	if !validID(m.ID) || !validName(m.Name) || !validName(m.UpstreamModel) || (m.Provider != "openai" && m.Provider != "anthropic") || m.MaxOutputTokens < 1 || m.MaxOutputTokens > 1000000 {
		return errors.New("invalid model configuration")
	}
	mode := m.EffectiveBillingMode()
	if mode != domain.BillingMetered && mode != domain.BillingUsageOnly {
		return errors.New("unsupported model billing mode")
	}
	for _, rate := range []int64{m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate, m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate} {
		if rate < 0 || rate > 1000000000000 {
			return errors.New("model rates outside supported range")
		}
		if mode == domain.BillingUsageOnly && rate != 0 {
			return errors.New("usage-only models require all sales and upstream cost rates to be zero")
		}
	}
	if mode == domain.BillingMetered && m.Enabled && (m.InputRate <= 0 || m.OutputRate <= 0 || m.CostInputRate <= 0 || m.CostOutputRate <= 0) {
		return errors.New("enabled metered models require positive sales and upstream input/output rates")
	}
	return nil
}

// CreateModels imports one bounded batch without overwriting any catalog entry.
// All values are checked before opening the transaction. Only a duplicate-key
// error is skipped; every other failure aborts the entire batch. A retry after a
// lost response can therefore only create missing entries, never change prices.
func (s *Store) CreateModels(ctx context.Context, models []domain.Model) (ModelImportResult, error) {
	if len(models) == 0 || len(models) > MaxModelImportItems {
		return ModelImportResult{}, ErrInvalidModelBatch
	}
	type item struct {
		index int
		model domain.Model
		raw   []byte
	}
	items := make([]item, len(models))
	seen := make(map[string]bool, len(models))
	for i, m := range models {
		// Unlike single-item saves, import IDs must be stable across retries.
		if m.ID == "" || seen[m.ID] {
			return ModelImportResult{}, fmt.Errorf("%w: duplicate or missing ID at index %d", ErrInvalidModelBatch, i)
		}
		seen[m.ID] = true
		m.BillingMode = m.EffectiveBillingMode()
		if err := validateModel(m); err != nil {
			return ModelImportResult{}, fmt.Errorf("%w: item %d: %v", ErrInvalidModelBatch, i, err)
		}
		m.Version = 1
		raw, err := json.Marshal(m)
		if err != nil {
			return ModelImportResult{}, err
		}
		items[i] = item{index: i, model: m, raw: raw}
	}
	// Consistent lock order reduces conflicts between overlapping imports; the
	// response is reconstructed in the submitted order.
	sort.Slice(items, func(i, j int) bool { return items[i].model.ID < items[j].model.ID })
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModelImportResult{}, err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, "INSERT INTO models(id,config,version) VALUES(?,?,?)")
	if err != nil {
		return ModelImportResult{}, err
	}
	defer statement.Close()
	result := ModelImportResult{Results: make([]ModelImportItemResult, len(items))}
	for _, item := range items {
		entry := ModelImportItemResult{ID: item.model.ID, Status: "created"}
		if _, err = statement.ExecContext(ctx, item.model.ID, item.raw, 1); err != nil {
			if !errors.Is(dbError(err), ErrConflict) {
				return ModelImportResult{}, err
			}
			entry.Status, entry.Reason = "skipped", "already_exists"
			result.SkippedCount++
		} else {
			result.CreatedCount++
		}
		result.Results[item.index] = entry
	}
	if err = tx.Commit(); err != nil {
		return ModelImportResult{}, err
	}
	return result, nil
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

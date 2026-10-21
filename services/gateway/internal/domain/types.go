package domain

import (
	"regexp"
	"time"
)

type User struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenant_id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	Operator     bool   `json:"operator"`
	PasswordHash string `json:"-"`
}
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
type Project struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
}
type Wallet struct {
	TenantID      string `json:"tenant_id"`
	BalanceMicros int64  `json:"balance_micros"`
	HeldMicros    int64  `json:"held_micros"`
	Currency      string `json:"currency"`
}
type AccessKey struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	Hash      string    `json:"-"`
	Enabled   bool      `json:"enabled"`
	RPM       int       `json:"rpm"`
	Models    []string  `json:"models"`
	CreatedAt time.Time `json:"created_at"`
}
type Endpoint struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	BaseURL  string   `json:"base_url"`
	AuthMode string   `json:"auth_mode"`
	Enabled  bool     `json:"enabled"`
	Models   []string `json:"models"`
}
type Credential struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	EndpointID     string `json:"endpoint_id"`
	WorkspaceID    string `json:"workspace_id"`
	Secret         string `json:"-"`
	MaskedKey      string `json:"masked_key"`
	QuotaGroup     string `json:"quota_group"`
	ProxyID        string `json:"proxy_id"`
	Enabled        bool   `json:"enabled"`
	RPM            int    `json:"rpm"`
	TPM            int    `json:"tpm"`
	MaxConcurrency int    `json:"max_concurrency"`
	BudgetMicros   int64  `json:"budget_micros"`
	SpentMicros    int64  `json:"spent_micros"`
	HeldMicros     int64  `json:"held_micros"`
	Priority       int    `json:"priority"`
}

var anthropicWorkspaceIDPattern = regexp.MustCompile(`^wrkspc_[A-Za-z0-9]+$`)

// ValidAnthropicWorkspaceID validates an optional, already-trimmed workspace ID.
// Workspace selection is explicit; an API-key prefix does not require one.
func ValidAnthropicWorkspaceID(value string) bool {
	return value == "" || len(value) <= 128 && anthropicWorkspaceIDPattern.MatchString(value)
}

type Proxy struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"-"`
	MaskedURL string `json:"masked_url"`
	Enabled   bool   `json:"enabled"`
}
type Model struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Provider           string `json:"provider"`
	UpstreamModel      string `json:"upstream_model"`
	Enabled            bool   `json:"enabled"`
	InputRate          int64  `json:"input_rate"`
	OutputRate         int64  `json:"output_rate"`
	CachedRate         int64  `json:"cached_rate"`
	CacheWriteRate     int64  `json:"cache_write_rate"`
	CostInputRate      int64  `json:"cost_input_rate"`
	CostOutputRate     int64  `json:"cost_output_rate"`
	CostCachedRate     int64  `json:"cost_cached_rate"`
	CostCacheWriteRate int64  `json:"cost_cache_write_rate"`
	MaxOutputTokens    int    `json:"max_output_tokens"`
	Version            int    `json:"version"`
}

// Rates are micro-USD per one million tokens. Amounts are micro-USD.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	Known            bool  `json:"known"`
}
type Request struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	ProjectID      string    `json:"project_id"`
	AccessKeyID    string    `json:"access_key_id"`
	CredentialID   string    `json:"credential_id,omitempty"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	Status         string    `json:"status"`
	Usage          Usage     `json:"usage"`
	ChargeMicros   int64     `json:"charge_micros"`
	CostMicros     int64     `json:"cost_micros,omitempty"`
	ReservedMicros int64     `json:"reserved_micros"`
	LatencyMS      int64     `json:"latency_ms"`
	ErrorCode      string    `json:"error_code,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}
type LedgerEntry struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	Kind         string    `json:"kind"`
	AmountMicros int64     `json:"amount_micros"`
	Reference    string    `json:"reference"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"created_at"`
}
type WaitlistEntry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}
type RouteConfig struct {
	Strategy          string `json:"strategy"`
	TenantRPM         int    `json:"tenant_rpm"`
	TenantConcurrency int    `json:"tenant_concurrency"`
}
type Snapshot struct {
	User        User            `json:"user"`
	Tenant      Tenant          `json:"tenant"`
	Project     Project         `json:"project"`
	Wallet      Wallet          `json:"wallet"`
	Keys        []AccessKey     `json:"keys"`
	Models      []Model         `json:"models"`
	Requests    []Request       `json:"requests"`
	Ledger      []LedgerEntry   `json:"ledger"`
	Waitlist    []WaitlistEntry `json:"waitlist"`
	Endpoints   []Endpoint      `json:"endpoints,omitempty"`
	Credentials []Credential    `json:"credentials,omitempty"`
	Proxies     []Proxy         `json:"proxies,omitempty"`
	Customers   []Tenant        `json:"customers,omitempty"`
	Routing     RouteConfig     `json:"routing"`
}
type Reservation struct {
	Request      Request
	Model        Model
	CredentialID string
	CostReserve  int64
}
type Settlement struct {
	Status       string
	Usage        Usage
	ChargeMicros int64
	CostMicros   int64
	LatencyMS    int64
	ErrorCode    string
}
type Resource struct {
	ID           string
	TenantID     string
	ProjectID    string
	CredentialID string
	EndpointID   string
	Model        string
	InputTokens  int64
}

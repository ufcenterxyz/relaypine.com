package domain

// RoleBackup marks an endpoint that receives traffic only after every primary
// (official or compatible) route for a request is unavailable. A backup is an
// aggregator that speaks both client protocols, so its endpoint has no single
// Provider; its addresses come from a fixed vendor preset.
const RoleBackup = "backup"

// BackupVendor is a supported aggregator. Addresses are constants so the
// console can never point a backup key at an arbitrary host.
type BackupVendor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// OpenAIBaseURL serves Chat Completions and the model catalog.
	OpenAIBaseURL string `json:"openai_base_url"`
	// AnthropicBaseURL serves Messages; /v1/messages is appended to it.
	AnthropicBaseURL  string `json:"anthropic_base_url"`
	AnthropicAuthMode string `json:"anthropic_auth_mode"`
}

var backupVendors = []BackupVendor{
	{ID: "openrouter", Name: "OpenRouter", OpenAIBaseURL: "https://openrouter.ai/api/v1", AnthropicBaseURL: "https://openrouter.ai/api", AnthropicAuthMode: "bearer"},
	{ID: "zenmux", Name: "ZenMux", OpenAIBaseURL: "https://zenmux.ai/api/v1", AnthropicBaseURL: "https://zenmux.ai/api/anthropic", AnthropicAuthMode: "x-api-key"},
}

func BackupVendors() []BackupVendor { return append([]BackupVendor{}, backupVendors...) }

func FindBackupVendor(id string) (BackupVendor, bool) {
	for _, v := range backupVendors {
		if v.ID == id {
			return v, true
		}
	}
	return BackupVendor{}, false
}

// Each vendor has exactly one backup endpoint and one credential, with stable
// IDs so saving a backup is an idempotent upsert.
func BackupEndpointID(vendor string) string   { return "backup-" + vendor }
func BackupCredentialID(vendor string) string { return "backup-" + vendor }

func (e Endpoint) IsBackup() bool { return e.Role == RoleBackup }

// Serves reports whether the endpoint can carry a client protocol.
func (e Endpoint) Serves(protocol string) bool {
	if e.IsBackup() {
		_, ok := FindBackupVendor(e.Vendor)
		return ok && (protocol == "openai" || protocol == "anthropic")
	}
	return e.Provider == protocol
}

// Target returns the base URL and authentication mode for one protocol. A
// backup always uses its vendor preset, never a stored address.
func (e Endpoint) Target(protocol string) (string, string) {
	if e.IsBackup() {
		vendor, ok := FindBackupVendor(e.Vendor)
		if !ok {
			return "", ""
		}
		if protocol == "anthropic" {
			return vendor.AnthropicBaseURL, vendor.AnthropicAuthMode
		}
		return vendor.OpenAIBaseURL, "bearer"
	}
	return e.BaseURL, e.AuthMode
}

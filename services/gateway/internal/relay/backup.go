package relay

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/pricing"
)

// Backups carry only stateless create calls. Responses keep server-side state
// that a different provider cannot continue, and token counting is free on the
// official API, so both stay with primary routes.
func backupPath(provider, path string) bool {
	return provider == "openai" && path == "/v1/chat/completions" || provider == "anthropic" && path == "/v1/messages"
}

var backupClaudeName = regexp.MustCompile(`^claude-([a-z]+)-([0-9]{1,2})(?:-([0-9]{1,2}))?$`)
var backupOpenAIName = regexp.MustCompile(`^(?:gpt-[a-z0-9.-]+|o[0-9]+(?:-[a-z0-9]+)*)$`)

// BackupModel maps an official upstream model ID to a vendor catalog ID, such
// as claude-sonnet-4-5 -> anthropic/claude-sonnet-4.5. Only official names and
// documented dated aliases are translated; dates, suffixes, custom names and
// vendor variants (such as :free) are never guessed.
func BackupModel(vendor, protocol, upstream string) (string, bool) {
	if _, ok := domain.FindBackupVendor(vendor); !ok {
		return "", false
	}
	switch protocol {
	case "anthropic":
		m := backupClaudeName.FindStringSubmatch(pricing.CanonicalModel("anthropic", upstream))
		if m == nil {
			return "", false
		}
		id := "anthropic/claude-" + m[1] + "-" + m[2]
		if m[3] != "" {
			id += "." + m[3]
		}
		return id, true
	case "openai":
		if !backupOpenAIName.MatchString(upstream) {
			return "", false
		}
		return "openai/" + upstream, true
	}
	return "", false
}

type BackupCoverage struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Enabled     bool   `json:"enabled"`
	BackupModel string `json:"backup_model,omitempty"`
	Available   bool   `json:"available"`
}
type BackupCheck struct {
	Vendor string `json:"vendor"`
	// KeyLabel is reported only by vendors that expose key metadata (OpenRouter).
	KeyLabel  string           `json:"key_label,omitempty"`
	Balance   BackupBalance    `json:"balance"`
	Models    []BackupCoverage `json:"models"`
	CheckedAt string           `json:"checked_at"`
}

const backupCatalogMaxBody = 8 << 20
const backupCatalogMaxPages = 10

// CheckBackup verifies a vendor key with a read-only request, reads the account
// balance when a management key is given, and compares the vendor's model
// catalog with RelayPine's models. It never runs inference.
func CheckBackup(ctx context.Context, vendorID, secret, managementKey, proxyURL string, allowPrivate bool, models []domain.Model) (BackupCheck, error) {
	vendor, ok := domain.FindBackupVendor(vendorID)
	secret = strings.TrimSpace(secret)
	if !ok || secret == "" || len(secret) > 4096 || strings.ContainsAny(secret, "\r\n\x00") {
		return BackupCheck{}, discoveryError(400, "invalid_backup", "Choose OpenRouter or ZenMux and enter a valid API key.")
	}
	tr, err := transport(proxyURL, allowPrivate)
	if err != nil {
		return BackupCheck{}, discoveryError(400, "invalid_proxy", "The selected proxy configuration is not usable. Check the proxy settings.")
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return checkBackup(ctx, vendor, secret, strings.TrimSpace(managementKey), client, models)
}

func checkBackup(ctx context.Context, vendor domain.BackupVendor, secret, managementKey string, client *http.Client, models []domain.Model) (BackupCheck, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	now := time.Now().UTC().Format(time.RFC3339)
	result := BackupCheck{Vendor: vendor.ID, Models: []BackupCoverage{}, CheckedAt: now, Balance: BackupBalance{Vendor: vendor.ID, ManagementKey: managementKey != "", CheckedAt: now}}
	if err := verifyBackupKey(ctx, vendor, secret, client, &result); err != nil {
		return result, err
	}
	// A rejected management key is reported with the balance; the inference
	// key and model coverage are still valid.
	if managementKey != "" {
		accountBalance(ctx, vendor, managementKey, client, &result.Balance)
	}
	// OpenRouter serves both protocols from one catalog; read each source once.
	catalogs, sources := map[string]map[string]bool{}, map[string]map[string]bool{}
	for _, protocol := range []string{"openai", "anthropic"} {
		base, auth := catalogSource(vendor, protocol)
		ids, fetched := sources[base]
		if !fetched {
			var err error
			if ids, err = backupCatalog(ctx, vendor, base, auth, secret, client); err != nil {
				return result, err
			}
			sources[base] = ids
		}
		catalogs[protocol] = ids
	}
	for _, m := range models {
		item := BackupCoverage{ID: m.ID, Name: m.Name, Provider: m.Provider, Enabled: m.Enabled}
		if id, ok := BackupModel(vendor.ID, m.Provider, m.UpstreamModel); ok {
			item.BackupModel = id
			item.Available = catalogs[m.Provider][id]
		}
		result.Models = append(result.Models, item)
	}
	sort.SliceStable(result.Models, func(i, j int) bool {
		a, b := result.Models[i], result.Models[j]
		if a.Enabled != b.Enabled {
			return a.Enabled
		}
		return a.ID < b.ID
	})
	return result, nil
}

func backupGet(ctx context.Context, client *http.Client, address, secret, authMode string, target any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, discoveryError(400, "invalid_backup", "The backup address could not be used.")
	}
	request.Header.Set("Accept", "application/json")
	// Model catalogs are large; compressed transfer is about ten times smaller,
	// which matters through a slow egress proxy. The relay transport leaves
	// decoding to us, so the decompressed size is bounded below.
	request.Header.Set("Accept-Encoding", "gzip")
	if authMode == "x-api-key" {
		request.Header.Set("X-Api-Key", secret)
		request.Header.Set("Anthropic-Version", "2023-06-01")
	} else {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, discoveryError(504, "backup_check_timeout", "The backup provider did not respond in time. Check the proxy and try again.")
		}
		return 0, discoveryError(502, "backup_unreachable", "The backup provider could not be reached securely. Check the proxy settings.")
	}
	defer response.Body.Close()
	if target == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return response.StatusCode, nil
	}
	reader := io.Reader(response.Body)
	switch response.Header.Get("Content-Encoding") {
	case "", "identity":
	case "gzip":
		unzipped, err := gzip.NewReader(response.Body)
		if err != nil {
			return response.StatusCode, discoveryError(502, "invalid_backup_response", "The backup provider returned invalid or oversized data. Try again later.")
		}
		defer unzipped.Close()
		reader = unzipped
	default:
		return response.StatusCode, discoveryError(502, "invalid_backup_response", "The backup provider returned an unsupported compressed response.")
	}
	body, err := io.ReadAll(io.LimitReader(reader, backupCatalogMaxBody+1))
	if err != nil || len(body) > backupCatalogMaxBody || json.Unmarshal(body, target) != nil {
		return response.StatusCode, discoveryError(502, "invalid_backup_response", "The backup provider returned invalid or oversized data. Try again later.")
	}
	return response.StatusCode, nil
}

func verifyBackupKey(ctx context.Context, vendor domain.BackupVendor, secret string, client *http.Client, result *BackupCheck) error {
	rejected := discoveryError(400, "backup_key_rejected", vendor.Name+" rejected this API key. Check the key, its permissions and the account balance.")
	if vendor.ID == "openrouter" {
		var key struct {
			Data *struct {
				Label          string   `json:"label"`
				LimitRemaining *float64 `json:"limit_remaining"`
			} `json:"data"`
		}
		status, err := backupGet(ctx, client, vendor.OpenAIBaseURL+"/key", secret, "bearer", &key)
		switch {
		case err != nil:
			return err
		case status == 401 || status == 403:
			return rejected
		case status == 429:
			return discoveryError(429, "backup_rate_limited", vendor.Name+" rate-limited the key check. Wait and try again.")
		case status < 200 || status >= 300 || key.Data == nil:
			return discoveryError(502, "backup_check_failed", vendor.Name+" did not confirm the key. Try again later.")
		}
		if safeMetadata(key.Data.Label, 200) && !strings.Contains(key.Data.Label, secret) {
			result.KeyLabel = key.Data.Label
		}
		if key.Data.LimitRemaining != nil {
			if micros, ok := usdMicros(*key.Data.LimitRemaining); ok {
				result.Balance.KeyLimitRemainingMicros = &micros
			}
		}
		return nil
	}
	// ZenMux has no key-introspection endpoint for inference keys. Its billing
	// lookup accepts them and authenticates before resolving the ID, so an
	// unknown generation separates a rejected key from an accepted one.
	address := vendor.OpenAIBaseURL + "/management/generation?" + url.Values{"id": {"relaypine-key-check"}}.Encode()
	status, err := backupGet(ctx, client, address, secret, "bearer", nil)
	switch {
	case err != nil:
		return err
	case status == 401 || status == 403:
		return rejected
	case status == 429:
		return discoveryError(429, "backup_rate_limited", vendor.Name+" rate-limited the key check. Wait and try again.")
	case status >= 500:
		return discoveryError(502, "backup_check_failed", vendor.Name+" did not confirm the key. Try again later.")
	}
	return nil
}

// backupCatalog returns the IDs a vendor serves on one protocol. OpenRouter has
// a single catalog; ZenMux publishes the Messages-compatible subset separately.
// catalogSource is where a vendor lists the models it serves on a protocol.
// ZenMux publishes its Messages-compatible subset separately.
func catalogSource(vendor domain.BackupVendor, protocol string) (string, string) {
	if protocol == "anthropic" && vendor.ID == "zenmux" {
		return vendor.AnthropicBaseURL + "/v1", vendor.AnthropicAuthMode
	}
	return vendor.OpenAIBaseURL, "bearer"
}

func backupCatalog(ctx context.Context, vendor domain.BackupVendor, base, auth, secret string, client *http.Client) (map[string]bool, error) {
	ids, cursors := map[string]bool{}, map[string]bool{}
	query := url.Values{}
	for page := 0; page < backupCatalogMaxPages; page++ {
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		address := base + "/models"
		if len(query) > 0 {
			address += "?" + query.Encode()
		}
		status, err := backupGet(ctx, client, address, secret, auth, &list)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 || list.Data == nil {
			return nil, discoveryError(502, "backup_catalog_unavailable", "The "+vendor.Name+" model list could not be read. Try again later.")
		}
		for _, model := range list.Data {
			if safeMetadata(model.ID, 200) {
				ids[model.ID] = true
			}
		}
		if !list.HasMore {
			return ids, nil
		}
		if !safeMetadata(list.LastID, 200) || cursors[list.LastID] {
			return nil, discoveryError(502, "backup_catalog_unavailable", "The "+vendor.Name+" model list could not be read completely. Try again later.")
		}
		cursors[list.LastID] = true
		query.Set("after_id", list.LastID)
	}
	return nil, discoveryError(502, "backup_catalog_unavailable", "The "+vendor.Name+" model list exceeds the supported size.")
}

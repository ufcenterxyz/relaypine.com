package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"relaypine.com/gateway/internal/domain"
)

const discoveryMaxBody = 1 << 20
const discoveryMaxPages = 4
const discoveryMaxItems = 400

type DiscoveredModel struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}
type DiscoveredWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type DiscoveryDefaults struct {
	Name           string `json:"name"`
	QuotaGroup     string `json:"quota_group"`
	RPM            int    `json:"rpm"`
	TPM            int    `json:"tpm"`
	MaxConcurrency int    `json:"max_concurrency"`
	BudgetMicros   int64  `json:"budget_micros"`
	Priority       int    `json:"priority"`
	Enabled        bool   `json:"enabled"`
}
type DiscoveryResult struct {
	Status         string                `json:"status"`
	Models         []DiscoveredModel     `json:"models"`
	Workspaces     []DiscoveredWorkspace `json:"workspaces"`
	WorkspaceID    string                `json:"workspace_id"`
	OrganizationID string                `json:"organization_id"`
	Defaults       DiscoveryDefaults     `json:"defaults"`
	LimitsSource   string                `json:"limits_source"`
	Warnings       []string              `json:"warnings"`
	CheckedAt      string                `json:"checked_at"`
}

// DiscoveryError contains only application-authored messages, never a provider
// response body, URL, API key, or proxy credential.
type DiscoveryError struct {
	Status  int
	Code    string
	Message string
}

func (e *DiscoveryError) Error() string { return e.Message }
func discoveryError(status int, code, message string) error {
	return &DiscoveryError{Status: status, Code: code, Message: message}
}

// DiscoverCredential makes only read-only metadata requests. It neither saves
// configuration nor issues paid inference, and cannot determine account balance.
func DiscoverCredential(ctx context.Context, endpoint domain.Endpoint, credential domain.Credential, proxyURL string, allowPrivate bool) (DiscoveryResult, error) {
	tr, err := transport(proxyURL, allowPrivate)
	if err != nil {
		return DiscoveryResult{}, discoveryError(400, "invalid_proxy", "The saved proxy configuration is not usable. Check the proxy settings.")
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return discoverCredential(ctx, endpoint, credential, allowPrivate, client)
}

type discoveryClient struct {
	client       *http.Client
	endpoint     domain.Endpoint
	credential   domain.Credential
	allowPrivate bool
}

func discoverCredential(ctx context.Context, endpoint domain.Endpoint, credential domain.Credential, allowPrivate bool, client *http.Client) (DiscoveryResult, error) {
	defaults := domain.DefaultCredential(endpoint)
	result := DiscoveryResult{
		Status: "verified", Models: []DiscoveredModel{}, Workspaces: []DiscoveredWorkspace{},
		Defaults:     DiscoveryDefaults{Name: defaults.Name, QuotaGroup: defaults.QuotaGroup, RPM: defaults.RPM, TPM: defaults.TPM, MaxConcurrency: defaults.MaxConcurrency, BudgetMicros: 0, Priority: 0, Enabled: true},
		LimitsSource: "default", Warnings: []string{}, CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
	credential.Secret, credential.WorkspaceID = strings.TrimSpace(credential.Secret), strings.TrimSpace(credential.WorkspaceID)
	if (endpoint.Provider != "openai" && endpoint.Provider != "anthropic") || credential.Secret == "" || len(credential.Secret) > 4096 || strings.ContainsAny(credential.Secret, "\r\n\x00") || !domain.ValidAnthropicWorkspaceID(credential.WorkspaceID) || endpoint.Provider != "anthropic" && credential.WorkspaceID != "" {
		return result, discoveryError(400, "invalid_discovery", "Choose a valid endpoint, API key, and optional Anthropic workspace.")
	}
	if endpoint.AuthMode == "" {
		endpoint.AuthMode = "bearer"
		if endpoint.Provider == "anthropic" {
			endpoint.AuthMode = "x-api-key"
		}
	}
	if endpoint.AuthMode != "bearer" && endpoint.AuthMode != "x-api-key" {
		return result, discoveryError(400, "invalid_auth", "Choose Bearer or x-api-key authentication for the endpoint.")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	d := discoveryClient{client: client, endpoint: endpoint, credential: credential, allowPrivate: allowPrivate}
	models, headers, modelErr := d.models(ctx)
	result.WorkspaceID = credential.WorkspaceID
	if modelErr == nil {
		result.Models = models
		if endpoint.Provider == "anthropic" {
			if workspace := strings.TrimSpace(headers.Get("Anthropic-Workspace-Id")); result.WorkspaceID == "" && workspace != "" && domain.ValidAnthropicWorkspaceID(workspace) {
				result.WorkspaceID = workspace
				d.credential.WorkspaceID = workspace
			}
			result.OrganizationID = validOrganizationID(headers.Get("Anthropic-Organization-Id"))
		}
	}
	if officialAnthropic(endpoint) {
		// Admin metadata is optional. Scoped inference keys may correctly be
		// denied here while their model discovery remains valid.
		var organization struct {
			ID string `json:"id"`
		}
		organizationAccess := false
		if _, err := d.get(ctx, "/v1/organizations/me", nil, &organization); err == nil {
			if id := validOrganizationID(organization.ID); id != "" {
				result.OrganizationID = id
				organizationAccess = true
			}
		}
		if result.WorkspaceID == "" && (organizationAccess || modelErr != nil && workspaceMayResolve(modelErr)) {
			workspaces, err := d.workspaces(ctx)
			if err != nil && organizationAccess {
				return result, discoveryError(400, "workspace_discovery_unavailable", "This organization key needs a workspace selection, but the complete workspace list could not be read. Enter its workspace ID in Advanced settings and try again.")
			}
			if err == nil {
				result.Workspaces = workspaces
				if len(workspaces) != 1 {
					result.Status = "workspace_required"
					result.Warnings = append(result.Warnings, "Choose a workspace to verify model access. No configuration has been saved.")
					setDiscoveryGroup(&result)
					return result, nil
				}
				if len(workspaces) == 1 {
					result.WorkspaceID = workspaces[0].ID
					d.credential.WorkspaceID = workspaces[0].ID
					models, headers, modelErr = d.models(ctx)
					if modelErr == nil {
						result.Models = models
						if id := validOrganizationID(headers.Get("Anthropic-Organization-Id")); id != "" {
							result.OrganizationID = id
						}
					}
				}
			}
		}
		if modelErr == nil {
			orgLimits, orgErr := d.rateLimits(ctx, "")
			var workspaceLimits []discoveredRateGroup
			var workspaceErr error
			if result.WorkspaceID != "" {
				workspaceLimits, workspaceErr = d.rateLimits(ctx, result.WorkspaceID)
			}
			if orgErr == nil || result.WorkspaceID != "" && workspaceErr == nil {
				if err := applyDiscoveredLimits(&result, orgLimits, workspaceLimits); err != nil {
					return result, err
				}
				if result.WorkspaceID != "" && workspaceErr != nil {
					result.LimitsSource = "default"
					result.Defaults.RPM = min(result.Defaults.RPM, defaults.RPM)
					result.Defaults.TPM = min(result.Defaults.TPM, defaults.TPM)
				}
			}
		}
	}
	if modelErr != nil {
		return result, modelErr
	}
	if len(result.Models) == 0 {
		return result, discoveryError(400, "no_models_available", "The key authenticated but returned no models. Check upstream permissions, or use Advanced settings to save manually.")
	}
	if result.LimitsSource == "default" {
		result.Warnings = append(result.Warnings, "Some inference rate limits could not be read. Missing values use local defaults.")
	}
	setDiscoveryGroup(&result)
	return result, nil
}

func officialAnthropic(e domain.Endpoint) bool {
	u, err := url.Parse(e.BaseURL)
	return err == nil && e.Provider == "anthropic" && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "api.anthropic.com") && (u.Port() == "" || u.Port() == "443") && (u.Path == "" || u.Path == "/" || u.Path == "/v1" || u.Path == "/v1/")
}

var organizationIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validOrganizationID(value string) string {
	if organizationIDPattern.MatchString(value) {
		return strings.ToLower(value)
	}
	return ""
}
func setDiscoveryGroup(result *DiscoveryResult) {
	if result.OrganizationID != "" {
		sum := sha256.Sum256([]byte(result.OrganizationID))
		result.Defaults.QuotaGroup = "auto:anthropic:org:" + hex.EncodeToString(sum[:12])
	}
}
func workspaceMayResolve(err error) bool {
	var e *DiscoveryError
	return errors.As(err, &e) && (e.Code == "upstream_key_rejected" || e.Code == "upstream_request_rejected")
}

func (d *discoveryClient) get(ctx context.Context, path string, query url.Values, target any) (http.Header, error) {
	address, err := upstreamURL(d.endpoint.BaseURL, path, d.allowPrivate)
	if err != nil {
		return nil, discoveryError(400, "invalid_endpoint", "Use a valid HTTPS API base URL without credentials, query parameters, or a fragment.")
	}
	u, _ := url.Parse(address)
	u.RawQuery = query.Encode()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, discoveryError(400, "invalid_endpoint", "The endpoint URL could not be used.")
	}
	r.Header.Set("Accept", "application/json")
	if d.endpoint.AuthMode == "bearer" {
		r.Header.Set("Authorization", "Bearer "+d.credential.Secret)
	} else {
		r.Header.Set("X-Api-Key", d.credential.Secret)
	}
	if d.endpoint.Provider == "anthropic" {
		r.Header.Set("Anthropic-Version", "2023-06-01")
		if path == "/v1/models" && d.credential.WorkspaceID != "" {
			r.Header.Set("Anthropic-Workspace-Id", d.credential.WorkspaceID)
		}
	}
	response, err := d.client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, discoveryError(504, "discovery_timeout", "Upstream discovery timed out. Check the endpoint or proxy and try again.")
		}
		return nil, discoveryError(502, "upstream_unreachable", "The upstream could not be reached securely. Check the endpoint and proxy settings.")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		switch response.StatusCode {
		case 401, 403:
			return nil, discoveryError(400, "upstream_key_rejected", "The upstream rejected model access. Check the API key, permissions, and Anthropic workspace selection.")
		case 400, 422:
			return nil, discoveryError(400, "upstream_request_rejected", "The upstream could not verify this configuration. Check the API base URL and workspace, or use Advanced settings to save manually.")
		case 404, 405, 501:
			return nil, discoveryError(400, "discovery_not_supported", "This endpoint does not support model discovery. Check its API base URL, or use Advanced settings to save manually.")
		case 429:
			return nil, discoveryError(429, "upstream_rate_limited", "The upstream rate-limited discovery. Wait and try again.")
		default:
			return nil, discoveryError(502, "upstream_discovery_failed", "The upstream did not complete discovery. Redirects are not followed; check the endpoint or try again later.")
		}
	}
	if response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
		return nil, discoveryError(502, "invalid_discovery_response", "The upstream returned an unsupported compressed discovery response.")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, discoveryMaxBody+1))
	if err != nil || len(body) > discoveryMaxBody || json.Unmarshal(body, target) != nil {
		return nil, discoveryError(502, "invalid_discovery_response", "The upstream returned invalid or oversized discovery data. Use Advanced settings if automatic discovery is unsupported.")
	}
	return response.Header.Clone(), nil
}

func safeMetadata(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func pageError() error {
	return discoveryError(502, "discovery_too_large", "The upstream metadata exceeds automatic discovery limits. Use Advanced settings to configure this key manually.")
}

func (d *discoveryClient) models(ctx context.Context) ([]DiscoveredModel, http.Header, error) {
	result := []DiscoveredModel{}
	seen, cursors := map[string]bool{}, map[string]bool{}
	query := url.Values{}
	if d.endpoint.Provider == "anthropic" {
		query.Set("limit", "100")
	}
	var headers http.Header
	for page := 0; page < discoveryMaxPages; page++ {
		var response struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
				MaxTokens   int    `json:"max_tokens"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		var err error
		headers, err = d.get(ctx, "/v1/models", query, &response)
		if err != nil {
			return nil, nil, err
		}
		if response.Data == nil {
			return nil, nil, discoveryError(502, "invalid_discovery_response", "The upstream did not return a model list. Check its compatibility or use Advanced settings.")
		}
		for _, model := range response.Data {
			if !safeMetadata(model.ID, 200) || strings.TrimSpace(model.ID) != model.ID || strings.Contains(model.ID, d.credential.Secret) || strings.Contains(model.DisplayName, d.credential.Secret) {
				return nil, nil, discoveryError(502, "invalid_discovery_response", "The upstream returned an invalid model identifier.")
			}
			if seen[model.ID] {
				continue
			}
			seen[model.ID] = true
			name := model.DisplayName
			if !safeMetadata(name, 200) {
				name = model.ID
			}
			output := 0
			if model.MaxTokens > 0 && model.MaxTokens <= 1000000 {
				output = model.MaxTokens
			}
			result = append(result, DiscoveredModel{ID: model.ID, Name: name, MaxOutputTokens: output})
			if len(result) > discoveryMaxItems {
				return nil, nil, pageError()
			}
		}
		if !response.HasMore {
			return result, headers, nil
		}
		if !safeMetadata(response.LastID, 200) || cursors[response.LastID] {
			return nil, nil, pageError()
		}
		cursors[response.LastID] = true
		query.Set("after_id", response.LastID)
	}
	return nil, nil, pageError()
}

func (d *discoveryClient) workspaces(ctx context.Context) ([]DiscoveredWorkspace, error) {
	result := []DiscoveredWorkspace{}
	seen, cursors := map[string]bool{}, map[string]bool{}
	query := url.Values{"limit": {"100"}, "include_default": {"true"}}
	for page := 0; page < discoveryMaxPages; page++ {
		var response struct {
			Data []struct {
				ID         string  `json:"id"`
				Name       string  `json:"name"`
				ArchivedAt *string `json:"archived_at"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if _, err := d.get(ctx, "/v1/organizations/workspaces", query, &response); err != nil {
			return nil, err
		}
		if response.Data == nil {
			return nil, pageError()
		}
		for _, workspace := range response.Data {
			if workspace.ArchivedAt != nil || seen[workspace.ID] {
				continue
			}
			if workspace.ID == "" || !domain.ValidAnthropicWorkspaceID(workspace.ID) {
				return nil, discoveryError(502, "invalid_discovery_response", "The upstream returned an invalid workspace identifier.")
			}
			name := workspace.Name
			if strings.Contains(name, d.credential.Secret) {
				return nil, discoveryError(502, "invalid_discovery_response", "The upstream returned invalid workspace metadata.")
			}
			if !safeMetadata(name, 200) {
				name = workspace.ID
			}
			result = append(result, DiscoveredWorkspace{ID: workspace.ID, Name: name})
			seen[workspace.ID] = true
			if len(result) > discoveryMaxItems {
				return nil, pageError()
			}
		}
		if !response.HasMore {
			return result, nil
		}
		if !safeMetadata(response.LastID, 200) || cursors[response.LastID] {
			return nil, pageError()
		}
		cursors[response.LastID] = true
		query.Set("after_id", response.LastID)
	}
	return nil, pageError()
}

type discoveredRateGroup struct {
	Group struct {
		Type string `json:"type"`
	} `json:"group"`
	Models []string `json:"models"`
	Limits []struct {
		Type     string   `json:"type"`
		Value    *float64 `json:"value"`
		OrgLimit *float64 `json:"org_limit"`
		Source   struct {
			Type string `json:"type"`
		} `json:"source"`
	} `json:"limits"`
}

func (d *discoveryClient) rateLimits(ctx context.Context, workspace string) ([]discoveredRateGroup, error) {
	path := "/v1/organizations/rate_limits"
	query := url.Values{"group_type": {"model_group"}}
	if workspace != "" {
		path = "/v1/organizations/workspaces/" + workspace + "/rate_limits"
		query.Set("include_inherited", "true")
	}
	result := []discoveredRateGroup{}
	seen := map[string]bool{}
	for page := 0; page < discoveryMaxPages; page++ {
		var response struct {
			Data     []discoveredRateGroup `json:"data"`
			NextPage *string               `json:"next_page"`
		}
		if _, err := d.get(ctx, path, query, &response); err != nil {
			return nil, err
		}
		if response.Data == nil {
			return nil, pageError()
		}
		result = append(result, response.Data...)
		if len(result) > discoveryMaxItems {
			return nil, pageError()
		}
		if response.NextPage == nil || *response.NextPage == "" {
			return result, nil
		}
		if !safeMetadata(*response.NextPage, 256) || seen[*response.NextPage] {
			return nil, pageError()
		}
		seen[*response.NextPage] = true
		query.Set("page", *response.NextPage)
	}
	return nil, pageError()
}

func applyDiscoveredLimits(result *DiscoveryResult, organization, workspace []discoveredRateGroup) error {
	// A local combined token bucket must not exceed either provider input or
	// output allowance. Metadata/catalog request headers are never consulted.
	types := map[string]bool{"requests_per_minute": true, "input_tokens_per_minute": true, "output_tokens_per_minute": true}
	perModel := map[string]map[string]int{}
	organizationCoverage, workspaceCoverage := map[string]map[string]bool{}, map[string]map[string]bool{}
	for _, model := range result.Models {
		perModel[model.ID] = map[string]int{}
		organizationCoverage[model.ID] = map[string]bool{}
		workspaceCoverage[model.ID] = map[string]bool{}
	}
	groups := append(append([]discoveredRateGroup{}, organization...), workspace...)
	for index, group := range groups {
		if group.Group.Type != "model_group" {
			continue
		}
		for _, model := range group.Models {
			limits, applies := perModel[model]
			if !applies {
				continue
			}
			for _, limit := range group.Limits {
				if !types[limit.Type] {
					continue
				}
				positive := func(value *float64) bool {
					return value != nil && *value > 0 && *value <= 1e15 && math.Trunc(*value) == *value
				}
				if index < len(organization) {
					if positive(limit.Value) {
						organizationCoverage[model][limit.Type] = true
					}
				} else {
					if positive(limit.Value) {
						workspaceCoverage[model][limit.Type] = true
					}
					if positive(limit.OrgLimit) || limit.Source.Type == "organization" && positive(limit.Value) {
						organizationCoverage[model][limit.Type] = true
					}
				}
				for _, value := range []*float64{limit.Value, limit.OrgLimit} {
					if value == nil {
						continue
					}
					if *value == 0 {
						result.Defaults.Enabled = false
						result.LimitsSource = "default"
						result.Warnings = append(result.Warnings, "The provider reports zero Messages capacity for an available model. This key is disabled by default; review provider limits before enabling it. Suggested positive values are local placeholders, not provider capacity.")
						return nil
					}
					if *value < 0 || *value > 1e15 || math.Trunc(*value) != *value {
						continue
					}
					cap := 100000000
					if limit.Type == "requests_per_minute" {
						cap = 100000
					}
					n := int(math.Min(*value, float64(cap)))
					if old := limits[limit.Type]; old == 0 || n < old {
						limits[limit.Type] = n
					}
				}
			}
		}
	}
	complete := len(perModel) > 0
	rpm, tpm := 100000, 100000000
	for model, limits := range perModel {
		for kind := range types {
			n := limits[kind]
			known := organizationCoverage[model][kind] && (result.WorkspaceID == "" || workspaceCoverage[model][kind])
			if n == 0 || !known {
				complete = false
				fallback := result.Defaults.TPM
				if kind == "requests_per_minute" {
					fallback = result.Defaults.RPM
				}
				if n == 0 || fallback < n {
					n = fallback
				}
			}
			if kind == "requests_per_minute" {
				rpm = min(rpm, n)
			} else {
				tpm = min(tpm, n)
			}
		}
	}
	if len(perModel) > 0 {
		result.Defaults.RPM, result.Defaults.TPM = rpm, tpm
	}
	if complete {
		result.LimitsSource = "provider"
	}
	return nil
}

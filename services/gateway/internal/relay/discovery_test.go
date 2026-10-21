package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

type discoveryRoundTrip func(*http.Request) (*http.Response, error)

func (f discoveryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func discoveryResponse(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = http.Header{}
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: headers}
}

const discoverySecret = "sk-discovery-test-private-secret"
const discoveryOrgID = "01234567-89ab-cdef-0123-456789abcdef"
const discoveryModels = `{"data":[{"id":"claude-test","display_name":"Claude Test","max_tokens":16384}],"has_more":false}`

func discoveryEndpoint(provider string) domain.Endpoint {
	return domain.Endpoint{ID: provider + "-test", Name: "Test upstream", Provider: provider, BaseURL: "https://api." + provider + ".com", Enabled: true}
}
func discoveryClientFor(t *testing.T, handler func(*http.Request) *http.Response) *http.Client {
	t.Helper()
	return &http.Client{Transport: discoveryRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.Body != nil {
			t.Fatal("discovery issued a non-GET or nonempty request")
		}
		return handler(r), nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestDiscoveryCustomHostsUseOnlyCatalogAndConservativeDefaults(t *testing.T) {
	for _, tc := range []struct{ provider, auth string }{{"openai", "bearer"}, {"openai", "x-api-key"}, {"anthropic", "bearer"}, {"anthropic", "x-api-key"}} {
		t.Run(tc.provider+"_"+tc.auth, func(t *testing.T) {
			endpoint := discoveryEndpoint(tc.provider)
			endpoint.BaseURL, endpoint.AuthMode = "https://compatible.example/v1", tc.auth
			credential := domain.Credential{Secret: discoverySecret}
			if tc.provider == "anthropic" {
				credential.WorkspaceID = "wrkspc_Selected"
			}
			var calls int
			client := discoveryClientFor(t, func(r *http.Request) *http.Response {
				calls++
				if r.URL.Path != "/v1/models" || r.Header.Get("Anthropic-Beta") != "" {
					t.Fatal("custom host received an admin or beta request")
				}
				if tc.auth == "bearer" {
					if r.Header.Get("Authorization") != "Bearer "+discoverySecret || r.Header.Get("X-Api-Key") != "" {
						t.Fatal("Bearer authentication was not isolated")
					}
				} else if r.Header.Get("X-Api-Key") != discoverySecret || r.Header.Get("Authorization") != "" {
					t.Fatal("x-api-key authentication was not isolated")
				}
				if tc.provider == "anthropic" {
					if r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("Anthropic-Workspace-Id") != credential.WorkspaceID {
						t.Fatal("Anthropic version or selected workspace missing")
					}
				} else if r.Header.Get("Anthropic-Version") != "" || r.Header.Get("Anthropic-Workspace-Id") != "" {
					t.Fatal("Anthropic metadata leaked into OpenAI")
				}
				return discoveryResponse(200, discoveryModels, http.Header{"Anthropic-Ratelimit-Requests-Limit": {"999999"}, "Anthropic-Ratelimit-Tokens-Limit": {"99999999"}})
			})
			result, err := discoverCredential(context.Background(), endpoint, credential, false, client)
			if err != nil || result.Status != "verified" || calls != 1 || result.Defaults.RPM != 60 || result.Defaults.TPM != 100000 || result.LimitsSource != "default" {
				t.Fatalf("unexpected discovery result: %+v err=%v calls=%d", result, err, calls)
			}
			if len(result.Models) != 1 || result.Models[0].MaxOutputTokens != 16384 || result.Defaults.BudgetMicros != 0 || result.Defaults.QuotaGroup != "auto:"+endpoint.ID {
				t.Fatal("model metadata or local defaults incorrect")
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), discoverySecret) {
				t.Fatal("discovery exposed an API key")
			}
		})
	}
}

func TestDiscoveryOfficialAnthropicScopedKeyDoesNotRequireAdminPermission(t *testing.T) {
	client := discoveryClientFor(t, func(r *http.Request) *http.Response {
		if r.URL.Path == "/v1/models" {
			return discoveryResponse(200, discoveryModels, http.Header{"Anthropic-Workspace-Id": {"wrkspc_Scoped"}, "Anthropic-Organization-Id": {discoveryOrgID}})
		}
		if r.Header.Get("Anthropic-Workspace-Id") != "" {
			t.Fatal("admin metadata received inference workspace header")
		}
		return discoveryResponse(403, discoverySecret, nil)
	})
	result, err := discoverCredential(context.Background(), discoveryEndpoint("anthropic"), domain.Credential{Secret: discoverySecret}, false, client)
	if err != nil || result.Status != "verified" || result.WorkspaceID != "wrkspc_Scoped" || result.OrganizationID != discoveryOrgID || result.LimitsSource != "default" {
		t.Fatalf("scoped key was rejected or misclassified: %+v %v", result, err)
	}
	if !strings.HasPrefix(result.Defaults.QuotaGroup, "auto:anthropic:org:") {
		t.Fatal("organization-wide quota group not suggested")
	}
}

func TestDiscoveryWorkspaceSelectionRequiresCompleteList(t *testing.T) {
	for _, tc := range []struct {
		name           string
		initialSuccess bool
		multiple       bool
		truncated      bool
	}{
		{name: "one workspace after auth rejection"},
		{name: "one workspace after unscoped catalog success", initialSuccess: true},
		{name: "multiple workspaces", multiple: true},
		{name: "unscoped catalog success still requires selection", initialSuccess: true, multiple: true},
		{name: "truncated workspace list never auto selects", initialSuccess: true, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var selectedCalls, workspacePages int
			client := discoveryClientFor(t, func(r *http.Request) *http.Response {
				switch r.URL.Path {
				case "/v1/models":
					if r.Header.Get("Anthropic-Workspace-Id") == "wrkspc_Default" {
						selectedCalls++
						return discoveryResponse(200, discoveryModels, nil)
					}
					if tc.initialSuccess {
						return discoveryResponse(200, discoveryModels, nil)
					}
					return discoveryResponse(400, `{"error":"workspace required"}`, nil)
				case "/v1/organizations/me":
					return discoveryResponse(200, `{"id":"`+discoveryOrgID+`"}`, nil)
				case "/v1/organizations/workspaces":
					workspacePages++
					if r.URL.Query().Get("include_default") != "true" {
						t.Fatal("default workspace excluded from discovery")
					}
					if tc.truncated {
						return discoveryResponse(200, `{"data":[{"id":"wrkspc_Default","name":"Default"}],"has_more":true,"last_id":"same"}`, nil)
					}
					if r.URL.Query().Get("after_id") == "first" {
						return discoveryResponse(200, `{"data":[{"id":"wrkspc_Other","name":"Other"}],"has_more":false}`, nil)
					}
					if tc.multiple {
						return discoveryResponse(200, `{"data":[{"id":"wrkspc_Default","name":"Default"}],"has_more":true,"last_id":"first"}`, nil)
					}
					return discoveryResponse(200, `{"data":[{"id":"wrkspc_Default","name":"Default"},{"id":"wrkspc_Old","name":"Old","archived_at":"2020-01-01"}],"has_more":false}`, nil)
				default:
					return discoveryResponse(403, "", nil)
				}
			})
			result, err := discoverCredential(context.Background(), discoveryEndpoint("anthropic"), domain.Credential{Secret: discoverySecret}, false, client)
			if tc.truncated {
				if err == nil || selectedCalls != 0 {
					t.Fatal("incomplete workspace enumeration was treated as a unique workspace")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.multiple {
				if result.Status != "workspace_required" || len(result.Workspaces) != 2 || selectedCalls != 0 || workspacePages != 2 {
					t.Fatalf("multiple workspace selection was not explicit: %+v", result)
				}
			} else if result.Status != "verified" || result.WorkspaceID != "wrkspc_Default" || selectedCalls != 1 {
				t.Fatalf("single complete workspace was not verified: %+v", result)
			}
		})
	}
}

func rateGroupJSON(models string, rpm, input, output string) string {
	return `{"group":{"type":"model_group"},"models":` + models + `,"limits":[{"type":"requests_per_minute","value":` + rpm + `},{"type":"input_tokens_per_minute","value":` + input + `},{"type":"output_tokens_per_minute","value":` + output + `}]}`
}

func TestDiscoveryUsesDocumentedMessagesLimitsAcrossOrganizationAndWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name, orgRPM, workspaceRPM, workspaceOutput string
		workspaceDenied, partial, zero              bool
		wantRPM, wantTPM                            int
		wantSource                                  string
	}{
		{name: "minimum across both scopes", orgRPM: "400", workspaceRPM: "250", workspaceOutput: "50000", wantRPM: 250, wantTPM: 50000, wantSource: "provider"},
		{name: "workspace unavailable never claims full provider scope", orgRPM: "400", workspaceDenied: true, wantRPM: 60, wantTPM: 100000, wantSource: "default"},
		{name: "partial limiter coverage uses conservative fallback", orgRPM: "400", workspaceRPM: "250", workspaceOutput: "null", partial: true, wantRPM: 250, wantTPM: 100000, wantSource: "default"},
		{name: "explicit zero disables rather than treats unlimited", orgRPM: "0", workspaceRPM: "250", workspaceOutput: "50000", zero: true, wantRPM: 60, wantTPM: 100000, wantSource: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := discoveryClientFor(t, func(r *http.Request) *http.Response {
				if r.URL.Path == "/v1/models" {
					return discoveryResponse(200, discoveryModels, nil)
				}
				if r.Header.Get("Anthropic-Workspace-Id") != "" {
					t.Fatal("admin request was scoped through an inference header")
				}
				if r.URL.Path == "/v1/organizations/me" {
					return discoveryResponse(200, `{"id":"`+discoveryOrgID+`"}`, nil)
				}
				if r.URL.Query().Get("group_type") != "model_group" {
					t.Fatal("non-Messages limits were requested")
				}
				if r.URL.Path == "/v1/organizations/rate_limits" {
					output := "800000"
					if tc.partial {
						output = "null"
					}
					return discoveryResponse(200, `{"data":[`+rateGroupJSON(`["claude-test"]`, tc.orgRPM, "1000000", output)+`],"next_page":null}`, nil)
				}
				if r.URL.Path == "/v1/organizations/workspaces/wrkspc_Selected/rate_limits" {
					if tc.workspaceDenied {
						return discoveryResponse(403, "denied", nil)
					}
					if r.URL.Query().Get("include_inherited") != "true" {
						t.Fatal("workspace inheritance omitted")
					}
					return discoveryResponse(200, `{"data":[`+rateGroupJSON(`["claude-test"]`, tc.workspaceRPM, "200000", tc.workspaceOutput)+`],"next_page":null}`, nil)
				}
				t.Fatalf("unexpected metadata path %s", r.URL.Path)
				return nil
			})
			result, err := discoverCredential(context.Background(), discoveryEndpoint("anthropic"), domain.Credential{Secret: discoverySecret, WorkspaceID: "wrkspc_Selected"}, false, client)
			if err != nil || result.Defaults.RPM != tc.wantRPM || result.Defaults.TPM != tc.wantTPM || result.LimitsSource != tc.wantSource || result.Defaults.Enabled == tc.zero {
				t.Fatalf("capacity result=%+v err=%v", result, err)
			}
		})
	}
}

func TestDiscoveryLimitsIgnoreUnrelatedProductsAndRespectOrgLimitAndUnknownModels(t *testing.T) {
	var groups []discoveredRateGroup
	if err := json.Unmarshal([]byte(`[`+rateGroupJSON(`["claude-test"]`, "1000", "2000000", "500000")+`,{"group":{"type":"token_count"},"models":["claude-test"],"limits":[{"type":"requests_per_minute","value":0}]},{"group":{"type":"model_group"},"models":["unrelated"],"limits":[{"type":"requests_per_minute","value":0}]},{"group":{"type":"model_group"},"models":["claude-test"],"limits":[{"type":"requests_per_minute","value":500,"org_limit":90}]}]`), &groups); err != nil {
		t.Fatal(err)
	}
	result := DiscoveryResult{Models: []DiscoveredModel{{ID: "claude-test"}, {ID: "unknown-model"}}, Defaults: DiscoveryDefaults{RPM: 60, TPM: 100000, Enabled: true}, LimitsSource: "default"}
	if err := applyDiscoveredLimits(&result, groups, nil); err != nil || result.Defaults.RPM != 60 || result.Defaults.TPM != 100000 || result.LimitsSource != "default" || !result.Defaults.Enabled {
		t.Fatalf("unknown/unrelated model handling failed: %+v %v", result, err)
	}
	result.Models = result.Models[:1]
	if err := applyDiscoveredLimits(&result, groups, nil); err != nil || result.Defaults.RPM != 90 || result.Defaults.TPM != 500000 || result.LimitsSource != "provider" {
		t.Fatalf("organization limit not respected: %+v %v", result, err)
	}
}

func TestDiscoveryLimitCompletenessIsCheckedPerScope(t *testing.T) {
	decode := func(raw string) []discoveredRateGroup {
		t.Helper()
		var groups []discoveredRateGroup
		if err := json.Unmarshal([]byte(raw), &groups); err != nil {
			t.Fatal(err)
		}
		return groups
	}
	organization := decode(`[` + rateGroupJSON(`["claude-test"]`, "4000", "10000000", "800000") + `]`)
	for _, tc := range []struct {
		name, workspace string
		org             []discoveredRateGroup
		wantRPM         int
		wantTPM         int
		wantSource      string
	}{
		{name: "successful partial workspace cannot borrow complete organization fields", org: organization, workspace: `[{"group":{"type":"model_group"},"models":["claude-test"],"limits":[{"type":"requests_per_minute","value":250}]}]`, wantRPM: 250, wantTPM: 100000, wantSource: "default"},
		{name: "complete workspace without known parent cannot claim provider", workspace: `[` + rateGroupJSON(`["claude-test"]`, "250", "200000", "150000") + `]`, wantRPM: 60, wantTPM: 100000, wantSource: "default"},
		{name: "explicit inherited values prove parent and workspace", workspace: `[{"group":{"type":"model_group"},"models":["claude-test"],"limits":[{"type":"requests_per_minute","value":250,"org_limit":4000},{"type":"input_tokens_per_minute","value":200000,"source":{"type":"organization"}},{"type":"output_tokens_per_minute","value":150000,"org_limit":800000}]}]`, wantRPM: 250, wantTPM: 150000, wantSource: "provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := DiscoveryResult{Models: []DiscoveredModel{{ID: "claude-test"}}, WorkspaceID: "wrkspc_Selected", Defaults: DiscoveryDefaults{RPM: 60, TPM: 100000, Enabled: true}, LimitsSource: "default"}
			if err := applyDiscoveredLimits(&result, tc.org, decode(tc.workspace)); err != nil || result.Defaults.RPM != tc.wantRPM || result.Defaults.TPM != tc.wantTPM || result.LimitsSource != tc.wantSource {
				t.Fatalf("scope completeness result=%+v err=%v", result, err)
			}
		})
	}
}

func TestDiscoveryRejectsUnsafeResponsesAndBoundsPagination(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{name: "provider error never returned", status: 401, body: discoverySecret},
		{name: "HTML response", status: 200, body: "<html>" + discoverySecret},
		{name: "oversized response", status: 200, body: strings.Repeat(" ", discoveryMaxBody+1)},
		{name: "missing catalog array", status: 200, body: `{}`},
		{name: "header injection metadata", status: 200, body: `{"data":[{"id":"bad\r\nmodel"}]}`},
		{name: "secret reflected in metadata", status: 200, body: `{"data":[{"id":"` + discoverySecret + `"}]}`},
		{name: "pagination cycle", status: 200, body: `{"data":[{"id":"test-model"}],"has_more":true,"last_id":"same"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := discoveryClientFor(t, func(r *http.Request) *http.Response {
				calls++
				return discoveryResponse(tc.status, tc.body, nil)
			})
			endpoint := discoveryEndpoint("openai")
			_, err := discoverCredential(context.Background(), endpoint, domain.Credential{Secret: discoverySecret}, false, client)
			if err == nil || strings.Contains(err.Error(), discoverySecret) || calls > discoveryMaxPages {
				t.Fatalf("unsafe discovery response accepted/leaked: %v calls=%d", err, calls)
			}
		})
	}
	client := &http.Client{Transport: discoveryRoundTrip(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := discoverCredential(ctx, discoveryEndpoint("openai"), domain.Credential{Secret: discoverySecret}, false, client)
	var problem *DiscoveryError
	if !errors.As(err, &problem) || problem.Code != "discovery_timeout" {
		t.Fatalf("request cancellation did not bound discovery: %v", err)
	}
}

func TestDiscoveryUsesSafeDialerAndNeverFollowsRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"test-model"}]}`)
	}))
	defer target.Close()
	endpoint := discoveryEndpoint("openai")
	endpoint.BaseURL = strings.Replace(target.URL, "http://", "https://", 1)
	if _, err := DiscoverCredential(context.Background(), endpoint, domain.Credential{Secret: discoverySecret}, "", false); err == nil || hits.Load() != 0 {
		t.Fatal("production discovery reached a private endpoint")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/models", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	endpoint.BaseURL = redirect.URL
	if _, err := DiscoverCredential(context.Background(), endpoint, domain.Credential{Secret: discoverySecret}, "", true); err == nil || hits.Load() != 0 {
		t.Fatal("discovery followed a redirect with a secret")
	}
	endpoint.BaseURL = target.URL
	if _, err := DiscoverCredential(context.Background(), endpoint, domain.Credential{Secret: discoverySecret}, "", true); err != nil || hits.Load() != 1 {
		t.Fatalf("development fixture failed: %v", err)
	}
	endpoint.BaseURL = "https://1.1.1.1"
	if _, err := DiscoverCredential(context.Background(), endpoint, domain.Credential{Secret: discoverySecret}, "socks5://127.0.0.1:1", false); err == nil {
		t.Fatal("private proxy discovery succeeded")
	}
}

func TestDiscoveryOfficialAdminProbeRequiresCanonicalOrigin(t *testing.T) {
	for _, base := range []string{"https://api.anthropic.com.attacker.example", "https://api.anthropic.com:8443", "https://api.anthropic.com/custom", "https://other.example/api.anthropic.com", "http://api.anthropic.com"} {
		endpoint := discoveryEndpoint("anthropic")
		endpoint.BaseURL = base
		calls := 0
		client := discoveryClientFor(t, func(r *http.Request) *http.Response {
			calls++
			if strings.Contains(r.URL.Path, "/organizations/") {
				t.Fatal("noncanonical host received an Admin request")
			}
			return discoveryResponse(200, discoveryModels, nil)
		})
		_, _ = discoverCredential(context.Background(), endpoint, domain.Credential{Secret: discoverySecret}, true, client)
		if calls != 1 {
			t.Fatalf("noncanonical host received %d calls", calls)
		}
	}
}

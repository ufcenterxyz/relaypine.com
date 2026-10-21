package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestAnthropicBetaHeader(t *testing.T) {
	const thinking = "interleaved-thinking-2025-05-14"
	const streaming = "fine-grained-tool-streaming-2025-05-14"
	for _, tc := range []struct {
		name   string
		values []string
		want   string
		bad    bool
	}{
		{name: "absent"},
		{name: "single", values: []string{thinking}, want: thinking},
		{name: "multiple lines and duplicates", values: []string{" \t" + thinking + ", " + streaming, thinking}, want: thinking + "," + streaming},
		{name: "current features", values: []string{"task-budgets-2026-03-13, thinking-display-updates-2026-08-18, thinking-binding-controls-2026-08-01"}, want: "task-budgets-2026-03-13,thinking-display-updates-2026-08-18,thinking-binding-controls-2026-08-01"},
		{name: "unknown", values: []string{"unreviewed-feature-2030-01-01"}, bad: true},
		{name: "mixed unknown", values: []string{thinking, "context-management-2025-06-27"}, bad: true},
		{name: "fallback", values: []string{"server-side-fallback-2026-07-01"}, bad: true},
		{name: "future fallback", values: []string{"server-side-fallback-2030-01-01"}, bad: true},
		{name: "empty", values: []string{""}, bad: true},
		{name: "empty token", values: []string{thinking + ","}, bad: true},
		{name: "invalid control", values: []string{thinking + "\r\nX-Other: value"}, bad: true},
		{name: "non ASCII whitespace", values: []string{"\u00a0" + thinking}, bad: true},
		{name: "oversized", values: []string{strings.Repeat("x", maxAnthropicBetaBytes+1)}, bad: true},
		{name: "combined oversized", values: []string{strings.Repeat(" ", 600) + thinking, strings.Repeat(" ", 600) + thinking}, bad: true},
		{name: "too many values", values: strings.Split(strings.Repeat(thinking+",", maxAnthropicBetas)+thinking, ","), bad: true},
		{name: "too many tokens", values: []string{strings.Repeat(thinking+",", maxAnthropicBetas) + thinking}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			for _, value := range tc.values {
				header.Add("Anthropic-Beta", value)
			}
			got, err := anthropicBetaHeader(header)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got %q, %v; want %q, error=%t", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestAnthropicScopedHeadersAcrossOperationsAndAuthModes(t *testing.T) {
	const betas = "fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14"
	for _, operation := range []string{"json", "sse", "count_tokens"} {
		for _, mode := range []string{"", "x-api-key", "bearer"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if got := r.Header.Values("Anthropic-Workspace-Id"); len(got) != 1 || got[0] != "wrkspc_Configured" {
						t.Errorf("wrong workspace scope: %v", got)
					}
					if r.Header.Get("Anthropic-Beta") != betas || r.Header.Get("Anthropic-Version") != "2023-06-01" {
						t.Errorf("missing supported native headers: %v", r.Header)
					}
					if mode == "bearer" {
						if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("X-Api-Key") != "" {
							t.Error("bearer upstream authentication changed")
						}
					} else if r.Header.Get("X-Api-Key") != "upstream-secret" || r.Header.Get("Authorization") != "" {
						t.Error("x-api-key upstream authentication changed")
					}
					if r.Header.Get("Cookie") != "" || r.Header.Get("X-Internal-Tenant") != "" {
						t.Error("private client headers leaked")
					}
					switch operation {
					case "sse":
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					case "count_tokens":
						if r.URL.Path != "/v1/messages/count_tokens" {
							t.Errorf("unexpected path %s", r.URL.Path)
						}
						_, _ = io.WriteString(w, `{"input_tokens":4}`)
					default:
						_, _ = io.WriteString(w, `{"usage":{"input_tokens":4,"output_tokens":2}}`)
					}
				}))
				defer server.Close()
				h, f := fixture("anthropic", server.URL)
				f.endpoints[0].AuthMode = mode
				f.credentials[0].WorkspaceID = "wrkspc_Configured"
				path, body := "/anthropic/v1/messages", `{"model":"model","messages":[]}`
				if operation == "sse" {
					body = `{"model":"model","messages":[],"stream":true}`
				} else if operation == "count_tokens" {
					path += "/count_tokens"
				}
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("X-Api-Key", "rp_test")
				r.Header.Add("Anthropic-Workspace-Id", "wrkspc_Attacker")
				r.Header.Add("Anthropic-Workspace-Id", "wrkspc_Another")
				r.Header.Add("Anthropic-Beta", " fine-grained-tool-streaming-2025-05-14, interleaved-thinking-2025-05-14 ")
				r.Header.Add("Anthropic-Beta", "fine-grained-tool-streaming-2025-05-14")
				r.Header.Set("Cookie", "session=private")
				r.Header.Set("X-Internal-Tenant", "private")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 || len(f.settlements) != 1 || f.settlements[0].Status != "completed" {
					t.Fatalf("request failed: %d %s, %+v", w.Code, w.Body, f.settlements)
				}
				wantCharge := int64(8)
				if operation == "count_tokens" {
					wantCharge = 0
				}
				if f.settlements[0].ChargeMicros != wantCharge {
					t.Fatalf("metering changed: %+v", f.settlements[0])
				}
			})
		}
	}
}

func TestAnthropicWorkspaceFollowsSelectedCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Anthropic-Workspace-Id") != "wrkspc_Second" || r.Header.Get("X-Api-Key") != "second-secret" {
			t.Error("workspace did not follow the selected upstream credential")
		}
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":4,"output_tokens":2}}`)
	}))
	defer server.Close()
	h, f := fixture("anthropic", server.URL)
	f.credentials[0].WorkspaceID = "wrkspc_First"
	f.credentials = append(f.credentials, domain.Credential{ID: "second", EndpointID: "endpoint", Secret: "second-secret", WorkspaceID: "wrkspc_Second", Enabled: true, Priority: -1, BudgetMicros: 100000000})
	h.limiter = &fakeLimiter{denied: map[string]bool{"credential:credential": true}}
	w := serve(h, "/anthropic/v1/messages", `{"model":"model","messages":[]}`)
	if w.Code != 200 || len(f.reservations) != 1 || f.reservations[0].CredentialID != "second" {
		t.Fatalf("credential selection failed: %d %s, %+v", w.Code, w.Body, f.reservations)
	}
}

func TestCurrentAnthropicBetaPayloadPreservesTokenLimitsAndPricing(t *testing.T) {
	const beta = "task-budgets-2026-03-13,thinking-display-updates-2026-08-18"
	const thinking = `{"type":"adaptive","display":"summarized"}`
	const outputConfig = `{"effort":"high","task_budget":{"type":"tokens","total":64000}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		payload := object(body)
		if r.Header.Get("Anthropic-Beta") != beta || str(payload["model"]) != "upstream-model" || string(payload["max_tokens"]) != "128" || str(payload["service_tier"]) != "standard_only" {
			t.Errorf("native routing or token limits changed: %s", body)
		}
		for key, want := range map[string]string{"thinking": thinking, "output_config": outputConfig} {
			var gotObject, wantObject any
			_ = json.Unmarshal(payload[key], &gotObject)
			_ = json.Unmarshal([]byte(want), &wantObject)
			gotJSON, _ := json.Marshal(gotObject)
			wantJSON, _ := json.Marshal(wantObject)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("%s was changed: %s", key, payload[key])
			}
		}
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":4,"output_tokens":2}}`)
	}))
	defer server.Close()
	h, f := fixture("anthropic", server.URL)
	r := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"model":"model","messages":[],"max_tokens":128,"thinking":`+thinking+`,"output_config":`+outputConfig+`}`))
	r.Header.Set("X-Api-Key", "rp_test")
	r.Header.Set("Anthropic-Beta", beta)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || len(f.reservations) != 1 || len(f.settlements) != 1 {
		t.Fatalf("request failed: %d %s", w.Code, w.Body)
	}
	s := f.settlements[0]
	if s.ChargeMicros != 8 || s.CostMicros != 4 || s.Usage.OutputTokens != 2 || s.Status != "completed" {
		t.Fatalf("beta changed metering: %+v", s)
	}
	if f.reservations[0].Request.ReservedMicros <= s.ChargeMicros {
		t.Fatal("missing conservative reservation")
	}
}

func TestClientCannotSupplyUpstreamWorkspace(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Anthropic-Workspace-Id") != "" {
					t.Error("unconfigured or cross-provider workspace header leaked")
				}
				if provider == "openai" && (r.Header.Get("Anthropic-Beta") != "" || r.Header.Get("Anthropic-Version") != "") {
					t.Error("Anthropic headers leaked to OpenAI")
				}
				_, _ = io.WriteString(w, `{"usage":{"input_tokens":4,"output_tokens":2}}`)
			}))
			defer server.Close()
			h, f := fixture(provider, server.URL)
			path := "/anthropic/v1/messages"
			if provider == "openai" {
				path = "/openai/v1/chat/completions"
				f.credentials[0].WorkspaceID = "wrkspc_Configured"
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"model","messages":[]}`))
			r.Header.Set("Authorization", "Bearer rp_test")
			r.Header.Set("Anthropic-Workspace-Id", "wrkspc_Attacker")
			r.Header.Set("Anthropic-Beta", "task-budgets-2026-03-13")
			r.Header.Set("Anthropic-Version", "2023-06-01")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("request failed: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestUnsupportedAnthropicFeaturesFailBeforeReservation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":4,"output_tokens":2}}`)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, beta, extra string
	}{
		{name: "unknown beta", beta: "unknown-feature-2030-01-01"},
		{name: "unsupported mixed beta", beta: "task-budgets-2026-03-13,context-management-2025-06-27"},
		{name: "fallback beta", beta: "server-side-fallback-2026-07-01"},
		{name: "fallback default", extra: `,"fallbacks":"default"`},
		{name: "fallback models", extra: `,"fallbacks":[{"model":"unapproved","max_tokens":1000000,"speed":"fast"}]`},
		{name: "fallback null", extra: `,"fallbacks":null`},
		{name: "fallback false", extra: `,"fallbacks":false`},
		{name: "fallback empty array", extra: `,"fallbacks":[]`},
		{name: "safe beta cannot allow fallback", beta: "task-budgets-2026-03-13", extra: `,"fallbacks":"default"`},
		{name: "safe beta cannot allow hosted tools", beta: "fine-grained-tool-streaming-2025-05-14", extra: `,"tools":[{"type":"web_search_20250305","name":"web_search"}]`},
		{name: "safe beta cannot allow premium speed", beta: "task-budgets-2026-03-13", extra: `,"speed":"fast"`},
		{name: "safe beta cannot allow one hour cache", beta: "interleaved-thinking-2025-05-14", extra: `,"cache_control":{"type":"ephemeral","ttl":"1h"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/anthropic/v1/messages", "/anthropic/v1/messages/count_tokens"} {
				h, f := fixture("anthropic", server.URL)
				r := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"model","messages":[]`+tc.extra+`}`))
				r.Header.Set("X-Api-Key", "rp_test")
				if tc.beta != "" {
					r.Header.Set("Anthropic-Beta", tc.beta)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 400 || len(f.reservations) != 0 || len(f.settlements) != 0 || h.limiter.(*fakeLimiter).acquired != 0 || calls.Load() != 0 {
					t.Fatalf("unsupported feature reached routing: %d %s, reservations=%d", w.Code, w.Body, len(f.reservations))
				}
				var response struct {
					Error struct{ Message string }
				}
				if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Error.Message == "" {
					t.Error("missing explicit rejection reason")
				}
			}
		})
	}
}

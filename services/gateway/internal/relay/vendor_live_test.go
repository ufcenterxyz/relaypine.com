package relay

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

// TestLiveBackupCompatibility sends small real requests to OpenRouter and
// ZenMux through the same failover path production uses: a local official
// upstream answers 529, so each request is rerouted to the vendor preset
// address with the vendor model name and the exact body RelayPine builds.
// It runs only with RELAYPINE_LIVE_OPENROUTER_KEY or RELAYPINE_LIVE_ZENMUX_KEY
// set, and costs a fraction of a cent per vendor.
func TestLiveBackupCompatibility(t *testing.T) {
	keys := map[string]string{"openrouter": os.Getenv("RELAYPINE_LIVE_OPENROUTER_KEY"), "zenmux": os.Getenv("RELAYPINE_LIVE_ZENMUX_KEY")}
	if keys["openrouter"] == "" && keys["zenmux"] == "" {
		t.Skip("set RELAYPINE_LIVE_OPENROUTER_KEY and/or RELAYPINE_LIVE_ZENMUX_KEY to call the real vendors")
	}
	tool := `"tools":[{"type":"function","function":{"name":"get_weather","description":"Current weather for a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],"tool_choice":"required"`
	anthropicTool := `"tools":[{"name":"get_weather","description":"Current weather for a city","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}],"tool_choice":{"type":"any"}`
	cases := []struct {
		name, provider, path, body, beta string
		check                            func(body string) bool
	}{
		{"chat", "openai", "/openai/v1/chat/completions", `{"model":"model","max_tokens":16,"messages":[{"role":"user","content":"Reply with the single word pong."}]}`, "",
			func(b string) bool { return len(object(json.RawMessage(b))["choices"]) > 2 }},
		{"chat stream", "openai", "/openai/v1/chat/completions", `{"model":"model","max_completion_tokens":16,"stream":true,"messages":[{"role":"user","content":"Reply with the single word pong."}]}`, "",
			func(b string) bool { return strings.Contains(b, "data: [DONE]") }},
		{"chat tool call", "openai", "/openai/v1/chat/completions", `{"model":"model","max_tokens":64,"messages":[{"role":"user","content":"What is the weather in Paris?"}],` + tool + `}`, "",
			func(b string) bool { return strings.Contains(b, `"tool_calls"`) && strings.Contains(b, "get_weather") }},
		{"messages", "anthropic", "/anthropic/v1/messages", `{"model":"model","max_tokens":16,"system":[{"type":"text","text":"Answer in one word.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"Say pong."}]}`, "",
			func(b string) bool { return str(object(json.RawMessage(b))["type"]) == "message" }},
		{"messages stream", "anthropic", "/anthropic/v1/messages", `{"model":"model","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"Say pong."}]}`, "",
			func(b string) bool { return strings.Contains(b, "message_stop") }},
		{"messages tool stream with beta", "anthropic", "/anthropic/v1/messages", `{"model":"model","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"What is the weather in Paris?"}],` + anthropicTool + `}`, "fine-grained-tool-streaming-2025-05-14",
			func(b string) bool { return strings.Contains(b, "tool_use") && strings.Contains(b, "message_stop") }},
	}
	for _, vendor := range []string{"openrouter", "zenmux"} {
		if keys[vendor] == "" {
			continue
		}
		for _, tc := range cases {
			t.Run(vendor+" "+tc.name, func(t *testing.T) {
				official := newRecorder(t, 529, "application/json", `{"type":"error","error":{"type":"overloaded_error"}}`)
				h, f := fixture(tc.provider, official.server.URL)
				h.limiter = &fakeLimiter{}
				f.models[0].UpstreamModel, f.models[0].MaxOutputTokens = "gpt-4.1-nano", 64
				if tc.provider == "anthropic" {
					f.models[0].UpstreamModel = "claude-haiku-4-5"
				}
				f.endpoints = append(f.endpoints, domain.Endpoint{ID: domain.BackupEndpointID(vendor), Name: vendor, Role: domain.RoleBackup, Vendor: vendor, Enabled: true})
				f.credentials = append(f.credentials, domain.Credential{ID: domain.BackupCredentialID(vendor), EndpointID: domain.BackupEndpointID(vendor), Secret: keys[vendor], Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5})
				r := newRequest(tc.path, tc.body)
				if tc.provider == "anthropic" {
					r.Header.Set("Anthropic-Version", "2023-06-01")
				}
				if tc.beta != "" {
					r.Header.Set("Anthropic-Beta", tc.beta)
				}
				w := serveRequest(h, r)
				body := w.Body.String()
				if w.Code != http.StatusOK {
					t.Fatalf("HTTP %d: %s", w.Code, body)
				}
				if len(f.reroutes) != 1 || f.reroutes[0].ToCredentialID != domain.BackupCredentialID(vendor) {
					t.Fatalf("request did not fail over: %+v", f.reroutes)
				}
				s := f.settlements
				if len(s) != 1 || s[0].Status != "completed" || !s[0].Usage.Known || s[0].Usage.InputTokens+s[0].Usage.CachedTokens+s[0].Usage.CacheWriteTokens <= 0 || s[0].Usage.OutputTokens <= 0 {
					t.Fatalf("settlement %+v\nbody: %s", s, body)
				}
				if !tc.check(body) {
					t.Fatalf("reply is not in the client protocol shape: %s", body)
				}
				t.Logf("ok: usage %+v", s[0].Usage)
			})
		}
	}
}

package relay

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

type liveCase struct {
	name, provider, path, body, beta string
	// maxOutput is the model's output cap; repeat sends the request again,
	// so a long prompt is first written to and then read from the cache.
	maxOutput, repeat int
	check             func(body string) bool
}

// vendorUsage is the usage a vendor reports in its own reply, parsed
// independently of the relay so the two can be compared.
type vendorUsage struct{ prompt, output, cacheRead, cacheWrite int64 }

func reportedUsage(provider, body string, stream bool) (vendorUsage, bool) {
	var u vendorUsage
	found := false
	read := func(raw json.RawMessage) {
		m := object(raw)
		if m == nil {
			return
		}
		if provider == "openai" {
			if _, ok := m["prompt_tokens"]; !ok {
				return
			}
			found = true
			u.prompt, _ = integer(m["prompt_tokens"])
			u.output, _ = integer(m["completion_tokens"])
			details := object(m["prompt_tokens_details"])
			u.cacheRead, _ = integer(details["cached_tokens"])
			u.cacheWrite, _ = integer(details["cache_write_tokens"])
			return
		}
		// Anthropic totals are cumulative; a later event only raises them.
		for key, target := range map[string]*int64{"input_tokens": &u.prompt, "output_tokens": &u.output, "cache_read_input_tokens": &u.cacheRead, "cache_creation_input_tokens": &u.cacheWrite} {
			if n, ok := integer(m[key]); ok && n > *target {
				*target, found = n, true
			}
		}
	}
	if !stream {
		read(object(json.RawMessage(body))["usage"])
		return u, found
	}
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		event := object(json.RawMessage(data))
		if message := object(event["message"]); message != nil {
			read(message["usage"])
		}
		read(event["usage"])
	}
	return u, found
}

// TestLiveBackupCompatibility sends small real requests to OpenRouter and
// ZenMux through the same failover path production uses: a local official
// upstream answers 529, so each request is rerouted to the vendor preset
// address with the vendor model name and the exact body RelayPine builds.
// Every reply must keep the client protocol shape, and the usage RelayPine
// settles must equal the usage the vendor reports, including cache reads,
// cache writes and thinking. It runs only with RELAYPINE_LIVE_OPENROUTER_KEY
// or RELAYPINE_LIVE_ZENMUX_KEY set, and costs about two cents per vendor.
func TestLiveBackupCompatibility(t *testing.T) {
	keys := map[string]string{"openrouter": os.Getenv("RELAYPINE_LIVE_OPENROUTER_KEY"), "zenmux": os.Getenv("RELAYPINE_LIVE_ZENMUX_KEY")}
	if keys["openrouter"] == "" && keys["zenmux"] == "" {
		t.Skip("set RELAYPINE_LIVE_OPENROUTER_KEY and/or RELAYPINE_LIVE_ZENMUX_KEY to call the real vendors")
	}
	tool := `"tools":[{"type":"function","function":{"name":"get_weather","description":"Current weather for a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],"tool_choice":"required"`
	anthropicTool := `"tools":[{"name":"get_weather","description":"Current weather for a city","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}],"tool_choice":{"type":"any"}`
	// Long enough for automatic OpenAI caching and Anthropic's minimum
	// cacheable prompt on every current model.
	longChat, _ := json.Marshal(strings.Repeat("RelayPine keeps official and backup upstream accounting identical. ", 260) + "Reply with the single word pong.")
	longSystem, _ := json.Marshal(strings.Repeat("RelayPine routes model traffic between official and backup providers without changing prices. ", 480))
	cases := []liveCase{
		{name: "chat", provider: "openai", path: "/openai/v1/chat/completions", body: `{"model":"model","max_tokens":16,"messages":[{"role":"user","content":"Reply with the single word pong."}]}`,
			check: func(b string) bool { return len(object(json.RawMessage(b))["choices"]) > 2 }},
		{name: "chat stream", provider: "openai", path: "/openai/v1/chat/completions", body: `{"model":"model","max_completion_tokens":16,"stream":true,"messages":[{"role":"user","content":"Reply with the single word pong."}]}`,
			check: func(b string) bool { return strings.Contains(b, "data: [DONE]") }},
		{name: "chat tool call", provider: "openai", path: "/openai/v1/chat/completions", body: `{"model":"model","max_tokens":64,"messages":[{"role":"user","content":"What is the weather in Paris?"}],` + tool + `}`,
			check: func(b string) bool { return strings.Contains(b, `"tool_calls"`) && strings.Contains(b, "get_weather") }},
		{name: "chat cached prompt", provider: "openai", path: "/openai/v1/chat/completions", body: `{"model":"model","max_tokens":16,"messages":[{"role":"user","content":` + string(longChat) + `}]}`, repeat: 2,
			check: func(b string) bool { return len(object(json.RawMessage(b))["choices"]) > 2 }},
		{name: "messages", provider: "anthropic", path: "/anthropic/v1/messages", body: `{"model":"model","max_tokens":16,"system":[{"type":"text","text":"Answer in one word.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"Say pong."}]}`,
			check: func(b string) bool { return str(object(json.RawMessage(b))["type"]) == "message" }},
		{name: "messages stream", provider: "anthropic", path: "/anthropic/v1/messages", body: `{"model":"model","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"Say pong."}]}`,
			check: func(b string) bool { return strings.Contains(b, "message_stop") }},
		{name: "messages tool stream with beta", provider: "anthropic", path: "/anthropic/v1/messages", body: `{"model":"model","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"What is the weather in Paris?"}],` + anthropicTool + `}`, beta: "fine-grained-tool-streaming-2025-05-14",
			check: func(b string) bool { return strings.Contains(b, "tool_use") && strings.Contains(b, "message_stop") }},
		{name: "messages prompt cache", provider: "anthropic", path: "/anthropic/v1/messages", body: `{"model":"model","max_tokens":16,"system":[{"type":"text","text":` + string(longSystem) + `,"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"Say pong."}]}`, repeat: 2,
			check: func(b string) bool { return str(object(json.RawMessage(b))["type"]) == "message" }},
		{name: "messages thinking stream", provider: "anthropic", path: "/anthropic/v1/messages", body: `{"model":"model","max_tokens":1300,"stream":true,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"Is 1001 prime? Answer yes or no."}]}`, maxOutput: 1300,
			check: func(b string) bool { return strings.Contains(b, "message_stop") }},
	}
	for _, vendor := range []string{"openrouter", "zenmux"} {
		if keys[vendor] == "" {
			continue
		}
		for _, tc := range cases {
			t.Run(vendor+" "+tc.name, func(t *testing.T) {
				for round := 1; round <= max(tc.repeat, 1); round++ {
					official := newRecorder(t, 529, "application/json", `{"type":"error","error":{"type":"overloaded_error"}}`)
					h, f := fixture(tc.provider, official.server.URL)
					h.limiter = &fakeLimiter{}
					f.models[0].UpstreamModel, f.models[0].MaxOutputTokens = "gpt-4.1-nano", max(tc.maxOutput, 64)
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
						t.Fatalf("round %d: HTTP %d: %s", round, w.Code, body)
					}
					if len(f.reroutes) != 1 || f.reroutes[0].ToCredentialID != domain.BackupCredentialID(vendor) {
						t.Fatalf("request did not fail over: %+v", f.reroutes)
					}
					s := f.settlements
					if len(s) != 1 || s[0].Status != "completed" || !s[0].Usage.Known || s[0].Usage.OutputTokens <= 0 {
						t.Fatalf("round %d: settlement %+v\nbody: %s", round, s, body)
					}
					if !tc.check(body) {
						t.Fatalf("round %d: reply is not in the client protocol shape: %s", round, body)
					}
					stream := strings.Contains(tc.body, `"stream":true`)
					reported, ok := reportedUsage(tc.provider, body, stream)
					got := s[0].Usage
					billedPrompt := got.InputTokens
					if tc.provider == "openai" {
						billedPrompt += got.CachedTokens + got.CacheWriteTokens
					}
					if !ok || reported.prompt != billedPrompt || reported.output != got.OutputTokens || reported.cacheRead != got.CachedTokens || reported.cacheWrite != got.CacheWriteTokens {
						t.Fatalf("round %d: settled usage %+v differs from the vendor's %+v", round, got, reported)
					}
					t.Logf("round %d ok: vendor usage prompt=%d output=%d cache_read=%d cache_write=%d; thinking=%v", round, reported.prompt, reported.output, reported.cacheRead, reported.cacheWrite, strings.Contains(body, `"thinking"`))
				}
			})
		}
	}
}

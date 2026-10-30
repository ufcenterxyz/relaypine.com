package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

func TestBackupModelMapping(t *testing.T) {
	cases := []struct {
		vendor, protocol, upstream, want string
		ok                               bool
	}{
		{"openrouter", "anthropic", "claude-sonnet-4-5", "anthropic/claude-sonnet-4.5", true},
		{"zenmux", "anthropic", "claude-opus-5", "anthropic/claude-opus-5", true},
		{"zenmux", "anthropic", "claude-fable-5-1", "anthropic/claude-fable-5.1", true},
		// Only documented dated aliases are resolved.
		{"openrouter", "anthropic", "claude-haiku-4-5-20251001", "anthropic/claude-haiku-4.5", true},
		{"openrouter", "anthropic", "claude-sonnet-4-20250514", "", false},
		{"openrouter", "anthropic", "claude-3-5-sonnet-20241022", "", false},
		{"openrouter", "anthropic", "anthropic/claude-sonnet-4.5", "", false},
		{"openrouter", "openai", "gpt-5.4-mini", "openai/gpt-5.4-mini", true},
		{"zenmux", "openai", "gpt-4o-2024-08-06", "openai/gpt-4o-2024-08-06", true},
		{"openrouter", "openai", "o3", "openai/o3", true},
		{"openrouter", "openai", "my-fine-tune", "", false},
		{"openrouter", "openai", "gpt-5:free", "", false},
		{"unknown", "openai", "gpt-5", "", false},
	}
	for _, tc := range cases {
		got, ok := BackupModel(tc.vendor, tc.protocol, tc.upstream)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s %s %s = %q %v, want %q %v", tc.vendor, tc.protocol, tc.upstream, got, ok, tc.want, tc.ok)
		}
	}
}

type upstreamCall struct {
	path, auth, apiKey, version, beta string
	body                              map[string]json.RawMessage
}

// recorder is a fake upstream that answers with a fixed status and body.
type recorder struct {
	mu     sync.Mutex
	calls  []upstreamCall
	server *httptest.Server
}

func newRecorder(t *testing.T, status int, contentType, body string) *recorder {
	rec := &recorder{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.calls = append(rec.calls, upstreamCall{path: r.URL.Path, auth: r.Header.Get("Authorization"), apiKey: r.Header.Get("X-Api-Key"), version: r.Header.Get("Anthropic-Version"), beta: r.Header.Get("Anthropic-Beta"), body: object(raw)})
		rec.mu.Unlock()
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(rec.server.Close)
	return rec
}
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}
func (r *recorder) last() upstreamCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

const anthropicReply = `{"id":"msg_1","type":"message","content":[{"type":"text","text":"Hi"}],"usage":{"input_tokens":20,"output_tokens":4}}`
const chatReply = `{"id":"chat_1","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":4}}`

// backupFixture has one official credential at official, plus backups at the
// given vendor servers. Vendor presets are pointed at the local servers.
func backupFixture(t *testing.T, provider string, official string, vendors map[string]*recorder) (*Handler, *fakeRepo, *fakeLimiter) {
	t.Helper()
	h, f := fixture(provider, official)
	f.models[0].UpstreamModel = "claude-sonnet-4-5"
	if provider == "openai" {
		f.models[0].UpstreamModel = "gpt-5.4-mini"
	}
	priority := len(vendors)
	for _, vendor := range []string{"openrouter", "zenmux"} {
		if vendors[vendor] == nil {
			continue
		}
		f.endpoints = append(f.endpoints, domain.Endpoint{ID: domain.BackupEndpointID(vendor), Name: vendor, Role: domain.RoleBackup, Vendor: vendor, Enabled: true})
		f.credentials = append(f.credentials, domain.Credential{ID: domain.BackupCredentialID(vendor), EndpointID: domain.BackupEndpointID(vendor), Secret: vendor + "-secret", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5, Priority: priority, QuotaGroup: "backup:" + vendor})
		priority--
	}
	h.target = func(e domain.Endpoint, protocol string) (string, string) {
		base, auth := e.Target(protocol)
		if e.IsBackup() {
			// Keep the preset path so requests prove the exact vendor routes.
			preset, _ := url.Parse(base)
			base = vendors[e.Vendor].server.URL + preset.Path
		}
		return base, auth
	}
	limiter := &fakeLimiter{}
	h.limiter = limiter
	return h, f, limiter
}

func TestAnthropicOutageFailsOverToBackup(t *testing.T) {
	official := newRecorder(t, 529, "application/json", `{"type":"error","error":{"type":"overloaded_error"}}`)
	openrouter := newRecorder(t, 200, "application/json", anthropicReply)
	h, f, limiter := backupFixture(t, "anthropic", official.server.URL, map[string]*recorder{"openrouter": openrouter})
	w := serve(h, "/anthropic/v1/messages", `{"model":"model","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	if w.Code != 200 || w.Body.String() != anthropicReply {
		t.Fatalf("response %d %s", w.Code, w.Body)
	}
	if official.count() != 1 || openrouter.count() != 1 {
		t.Fatalf("calls official=%d backup=%d", official.count(), openrouter.count())
	}
	call := openrouter.last()
	if call.path != "/api/v1/messages" || call.auth != "Bearer openrouter-secret" || call.apiKey != "" || call.version != "2023-06-01" {
		t.Fatalf("backup request %+v", call)
	}
	if str(call.body["model"]) != "anthropic/claude-sonnet-4.5" || call.body["service_tier"] != nil || string(call.body["max_tokens"]) != "64" {
		t.Fatalf("backup body %v", call.body)
	}
	if str(official.last().body["model"]) != "claude-sonnet-4-5" || str(official.last().body["service_tier"]) != "standard_only" {
		t.Fatalf("official body %v", official.last().body)
	}
	if len(f.reservations) != 1 || f.reservations[0].CredentialID != "credential" {
		t.Fatalf("reservations %+v", f.reservations)
	}
	if len(f.reroutes) != 1 || f.reroutes[0] != (domain.Reroute{FromCredentialID: "credential", ToCredentialID: "backup-openrouter", CostReserve: f.reservations[0].CostReserve, ErrorCode: "upstream_529"}) {
		t.Fatalf("reroutes %+v", f.reroutes)
	}
	// One settlement at the customer price, for the backup's verified usage.
	s := f.settlements
	if len(s) != 1 || s[0].Status != "completed" || !s[0].Usage.Known || s[0].Usage.InputTokens != 20 || s[0].ChargeMicros != 28 || s[0].CostMicros != 14 {
		t.Fatalf("settlements %+v", s)
	}
	if limiter.acquired != 2 || limiter.released != 2 || limiter.cooled["credential:credential"] != failureCooldown {
		t.Fatalf("limiter %+v", limiter)
	}
	// An official reference price pins primary routes to the official API,
	// but it does not exclude the backup that carries the same model.
	f.models[0].PricingReference = &domain.PriceReference{Provider: "anthropic", Model: "claude-sonnet-4-5", MaxInputTokens: 1000000}
	w = serve(h, "/anthropic/v1/messages", `{"model":"model","messages":[{"role":"user","content":"hello"}]}`)
	if w.Code != 200 || official.count() != 1 || openrouter.count() != 2 {
		t.Fatalf("reference-priced response %d official=%d backup=%d", w.Code, official.count(), openrouter.count())
	}
}

func TestServiceFailureSkipsSameUpstreamAndUsesZenMux(t *testing.T) {
	official := newRecorder(t, 503, "application/json", `{"error":{"message":"unavailable"}}`)
	zenmux := newRecorder(t, 200, "application/json", chatReply)
	h, f, _ := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"zenmux": zenmux})
	f.credentials = append(f.credentials, domain.Credential{ID: "second", EndpointID: "endpoint", Secret: "second", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5})
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}]}`)
	if w.Code != 200 || official.count() != 1 || zenmux.count() != 1 {
		t.Fatalf("response %d official=%d backup=%d %s", w.Code, official.count(), zenmux.count(), w.Body)
	}
	call := zenmux.last()
	if call.path != "/api/v1/chat/completions" || call.auth != "Bearer zenmux-secret" || str(call.body["model"]) != "openai/gpt-5.4-mini" || call.body["service_tier"] != nil {
		t.Fatalf("backup request %+v", call)
	}
	if len(f.reroutes) != 1 || f.reroutes[0].ToCredentialID != "backup-zenmux" {
		t.Fatalf("reroutes %+v", f.reroutes)
	}
}

func TestZenMuxMessagesUseItsAnthropicAddress(t *testing.T) {
	official := newRecorder(t, 500, "application/json", `{}`)
	zenmux := newRecorder(t, 200, "application/json", anthropicReply)
	h, _, _ := backupFixture(t, "anthropic", official.server.URL, map[string]*recorder{"zenmux": zenmux})
	w := serve(h, "/anthropic/v1/messages", `{"model":"model","messages":[{"role":"user","content":"hello"}]}`)
	call := zenmux.last()
	if w.Code != 200 || call.path != "/api/anthropic/v1/messages" || call.apiKey != "zenmux-secret" || call.auth != "" {
		t.Fatalf("response %d %+v", w.Code, call)
	}
}

func TestKeyRejectionTriesAnotherOfficialKeyFirst(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer upstream-secret" {
			w.Header().Set("Retry-After", "12")
			w.WriteHeader(429)
			return
		}
		_, _ = io.WriteString(w, chatReply)
	}))
	defer official.Close()
	openrouter := newRecorder(t, 200, "application/json", chatReply)
	h, f, limiter := backupFixture(t, "openai", official.URL, map[string]*recorder{"openrouter": openrouter})
	f.credentials = append(f.credentials, domain.Credential{ID: "second", EndpointID: "endpoint", Secret: "second", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5, Priority: -1})
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	mu.Lock()
	defer mu.Unlock()
	if w.Code != 200 || len(calls) != 2 || calls[1] != "Bearer second" || openrouter.count() != 0 {
		t.Fatalf("response %d calls %v backup %d", w.Code, calls, openrouter.count())
	}
	if len(f.reroutes) != 1 || f.reroutes[0].ToCredentialID != "second" || f.reroutes[0].ErrorCode != "upstream_429" {
		t.Fatalf("reroutes %+v", f.reroutes)
	}
	if limiter.cooled["credential:credential"] != 12*time.Second {
		t.Fatalf("cooldown %+v", limiter.cooled)
	}
}

func TestHealthyOfficialNeverTouchesBackup(t *testing.T) {
	official := newRecorder(t, 200, "application/json", chatReply)
	openrouter := newRecorder(t, 200, "application/json", chatReply)
	h, f, _ := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"openrouter": openrouter})
	// Load does not move traffic to a backup ahead of an available primary.
	h.limiter = &fakeLimiter{loads: map[string]float64{"credential:credential": 0.99, "credential:backup-openrouter": 0}}
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 200 || official.count() != 1 || openrouter.count() != 0 || len(f.reroutes) != 0 {
		t.Fatalf("response %d official=%d backup=%d", w.Code, official.count(), openrouter.count())
	}
}

func TestRequestErrorsAreNotRerouted(t *testing.T) {
	official := newRecorder(t, 400, "application/json", `{"error":{"message":"bad request"}}`)
	openrouter := newRecorder(t, 200, "application/json", chatReply)
	h, f, _ := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"openrouter": openrouter})
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 400 || openrouter.count() != 0 || len(f.reroutes) != 0 || len(f.settlements) != 1 || f.settlements[0].ChargeMicros != 0 {
		t.Fatalf("response %d backup=%d %+v", w.Code, openrouter.count(), f.settlements)
	}
}

func TestStatefulAndCountingCallsStayOfficial(t *testing.T) {
	for _, tc := range []struct{ provider, path, body string }{
		{"openai", "/openai/v1/responses", `{"model":"model","input":"hello"}`},
		{"anthropic", "/anthropic/v1/messages/count_tokens", `{"model":"model","messages":[]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			official := newRecorder(t, 503, "application/json", `{}`)
			openrouter := newRecorder(t, 200, "application/json", `{}`)
			h, f, _ := backupFixture(t, tc.provider, official.server.URL, map[string]*recorder{"openrouter": openrouter})
			w := serve(h, tc.path, tc.body)
			if w.Code != 503 || openrouter.count() != 0 || len(f.reroutes) != 0 {
				t.Fatalf("response %d backup=%d", w.Code, openrouter.count())
			}
			// Without any primary route, the backup is still not a candidate.
			f.credentials = f.credentials[1:]
			w = serve(h, tc.path, tc.body)
			if w.Code != 503 || openrouter.count() != 0 {
				t.Fatalf("backup-only response %d backup=%d", w.Code, openrouter.count())
			}
		})
	}
}

func TestBackupServesWhenNoOfficialRouteIsAdmissible(t *testing.T) {
	official := newRecorder(t, 200, "application/json", chatReply)
	openrouter := newRecorder(t, 200, "application/json", chatReply)
	h, f, limiter := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"openrouter": openrouter})
	limiter.denied = map[string]bool{"credential:credential": true}
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 200 || official.count() != 0 || openrouter.count() != 1 || len(f.reservations) != 1 || f.reservations[0].CredentialID != "backup-openrouter" {
		t.Fatalf("response %d official=%d backup=%d %+v", w.Code, official.count(), openrouter.count(), f.reservations)
	}
	// Models served only through a backup are still published.
	f.credentials = f.credentials[1:]
	f.endpoints[0].Enabled = false
	r := httptest.NewRequest("GET", "/openai/v1/models", nil)
	r.Header.Set("Authorization", "Bearer rp_test")
	models := httptest.NewRecorder()
	h.ServeHTTP(models, r)
	if !strings.Contains(models.Body.String(), `"id":"model"`) {
		t.Fatalf("model list %s", models.Body)
	}
}

func TestAttemptsAreBoundedAndFinalFailureIsFree(t *testing.T) {
	official := newRecorder(t, 502, "application/json", `{}`)
	openrouter := newRecorder(t, 503, "application/json", `{}`)
	zenmux := newRecorder(t, 500, "application/json", `{}`)
	h, f, limiter := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"openrouter": openrouter, "zenmux": zenmux})
	f.credentials = append(f.credentials, domain.Credential{ID: "late", EndpointID: "other", Secret: "late", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5})
	f.endpoints = append(f.endpoints, domain.Endpoint{ID: "other", Provider: "openai", BaseURL: official.server.URL, Enabled: true})
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	total := official.count() + openrouter.count() + zenmux.count()
	if w.Code != 503 || total != maxUpstreamAttempts || len(f.reroutes) != maxUpstreamAttempts-1 {
		t.Fatalf("response %d calls %d reroutes %+v", w.Code, total, f.reroutes)
	}
	// Primaries go first; the second official endpoint is a separate service.
	if official.count() != 2 || openrouter.count() != 1 || zenmux.count() != 0 {
		t.Fatalf("order official=%d openrouter=%d zenmux=%d", official.count(), openrouter.count(), zenmux.count())
	}
	s := f.settlements
	if len(s) != 1 || s[0].Status != "failed" || !s[0].Usage.Known || s[0].ChargeMicros != 0 || s[0].CostMicros != 0 || s[0].ErrorCode != "upstream_503" {
		t.Fatalf("settlement %+v", s)
	}
	if limiter.acquired != limiter.released {
		t.Fatalf("capacity leak %+v", limiter)
	}
}

func TestBackupOrderFollowsPriority(t *testing.T) {
	official := newRecorder(t, 503, "application/json", `{}`)
	openrouter := newRecorder(t, 200, "application/json", chatReply)
	zenmux := newRecorder(t, 200, "application/json", chatReply)
	h, f, _ := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"openrouter": openrouter, "zenmux": zenmux})
	for i := range f.credentials {
		if f.credentials[i].ID == "backup-zenmux" {
			f.credentials[i].Priority = 10
		}
	}
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 200 || zenmux.count() != 1 || openrouter.count() != 0 {
		t.Fatalf("response %d zenmux=%d openrouter=%d", w.Code, zenmux.count(), openrouter.count())
	}
}

func TestUnsentAttemptReroutesButSentFailureNeverReplays(t *testing.T) {
	openrouter := newRecorder(t, 200, "application/json", chatReply)
	// Nothing listens on port 1, so the request never leaves.
	h, f, limiter := backupFixture(t, "openai", "http://127.0.0.1:1", map[string]*recorder{"openrouter": openrouter})
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 200 || openrouter.count() != 1 || len(f.reroutes) != 1 || f.reroutes[0].ErrorCode != "upstream_transport" || limiter.cooled["credential:credential"] != failureCooldown {
		t.Fatalf("unsent: %d backup=%d %+v %+v", w.Code, openrouter.count(), f.reroutes, limiter.cooled)
	}
	// A connection that drops after the request was written may have run the
	// inference upstream, so it is never sent again.
	dropped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer dropped.Close()
	openrouter = newRecorder(t, 200, "application/json", chatReply)
	h, f, _ = backupFixture(t, "openai", dropped.URL, map[string]*recorder{"openrouter": openrouter})
	w = serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 503 || openrouter.count() != 0 || len(f.reroutes) != 0 || f.settlements[0].Status != "failed" || f.settlements[0].Usage.Known {
		t.Fatalf("sent: %d backup=%d %+v", w.Code, openrouter.count(), f.settlements)
	}
}

func TestRerouteBudgetRaceTriesNextBackup(t *testing.T) {
	official := newRecorder(t, 503, "application/json", `{}`)
	openrouter := newRecorder(t, 200, "application/json", chatReply)
	zenmux := newRecorder(t, 200, "application/json", chatReply)
	h, f, limiter := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"openrouter": openrouter, "zenmux": zenmux})
	f.rerouteErrors = map[string]error{"backup-openrouter": store.ErrBudgetExceeded}
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 200 || openrouter.count() != 0 || zenmux.count() != 1 || limiter.acquired != limiter.released {
		t.Fatalf("response %d openrouter=%d zenmux=%d %+v", w.Code, openrouter.count(), zenmux.count(), limiter)
	}
	// A storage failure while rerouting reports the original rejection.
	f.rerouteErrors = map[string]error{"backup-zenmux": errors.New("database unavailable"), "backup-openrouter": errors.New("database unavailable")}
	f.settlements = nil
	w = serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 503 || len(f.settlements) != 1 || f.settlements[0].ErrorCode != "upstream_503" {
		t.Fatalf("storage failure %d %+v", w.Code, f.settlements)
	}
}

func TestStreamingBackupPassesThroughVendorComments(t *testing.T) {
	events := ": OPENROUTER PROCESSING\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n"
	official := newRecorder(t, 529, "application/json", `{}`)
	openrouter := newRecorder(t, 200, "text/event-stream", events)
	h, f, _ := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"openrouter": openrouter})
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[],"stream":true}`)
	if w.Code != 200 || w.Body.String() != events {
		t.Fatalf("response %d %q", w.Code, w.Body)
	}
	if !strings.Contains(string(openrouter.last().body["stream_options"]), `"include_usage":true`) {
		t.Fatal("backup stream did not request usage")
	}
	s := f.settlements
	if len(s) != 1 || s[0].Status != "completed" || s[0].Usage.InputTokens != 20 || s[0].Usage.OutputTokens != 4 {
		t.Fatalf("settlement %+v", s)
	}
}

func TestBackupCheckReportsCoverage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/key", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-or-good" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"label":"relaypine","limit_remaining":12.5}}`)
	})
	mux.HandleFunc("/api/v1/models", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"anthropic/claude-sonnet-4.5"},{"id":"openai/gpt-5.4-mini"}]}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	vendor := domain.BackupVendor{ID: "openrouter", Name: "OpenRouter", OpenAIBaseURL: server.URL + "/api/v1", AnthropicBaseURL: server.URL + "/api", AnthropicAuthMode: "bearer"}
	models := []domain.Model{
		{ID: "sonnet", Provider: "anthropic", UpstreamModel: "claude-sonnet-4-5", Enabled: true},
		{ID: "mini", Provider: "openai", UpstreamModel: "gpt-5.4-mini", Enabled: true},
		{ID: "opus", Provider: "anthropic", UpstreamModel: "claude-opus-9", Enabled: true},
		{ID: "custom", Provider: "openai", UpstreamModel: "my-fine-tune"},
	}
	result, err := checkBackup(context.Background(), vendor, "sk-or-good", "", server.Client(), models)
	if err != nil || result.KeyLabel != "relaypine" || result.Balance.KeyLimitRemainingMicros == nil || *result.Balance.KeyLimitRemainingMicros != 12500000 || result.Balance.BalanceMicros != nil || result.Balance.ManagementKey {
		t.Fatalf("check %+v %v", result, err)
	}
	available := map[string]bool{}
	for _, m := range result.Models {
		available[m.ID] = m.Available
	}
	if !available["sonnet"] || !available["mini"] || available["opus"] || available["custom"] || result.Models[len(result.Models)-1].ID != "custom" {
		t.Fatalf("coverage %+v", result.Models)
	}
	_, err = checkBackup(context.Background(), vendor, "sk-or-bad", "", server.Client(), models)
	var problem *DiscoveryError
	if !errors.As(err, &problem) || problem.Code != "backup_key_rejected" || strings.Contains(problem.Message, "sk-or-bad") {
		t.Fatalf("rejected key %v", err)
	}
}

func TestZenMuxCheckUsesSeparateMessagesCatalog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/management/generation", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-ai-good" {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(404)
	})
	mux.HandleFunc("/api/v1/models", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"openai/gpt-5.4-mini"},{"id":"anthropic/claude-sonnet-4.5"}]}`)
	})
	mux.HandleFunc("/api/anthropic/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "sk-ai-good" {
			w.WriteHeader(403)
			return
		}
		if r.URL.Query().Get("after_id") == "" {
			_, _ = io.WriteString(w, `{"data":[{"id":"anthropic/claude-haiku-4.5"}],"has_more":true,"last_id":"anthropic/claude-haiku-4.5"}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"anthropic/claude-opus-5"}],"has_more":false}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	vendor := domain.BackupVendor{ID: "zenmux", Name: "ZenMux", OpenAIBaseURL: server.URL + "/api/v1", AnthropicBaseURL: server.URL + "/api/anthropic", AnthropicAuthMode: "x-api-key"}
	models := []domain.Model{{ID: "sonnet", Provider: "anthropic", UpstreamModel: "claude-sonnet-4-5", Enabled: true}, {ID: "opus", Provider: "anthropic", UpstreamModel: "claude-opus-5", Enabled: true}}
	result, err := checkBackup(context.Background(), vendor, "sk-ai-good", "", server.Client(), models)
	if err != nil || result.Models[0].ID != "opus" || !result.Models[0].Available || result.Models[1].Available {
		t.Fatalf("check %+v %v", result, err)
	}
	if _, err = checkBackup(context.Background(), vendor, "sk-ai-bad", "", server.Client(), models); err == nil {
		t.Fatal("rejected ZenMux key passed the check")
	}
}

// OpenAI reasoning models refuse max_tokens, so a chat limit the gateway fills
// in is max_completion_tokens. Backups keep receiving that limit as max_tokens,
// and a client's own field passes through unchanged.
func TestChatOutputLimitField(t *testing.T) {
	limits := func(c upstreamCall) string {
		return "max_tokens=" + string(c.body["max_tokens"]) + " max_completion_tokens=" + string(c.body["max_completion_tokens"])
	}
	for _, tc := range []struct{ name, extra, official, backup string }{
		{"filled in", "", "max_tokens= max_completion_tokens=128", "max_tokens=128 max_completion_tokens="},
		{"client max_tokens", `,"max_tokens":64`, "max_tokens=64 max_completion_tokens=", "max_tokens=64 max_completion_tokens="},
		{"client max_completion_tokens", `,"max_completion_tokens":64`, "max_tokens= max_completion_tokens=64", "max_tokens= max_completion_tokens=64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			official := newRecorder(t, 503, "application/json", `{"error":{"message":"unavailable"}}`)
			zenmux := newRecorder(t, 200, "application/json", chatReply)
			h, _, _ := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"zenmux": zenmux})
			w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}]`+tc.extra+`}`)
			if w.Code != 200 || official.count() != 1 || zenmux.count() != 1 {
				t.Fatalf("response %d official=%d backup=%d %s", w.Code, official.count(), zenmux.count(), w.Body)
			}
			if got := limits(official.last()); got != tc.official {
				t.Errorf("official %s, want %s", got, tc.official)
			}
			if got := limits(zenmux.last()); got != tc.backup {
				t.Errorf("backup %s, want %s", got, tc.backup)
			}
		})
	}
	h, _ := fixture("openai", "http://127.0.0.1:1")
	if w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[],"max_tokens":8,"max_completion_tokens":8}`); w.Code != 400 {
		t.Fatalf("both limits: %d %s", w.Code, w.Body)
	}
}

package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

type fakeRepo struct {
	mu                      sync.Mutex
	key                     domain.AccessKey
	models                  []domain.Model
	endpoints               []domain.Endpoint
	credentials             []domain.Credential
	proxies                 []domain.Proxy
	routing                 domain.RouteConfig
	reservations            []domain.Reservation
	settlements             []domain.Settlement
	payloads                map[string]domain.RequestPayload
	resources               map[string]domain.Resource
	reserveError, saveError error
}

func (f *fakeRepo) ListEndpoints(context.Context) ([]domain.Endpoint, error) { return f.endpoints, nil }
func (f *fakeRepo) ListCredentials(context.Context) ([]domain.Credential, error) {
	return f.credentials, nil
}
func (f *fakeRepo) ListProxies(context.Context) ([]domain.Proxy, error) { return f.proxies, nil }
func (f *fakeRepo) ListModels(context.Context) ([]domain.Model, error)  { return f.models, nil }
func (f *fakeRepo) AuthenticateKey(_ context.Context, secret string) (domain.AccessKey, error) {
	if secret != "rp_test" {
		return domain.AccessKey{}, errors.New("unauthorized")
	}
	return f.key, nil
}
func (f *fakeRepo) Routing(context.Context) (domain.RouteConfig, error) { return f.routing, nil }
func (f *fakeRepo) Reserve(_ context.Context, v domain.Reservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reserveError != nil {
		return f.reserveError
	}
	f.reservations = append(f.reservations, v)
	return nil
}
func (f *fakeRepo) Settle(_ context.Context, _ string, v domain.Settlement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settlements = append(f.settlements, v)
	return nil
}
func (f *fakeRepo) SavePayload(_ context.Context, id string, v domain.RequestPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.payloads == nil {
		f.payloads = map[string]domain.RequestPayload{}
	}
	f.payloads[id] = v
	return nil
}
func (f *fakeRepo) SaveResource(_ context.Context, v domain.Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveError != nil {
		return f.saveError
	}
	f.resources[v.ID] = v
	return nil
}
func (f *fakeRepo) Resource(_ context.Context, tenant, project, id string) (domain.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.resources[id]
	if !ok || v.TenantID != tenant || v.ProjectID != project {
		return domain.Resource{}, errors.New("not found")
	}
	return v, nil
}

type fakeLimiter struct {
	loads              map[string]float64
	denied             map[string]bool
	acquired, released int
}

func (l *fakeLimiter) load(_ context.Context, b []limits) (float64, error) {
	for _, v := range b {
		if score, ok := l.loads[v.scope]; ok {
			return score, nil
		}
	}
	return 0, nil
}
func (l *fakeLimiter) acquire(_ context.Context, _ string, b []limits, _ int64) error {
	for _, v := range b {
		if l.denied[v.scope] {
			return errCapacity
		}
	}
	l.acquired++
	return nil
}
func (l *fakeLimiter) release(context.Context, string, []limits) error     { l.released++; return nil }
func (l *fakeLimiter) cool(context.Context, []limits, time.Duration) error { return nil }

func fixture(provider, url string) (*Handler, *fakeRepo) {
	f := &fakeRepo{key: domain.AccessKey{ID: "access", TenantID: "tenant", ProjectID: "project", Enabled: true, RPM: 60}, models: []domain.Model{{ID: "model", Name: "Test Model", UpstreamModel: "upstream-model", Provider: provider, Enabled: true, InputRate: 1000000, OutputRate: 2000000, CachedRate: 500000, CacheWriteRate: 1250000, CostInputRate: 500000, CostOutputRate: 1000000, CostCachedRate: 250000, CostCacheWriteRate: 625000, MaxOutputTokens: 128, Version: 1}}, endpoints: []domain.Endpoint{{ID: "endpoint", Provider: provider, BaseURL: url, Enabled: true}}, credentials: []domain.Credential{{ID: "credential", EndpointID: "endpoint", Secret: "upstream-secret", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5, BudgetMicros: 100000000}}, resources: map[string]domain.Resource{}}
	return &Handler{repo: f, limiter: &fakeLimiter{}, allowPrivate: true, capture: true}, f
}
func serve(h *Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer rp_test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNativeForwardingAndPricing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Internal-Tenant") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Errorf("unsafe headers: %v", r.Header)
		}
		b, _ := io.ReadAll(r.Body)
		obj := object(b)
		if str(obj["model"]) != "upstream-model" || string(obj["max_tokens"]) != "128" || string(obj["future_option"]) != "42" {
			t.Errorf("unexpected body %s", b)
		}
		w.Header().Set("Set-Cookie", "secret=bad")
		_, _ = io.WriteString(w, `{"id":"chat_1","future_response":true,"usage":{"prompt_tokens":1000,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":200}}}`)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL+"/gateway/v1")
	r := httptest.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}],"future_option":42}`))
	r.Header.Set("Authorization", "Bearer rp_test")
	r.Header.Set("Cookie", "session=private")
	r.Header.Set("X-Api-Key", "other-secret")
	r.Header.Set("X-Internal-Tenant", "private")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"future_response":true`) || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("response %d %s", w.Code, w.Body)
	}
	if len(f.settlements) != 1 || f.settlements[0].ChargeMicros != 920 || f.settlements[0].CostMicros != 460 {
		t.Fatalf("settlement %+v", f.settlements)
	}
	if f.reservations[0].Request.ReservedMicros <= f.settlements[0].ChargeMicros {
		t.Fatal("reservation did not conservatively cover charge")
	}
}

func TestStreamingUsage(t *testing.T) {
	cases := []struct {
		name, provider, path, events string
		input, output, cached, write int64
	}{
		{"chat", "openai", "/openai/v1/chat/completions", "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":5}}}\n\ndata: [DONE]\n\n", 7, 7, 5, 0},
		{"anthropic", "anthropic", "/anthropic/v1/messages", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":1,\"cache_read_input_tokens\":5,\"cache_creation_input_tokens\":3}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", 12, 7, 5, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if tc.provider == "openai" && !strings.Contains(string(b), `"include_usage":true`) {
					t.Error("missing streaming usage request")
				}
				if tc.provider == "anthropic" && r.Header.Get("X-Api-Key") != "upstream-secret" {
					t.Error("missing Anthropic key")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.events)
			}))
			defer server.Close()
			h, f := fixture(tc.provider, server.URL)
			w := serve(h, tc.path, `{"model":"model","messages":[{"role":"user","content":"hello"}],"stream":true}`)
			if w.Code != 200 || w.Body.String() != tc.events {
				t.Fatalf("response %d %s", w.Code, w.Body)
			}
			s := f.settlements[0]
			if s.Status != "completed" || !s.Usage.Known || s.Usage.InputTokens != tc.input || s.Usage.OutputTokens != tc.output || s.Usage.CachedTokens != tc.cached || s.Usage.CacheWriteTokens != tc.write {
				t.Fatalf("settlement %+v", s)
			}
		})
	}
}

func TestResponsesOwnershipAndContinuation(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Error("continuation switched upstream")
		}
		id := "resp_1"
		if calls > 1 {
			id = "resp_2"
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"status":"completed","usage":{"input_tokens":20,"output_tokens":4}}`, id)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	w := serve(h, "/openai/v1/responses", `{"model":"model","input":"hello"}`)
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	if f.resources["resp_1"].InputTokens <= 0 {
		t.Fatal("missing conservative history bound")
	}
	f.credentials = append(f.credentials, domain.Credential{ID: "other", EndpointID: "endpoint", Secret: "other", Enabled: true, BudgetMicros: 200000000, Priority: 99})
	w = serve(h, "/openai/v1/responses", `{"model":"model","previous_response_id":"resp_1","input":"continue"}`)
	if w.Code != 200 || f.reservations[1].Request.CredentialID != "credential" {
		t.Fatalf("continuation %d %s", w.Code, w.Body)
	}
	if f.resources["resp_2"].InputTokens <= f.resources["resp_1"].InputTokens {
		t.Fatal("history reservation did not accumulate")
	}
	f.key.ProjectID = "another-project"
	r := httptest.NewRequest("GET", "/openai/v1/responses/resp_1", nil)
	r.Header.Set("Authorization", "Bearer rp_test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || calls != 2 {
		t.Fatalf("cross project read %d calls=%d", w.Code, calls)
	}
}

type ownershipWriter struct {
	*httptest.ResponseRecorder
	repo *fakeRepo
	t    *testing.T
}

func (w *ownershipWriter) Write(b []byte) (int, error) {
	if strings.Contains(string(b), "resp_stream") {
		if _, ok := w.repo.resources["resp_stream"]; !ok {
			w.t.Error("response ID exposed before ownership persisted")
		}
	}
	return w.ResponseRecorder.Write(b)
}
func (w *ownershipWriter) Flush() { w.ResponseRecorder.Flush() }
func TestResponsesSSEBindingBeforeExposure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_stream\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"usage\":{\"input_tokens\":10,\"output_tokens\":3}}}\n\n")
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	r := httptest.NewRequest("POST", "/openai/v1/responses", strings.NewReader(`{"model":"model","input":"hi","stream":true}`))
	r.Header.Set("Authorization", "Bearer rp_test")
	w := &ownershipWriter{httptest.NewRecorder(), f, t}
	h.ServeHTTP(w, r)
	if w.Code != 200 || f.settlements[0].Status != "completed" {
		t.Fatalf("status %d %+v", w.Code, f.settlements)
	}
}

func TestMissingAndIncompleteUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
				} else {
					_, _ = io.WriteString(w, `{"choices":[]}`)
				}
			}))
			defer server.Close()
			h, f := fixture("openai", server.URL)
			w := serve(h, "/openai/v1/chat/completions", fmt.Sprintf(`{"model":"model","messages":[],"stream":%t}`, stream))
			s := f.settlements[0]
			if s.Usage.Known || s.ChargeMicros != 0 {
				t.Fatalf("invented usage %+v", s)
			}
			if stream && (s.Status != "failed" || !strings.Contains(w.Body.String(), "ended before completion")) {
				t.Fatalf("incomplete stream %+v %s", s, w.Body)
			}
		})
	}
}

func TestFailClosedNetworkAndNoReplay(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "secret upstream diagnostic", 500)
	}))
	defer server.Close()
	for _, mode := range []string{"failure", "proxy", "ssrf", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls.Store(0)
			h, f := fixture("openai", server.URL)
			if mode == "proxy" {
				f.credentials[0].ProxyID = "proxy"
				f.proxies = []domain.Proxy{{ID: "proxy", Enabled: true, URL: "socks5://127.0.0.1:1"}}
			}
			if mode == "ssrf" {
				h.allowPrivate = false
				f.endpoints[0].BaseURL = strings.Replace(server.URL, "http:", "https:", 1)
			}
			if mode == "redirect" {
				redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", server.URL)
					w.WriteHeader(307)
				}))
				defer redirect.Close()
				f.endpoints[0].BaseURL = redirect.URL
			}
			w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
			if w.Code < 400 || strings.Contains(w.Body.String(), "secret upstream diagnostic") {
				t.Fatalf("bad failure %d %s", w.Code, w.Body)
			}
			expected := int64(0)
			if mode == "failure" {
				expected = 1
			}
			if calls.Load() != expected {
				t.Fatalf("upstream calls %d expected %d", calls.Load(), expected)
			}
			if f.settlements[0].ChargeMicros != 0 {
				t.Fatal("failed request charged")
			}
			if (mode == "proxy" || mode == "ssrf") && f.settlements[0].Status != "not_sent" {
				t.Fatalf("pre-send failure %+v", f.settlements[0])
			}
		})
	}
}

func TestUnsupportedCapabilitiesAndBudget(t *testing.T) {
	h, f := fixture("openai", "https://api.openai.com")
	for _, extra := range []string{`"background":true`, `"n":2`, `"tools":[{"type":"web_search"}]`, `"input":[{"type":"input_image","image_url":"https://example.com/a.jpg"}]`, `"input":[{"type":"item_reference","id":"foreign"}]`, `"max_tokens":99999`, `"conversation":"foreign"`, `"service_tier":"priority"`} {
		w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[],`+extra+`}`)
		if w.Code != 400 {
			t.Errorf("accepted %s: %d", extra, w.Code)
		}
	}
	if len(f.reservations) > 0 {
		t.Fatal("unsupported request reserved budget")
	}
	f.reserveError = errors.New("balance insufficient")
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 402 || len(f.settlements) != 0 {
		t.Fatalf("budget failure %d %+v", w.Code, f.settlements)
	}
	l := h.limiter.(*fakeLimiter)
	if l.acquired != l.released {
		t.Fatal("reservation failure leaked concurrency")
	}
}

func TestRouteFallbackAndStrategy(t *testing.T) {
	h, f := fixture("openai", "https://api.openai.com")
	f.credentials = append(f.credentials, domain.Credential{ID: "alternate", EndpointID: "endpoint", Secret: "second", Enabled: true, BudgetMicros: 200000000, RPM: 100, TPM: 100000, MaxConcurrency: 10})
	h.limiter = &fakeLimiter{loads: map[string]float64{"credential:credential": 0.2, "credential:alternate": 0.8}}
	p := plan{provider: "openai", path: "/v1/chat/completions", key: f.key, model: f.models[0]}
	c, err := h.candidates(context.Background(), p, domain.RouteConfig{Strategy: "least_load"})
	if err != nil || c[0].credential.ID != "credential" {
		t.Fatalf("least load %+v %v", c, err)
	}
	c, err = h.candidates(context.Background(), p, domain.RouteConfig{Strategy: "highest_balance"})
	if err != nil || c[0].credential.ID != "alternate" {
		t.Fatal("balance strategy ignored remaining budget")
	}
}

func TestCountTokensIsFreeAndUsesAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Error(r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"input_tokens":42}`)
	}))
	defer server.Close()
	h, f := fixture("anthropic", server.URL)
	r := httptest.NewRequest("POST", "/anthropic/v1/messages/count_tokens", strings.NewReader(`{"model":"model","messages":[]}`))
	r.Header.Set("X-Api-Key", "rp_test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || f.settlements[0].ChargeMicros != 0 || f.reservations[0].Request.ReservedMicros != 0 {
		t.Fatalf("free count %d %+v", w.Code, f.settlements)
	}
}

func TestAuthentication(t *testing.T) {
	h, f := fixture("openai", "https://api.openai.com")
	r := httptest.NewRequest("GET", "/openai/v1/models", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || len(f.reservations) > 0 {
		t.Fatal("missing key accepted")
	}
}

func TestUsageMath(t *testing.T) {
	u, ok := parseUsage(json.RawMessage(`{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":11}}`), "openai")
	if ok || u.Known {
		t.Fatal("invalid cached total accepted")
	}
	if _, err := amount(domain.Usage{InputTokens: 1 << 62, Known: true}, 1<<62, 0, 0, 0); err == nil {
		t.Fatal("overflow accepted")
	}
	n, err := amount(domain.Usage{InputTokens: 1, Known: true}, 1, 0, 0, 0)
	if err != nil || n != 1 {
		t.Fatal("fractional micro-USD was truncated")
	}
}

type disconnectedWriter struct{ header http.Header }

func (w *disconnectedWriter) Header() http.Header       { return w.header }
func (w *disconnectedWriter) WriteHeader(int)           {}
func (w *disconnectedWriter) Write([]byte) (int, error) { return 0, errors.New("client disconnected") }
func (w *disconnectedWriter) Flush()                    {}

type terminalDisconnectWriter struct {
	*httptest.ResponseRecorder
	terminal       string
	onFlush, armed bool
}

func (w *terminalDisconnectWriter) Write(body []byte) (int, error) {
	if strings.Contains(string(body), w.terminal) {
		if !w.onFlush {
			return 0, errors.New("client disconnected at terminal event")
		}
		w.armed = true
	}
	return w.ResponseRecorder.Write(body)
}
func (w *terminalDisconnectWriter) FlushError() error {
	if w.armed {
		return errors.New("client disconnected during terminal flush")
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestCanceledJSONWithVerifiedUsageIsCharged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":4}}`)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	r := httptest.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[]}`))
	r.Header.Set("Authorization", "Bearer rp_test")
	h.ServeHTTP(&disconnectedWriter{header: http.Header{}}, r)
	s := f.settlements[0]
	if s.Status != "canceled" || !s.Usage.Known || s.ChargeMicros != 28 || s.CostMicros != 14 {
		t.Fatalf("verified JSON usage lost after disconnect: %+v", s)
	}
}

func TestCanceledSSETerminalWithVerifiedUsageIsCharged(t *testing.T) {
	cases := []struct{ name, provider, path, events, terminal string }{
		{"chat", "openai", "/openai/v1/chat/completions", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n", "[DONE]"},
		{"anthropic", "anthropic", "/anthropic/v1/messages", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":20,\"output_tokens\":1}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "message_stop"},
		{"responses", "openai", "/openai/v1/responses", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_cancel\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cancel\",\"usage\":{\"input_tokens\":20,\"output_tokens\":4}}}\n\n", "response.completed"},
	}
	for _, tc := range cases {
		for _, onFlush := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/flush=%t", tc.name, onFlush), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, tc.events)
				}))
				defer server.Close()
				h, f := fixture(tc.provider, server.URL)
				r := httptest.NewRequest("POST", tc.path, strings.NewReader(`{"model":"model","input":"hi","messages":[],"stream":true}`))
				r.Header.Set("Authorization", "Bearer rp_test")
				w := &terminalDisconnectWriter{ResponseRecorder: httptest.NewRecorder(), terminal: tc.terminal, onFlush: onFlush}
				h.ServeHTTP(w, r)
				s := f.settlements[0]
				if s.Status != "canceled" || !s.Usage.Known || s.ChargeMicros != 28 || s.CostMicros != 14 {
					t.Fatalf("verified terminal usage lost after disconnect: %+v", s)
				}
			})
		}
	}
}

func TestUpstreamFailedSSEWithDisconnectedClientStaysFree(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failure\",\"usage\":{\"input_tokens\":20,\"output_tokens\":4}}}\n\n")
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	r := httptest.NewRequest("POST", "/openai/v1/responses", strings.NewReader(`{"model":"model","input":"hi","stream":true}`))
	r.Header.Set("Authorization", "Bearer rp_test")
	w := &terminalDisconnectWriter{ResponseRecorder: httptest.NewRecorder(), terminal: "response.failed"}
	h.ServeHTTP(w, r)
	s := f.settlements[0]
	if s.Status != "failed" || !s.Usage.Known || s.ChargeMicros != 0 || s.CostMicros != 14 {
		t.Fatalf("upstream failure charged customer or lost verified cost: %+v", s)
	}
}

func TestTerminalResourceBindingFailureIsFreeWithVerifiedCost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_unsaved\",\"usage\":{\"input_tokens\":20,\"output_tokens\":4}}}\n\n")
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	f.saveError = errors.New("storage failed")
	w := serve(h, "/openai/v1/responses", `{"model":"model","input":"hi","stream":true}`)
	s := f.settlements[0]
	if w.Code != 502 || s.Status != "failed" || !s.Usage.Known || s.ChargeMicros != 0 || s.CostMicros != 14 {
		t.Fatalf("platform failure billing: HTTP %d %+v", w.Code, s)
	}
}
func TestStreamErrorAndClientDisconnectPreserveUncertainUpstreamCost(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint(disconnect), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":20,\"output_tokens\":1}}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n")
			}))
			defer server.Close()
			h, f := fixture("anthropic", server.URL)
			r := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"model":"model","messages":[],"stream":true}`))
			r.Header.Set("X-Api-Key", "rp_test")
			var w http.ResponseWriter = httptest.NewRecorder()
			if disconnect {
				w = &disconnectedWriter{header: http.Header{}}
			}
			h.ServeHTTP(w, r)
			s := f.settlements[0]
			if s.ChargeMicros != 0 || s.Usage.Known {
				t.Fatalf("partial usage was treated as final %+v", s)
			}
			expected := "failed"
			if disconnect {
				expected = "canceled"
			}
			if s.Status != expected {
				t.Fatalf("status %s expected %s", s.Status, expected)
			}
		})
	}
}

func TestCapacityFallbackBeforeSending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer second" {
			t.Error("did not select available alternate key")
		}
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	f.credentials = append(f.credentials, domain.Credential{ID: "alternate", EndpointID: "endpoint", Secret: "second", Enabled: true, Priority: -1, BudgetMicros: 100000000})
	h.limiter = &fakeLimiter{denied: map[string]bool{"credential:credential": true}}
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 200 || len(f.reservations) != 1 || f.reservations[0].CredentialID != "alternate" {
		t.Fatalf("fallback failed %d %+v", w.Code, f.reservations)
	}
}

func TestStandardTierAndCacheControls(t *testing.T) {
	for _, tc := range []struct{ path, tier string }{{"/v1/chat/completions", "default"}, {"/v1/messages", "standard_only"}} {
		m := object([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`))
		if err := validatePayload(m, tc.path); err != nil {
			t.Fatal(err)
		}
		if str(m["service_tier"]) != tc.tier {
			t.Errorf("tier %s", m["service_tier"])
		}
	}
	for _, body := range []string{`{"speed":"fast"}`, `{"inference_geo":"us"}`, `{"cache_control":{"type":"ephemeral","ttl":"1h"}}`, `{"tools":[{"name":"tool","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`} {
		if err := validatePayload(object([]byte(body)), "/v1/messages"); err == nil {
			t.Errorf("accepted premium capability %s", body)
		}
	}
}

func TestFailedResponsesSettleVerifiedCostWithoutCustomerCharge(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := `{"id":"resp_failed","status":"failed","error":{"message":"failed"},"usage":{"input_tokens":20,"output_tokens":4}}`
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":%s}\n\n", response)
				} else {
					_, _ = io.WriteString(w, response)
				}
			}))
			defer server.Close()
			h, f := fixture("openai", server.URL)
			_ = serve(h, "/openai/v1/responses", fmt.Sprintf(`{"model":"model","input":"hi","stream":%t}`, stream))
			s := f.settlements[0]
			if s.Status != "failed" || s.ChargeMicros != 0 || !s.Usage.Known || s.CostMicros != 14 {
				t.Fatalf("verified failure cost lost %+v", s)
			}
		})
	}
}

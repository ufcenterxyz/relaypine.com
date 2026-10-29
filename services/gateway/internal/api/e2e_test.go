package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/relay"
	"relaypine.com/gateway/internal/store"
)

// Restrict a shared integration database to this test's own configuration. All
// persistence, authentication, budget transactions, and settlement remain the
// production Store. No test request can select an unrelated real API credential.
type isolatedE2ERepository struct {
	*store.Store
	prefix string
}

func (s isolatedE2ERepository) ListEndpoints(ctx context.Context) ([]domain.Endpoint, error) {
	all, err := s.Store.ListEndpoints(ctx)
	out := []domain.Endpoint{}
	for _, v := range all {
		if strings.HasPrefix(v.ID, s.prefix) {
			out = append(out, v)
		}
	}
	return out, err
}
func (s isolatedE2ERepository) ListCredentials(ctx context.Context) ([]domain.Credential, error) {
	all, err := s.Store.ListCredentials(ctx)
	out := []domain.Credential{}
	for _, v := range all {
		if strings.HasPrefix(v.ID, s.prefix) {
			out = append(out, v)
		}
	}
	return out, err
}
func (s isolatedE2ERepository) ListModels(ctx context.Context) ([]domain.Model, error) {
	all, err := s.Store.ListModels(ctx)
	out := []domain.Model{}
	for _, v := range all {
		if strings.HasPrefix(v.ID, s.prefix) {
			out = append(out, v)
		}
	}
	return out, err
}

func TestNativeProtocolsEndToEndWithDurableBilling(t *testing.T) {
	f := newAPIFixture(t)
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		addr = os.Getenv("REDIS_ADDR")
	}
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR/REDIS_ADDR required for complete gateway integration")
	}
	redisClient := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD")})
	t.Cleanup(func() { redisClient.Close() })
	if err := redisClient.Ping(f.ctx).Err(); err != nil {
		t.Fatal(err)
	}
	f.s.Redis = redisClient
	f.s.AllowPrivate = true
	f.s.Relay = relay.New(isolatedE2ERepository{Store: f.db, prefix: f.prefix}, redisClient, true, true)
	f.h = f.s.Handler()
	// Request rows must be removed before the base fixture removes users/keys.
	t.Cleanup(func() {
		for _, tenant := range []string{f.operator.TenantID, f.other.TenantID} {
			if _, err := f.db.DB.ExecContext(context.Background(), "DELETE a FROM attempts a JOIN requests r ON r.id=a.request_id WHERE r.tenant_id=?", tenant); err != nil {
				t.Logf("attempt cleanup: %v", err)
			}
			if _, err := f.db.DB.ExecContext(context.Background(), "DELETE FROM requests WHERE tenant_id=?", tenant); err != nil {
				t.Logf("request cleanup: %v", err)
			}
		}
	})
	const openAIJSON = `{"id":"chat_json","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Hello JSON"}}],"unknown_output":44,"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4}}}`
	const claudeJSON = `{"id":"msg_json","type":"message","role":"assistant","content":[{"type":"text","text":"Hello JSON"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":4}}`
	const openAISSE = "data: {\"choices\":[{\"delta\":{\"content\":\"Hello stream\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\ndata: [DONE]\n\n"
	const claudeSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"usage\":{\"input_tokens\":9,\"output_tokens\":1,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":2}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello stream\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	type upstreamFixture struct {
		provider, path, model, endpoint, credential, secret string
		hits                                                atomic.Int32
		server                                              *httptest.Server
	}
	upstreams := []*upstreamFixture{{provider: "openai", path: "/custom/v1/chat/completions"}, {provider: "anthropic", path: "/bridge/v1/messages"}}
	for _, u := range upstreams {
		u.model = f.prefix + "m_" + u.provider
		u.endpoint = f.prefix + "e_" + u.provider
		u.credential = f.prefix + "c_" + u.provider
		u.secret = "mock-upstream-" + u.provider + "-" + testID()
		f.models = append(f.models, u.model)
		f.endpoints = append(f.endpoints, u.endpoint)
		f.credentials = append(f.credentials, u.credential)
		u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u.hits.Add(1)
			if r.Method != "POST" || r.URL.Path != u.path {
				t.Errorf("%s path/method: %s %s", u.provider, r.Method, r.URL.Path)
			}
			if r.Header.Get("Cookie") != "" || r.Header.Get("X-Internal-Tenant") != "" {
				t.Error("customer private headers reached upstream")
			}
			if u.provider == "openai" {
				if r.Header.Get("Authorization") != "Bearer "+u.secret || r.Header.Get("X-Api-Key") != "" {
					t.Error("OpenAI upstream authentication incorrect")
				}
			} else if r.Header.Get("X-Api-Key") != u.secret || r.Header.Get("Authorization") != "" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
				t.Error("Anthropic upstream authentication/version incorrect")
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("upstream payload: %v", err)
				w.WriteHeader(400)
				return
			}
			var model string
			_ = json.Unmarshal(body["model"], &model)
			limit := "max_tokens"
			if u.provider == "openai" {
				// OpenAI reasoning models refuse max_tokens.
				limit = "max_completion_tokens"
			}
			if model != "mock-"+u.provider || string(body[limit]) != "64" || (limit != "max_tokens" && body["max_tokens"] != nil) || string(body["probe"]) != "true" {
				t.Errorf("upstream model/cap/unknown field not preserved: %s", u.provider)
			}
			w.Header().Set("Set-Cookie", "upstream-secret=must-not-be-forwarded")
			if string(body["stream"]) == "true" {
				w.Header().Set("Content-Type", "text/event-stream")
				payload := claudeSSE
				if u.provider == "openai" {
					payload = openAISSE
					var options map[string]bool
					_ = json.Unmarshal(body["stream_options"], &options)
					if !options["include_usage"] {
						t.Error("OpenAI stream did not request final usage")
					}
				}
				for _, frame := range strings.Split(strings.TrimSuffix(payload, "\n\n"), "\n\n") {
					_, _ = io.WriteString(w, frame+"\n\n")
					w.(http.Flusher).Flush()
				}
			} else {
				w.Header().Set("Content-Type", "application/json")
				payload := claudeJSON
				if u.provider == "openai" {
					payload = openAIJSON
				}
				_, _ = io.WriteString(w, payload)
			}
		}))
		t.Cleanup(u.server.Close)
		basePath := "/custom/v1"
		if u.provider == "anthropic" {
			basePath = "/bridge/v1"
		}
		expectStatus(t, f.call(t, "POST", "/api/admin/endpoints", f.operatorCookie, f.s.AdminAppURL, domain.Endpoint{ID: u.endpoint, Name: "E2E " + u.provider, Provider: u.provider, BaseURL: u.server.URL + basePath, Enabled: true, Models: []string{u.model}}), 200)
		expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AdminAppURL, domain.Model{ID: u.model, Name: "E2E " + u.provider, Provider: u.provider, UpstreamModel: "mock-" + u.provider, Enabled: true, InputRate: 2000000, OutputRate: 4000000, CachedRate: 1000000, CacheWriteRate: 3000000, CostInputRate: 1000000, CostOutputRate: 2000000, CostCachedRate: 500000, CostCacheWriteRate: 1500000, MaxOutputTokens: 64}), 200)
		expectStatus(t, f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AdminAppURL, map[string]any{"id": u.credential, "name": "E2E " + u.provider, "endpoint_id": u.endpoint, "secret": u.secret, "quota_group": f.prefix + u.provider, "enabled": true, "rpm": 100, "tpm": 1000000, "max_concurrency": 10, "budget_micros": 1000000}), 200)
	}
	modelIDs := []string{upstreams[0].model, upstreams[1].model}
	type issued struct {
		Key    domain.AccessKey `json:"key"`
		Secret string           `json:"secret"`
	}
	issue := func(cookie *http.Cookie) issued {
		w := f.call(t, "POST", "/api/keys", cookie, f.s.AdminAppURL, map[string]any{"name": "Native SDK integration", "rpm": 100, "models": modelIDs})
		expectStatus(t, w, 201)
		return decodeResponse[issued](t, w)
	}
	access := issue(f.operatorCookie)
	legacyKey, legacySecret := f.legacyKey(t, f.other)
	emptyAccess := issued{Key: legacyKey, Secret: legacySecret}
	redisScopes := []string{"tenant:" + f.operator.TenantID, "tenant:" + f.other.TenantID, "access:" + access.Key.ID, "access:" + emptyAccess.Key.ID}
	for _, u := range upstreams {
		redisScopes = append(redisScopes, "credential:"+u.credential, "group:"+f.prefix+u.provider)
	}
	t.Cleanup(func() {
		for _, scope := range redisScopes {
			sum := sha256.Sum256([]byte(scope))
			prefix := "relay:" + hex.EncodeToString(sum[:16]) + ":"
			var cursor uint64
			for {
				keys, next, err := redisClient.Scan(context.Background(), cursor, prefix+"*", 100).Result()
				if err != nil {
					t.Logf("Redis cleanup: %v", err)
					break
				}
				if len(keys) > 0 {
					redisClient.Del(context.Background(), keys...)
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
		}
	})
	const initialBalance int64 = 1000000
	expectStatus(t, f.call(t, "POST", "/api/admin/credits", f.operatorCookie, f.s.AdminAppURL, map[string]any{"tenant_id": f.operator.TenantID, "amount_micros": initialBalance, "reason": "E2E verified test credit", "idempotency_key": f.prefix + "funds"}), 200)
	gateway := httptest.NewServer(f.h)
	t.Cleanup(gateway.Close)
	client := gateway.Client()
	client.Timeout = 10 * time.Second
	type expectation struct {
		provider     string
		stream       bool
		payload      string
		charge, cost int64
		usage        domain.Usage
	}
	cases := []expectation{
		{provider: "openai", payload: openAIJSON, charge: 32, cost: 16, usage: domain.Usage{InputTokens: 8, OutputTokens: 3, CachedTokens: 4, Known: true}},
		{provider: "openai", stream: true, payload: openAISSE, charge: 34, cost: 17, usage: domain.Usage{InputTokens: 8, OutputTokens: 4, CachedTokens: 2, Known: true}},
		{provider: "anthropic", payload: claudeJSON, charge: 40, cost: 20, usage: domain.Usage{InputTokens: 7, OutputTokens: 3, CachedTokens: 2, CacheWriteTokens: 4, Known: true}},
		{provider: "anthropic", stream: true, payload: claudeSSE, charge: 47, cost: 24, usage: domain.Usage{InputTokens: 9, OutputTokens: 5, CachedTokens: 3, CacheWriteTokens: 2, Known: true}},
	}
	request := func(tc expectation, key issued) (*http.Response, []byte) {
		path := "/openai/v1/chat/completions"
		model := upstreams[0].model
		if tc.provider == "anthropic" {
			path = "/anthropic/v1/messages"
			model = upstreams[1].model
		}
		body, _ := json.Marshal(map[string]any{"model": model, "messages": []any{map[string]string{"role": "user", "content": "hello end to end"}}, "stream": tc.stream, "probe": true})
		r, err := http.NewRequestWithContext(f.ctx, "POST", gateway.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", "customer_private=true")
		r.Header.Set("X-Internal-Tenant", "forged-tenant")
		if tc.provider == "openai" {
			r.Header.Set("Authorization", "Bearer "+key.Secret)
		} else {
			r.Header.Set("X-Api-Key", key.Secret)
			r.Header.Set("Anthropic-Version", "2023-06-01")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return response, raw
	}
	var totalCharge, totalCost int64
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s_stream_%t", tc.provider, tc.stream), func(t *testing.T) {
			response, raw := request(tc, access)
			if response.StatusCode != 200 || string(raw) != tc.payload {
				t.Fatalf("native bytes/status changed: status=%d body=%s", response.StatusCode, raw)
			}
			if response.Header.Get("Set-Cookie") != "" {
				t.Fatal("upstream cookie leaked")
			}
			if tc.stream && (response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("X-Accel-Buffering") != "no") {
				t.Fatal("stream headers missing")
			}
			id := response.Header.Get("X-Request-Id")
			if id == "" {
				t.Fatal("request ID missing")
			}
			var status, phase string
			var charge, cost int64
			var customerHeld, costHeld bool
			var usageRaw []byte
			if err := f.db.DB.QueryRowContext(f.ctx, "SELECT status,phase,charge_micros,cost_micros,customer_hold_active,cost_hold_active,usage_json FROM requests WHERE id=? AND tenant_id=?", id, f.operator.TenantID).Scan(&status, &phase, &charge, &cost, &customerHeld, &costHeld, &usageRaw); err != nil {
				t.Fatal(err)
			}
			var usage domain.Usage
			if err := json.Unmarshal(usageRaw, &usage); err != nil {
				t.Fatal(err)
			}
			if status != "completed" || phase != "settled" || charge != tc.charge || cost != tc.cost || customerHeld || costHeld || usage != tc.usage {
				t.Fatalf("durable settlement incorrect: %s %s charge=%d cost=%d holds=%t/%t usage=%+v", status, phase, charge, cost, customerHeld, costHeld, usage)
			}
			var ledgerCount, attemptCount int
			var ledgerAmount, attemptCost int64
			if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*),COALESCE(SUM(amount_micros),0) FROM ledger_entries WHERE tenant_id=? AND reference=?", f.operator.TenantID, "request:"+id).Scan(&ledgerCount, &ledgerAmount); err != nil {
				t.Fatal(err)
			}
			if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*),COALESCE(SUM(cost_micros),0) FROM attempts WHERE request_id=? AND status='settled'", id).Scan(&attemptCount, &attemptCost); err != nil {
				t.Fatal(err)
			}
			if ledgerCount != 1 || ledgerAmount != -tc.charge || attemptCount != 1 || attemptCost != tc.cost {
				t.Fatalf("ledger/attempt accounting: entries=%d charge=%d attempts=%d cost=%d", ledgerCount, ledgerAmount, attemptCount, attemptCost)
			}
			// The console shows the client's own request and the exact bytes it
			// received, never the upstream model alias or any credential header.
			w := f.call(t, "GET", "/api/requests/"+id, f.operatorCookie, f.s.AdminAppURL, nil)
			expectStatus(t, w, 200)
			detail := decodeResponse[domain.RequestDetail](t, w)
			wantPath, wantModel := "/openai/v1/chat/completions", upstreams[0].model
			if tc.provider == "anthropic" {
				wantPath, wantModel = "/anthropic/v1/messages", upstreams[1].model
			}
			if detail.Payload == nil {
				t.Fatal("captured exchange missing")
			}
			sent, got := detail.Payload.Request, detail.Payload.Response
			if sent.Method != "POST" || sent.Path != wantPath || !strings.Contains(sent.Body, `"model":"`+wantModel+`"`) || !strings.Contains(sent.Body, `"probe":true`) || strings.Contains(sent.Body, "mock-"+tc.provider) || strings.Contains(sent.Body, "max_tokens") {
				t.Fatalf("captured request %+v", sent)
			}
			for name := range sent.Headers {
				if name != "content-type" && name != "anthropic-version" && name != "user-agent" {
					t.Fatalf("unexpected captured header %q", name)
				}
			}
			if got.Status != 200 || got.Body != tc.payload || got.Bytes != len(tc.payload) || got.Truncated || detail.Payload.Upstream != nil {
				t.Fatalf("captured response %+v", got)
			}
			// Replay the settlement acknowledgment as after a worker retry. It must
			// neither debit the customer nor add the supplier cost a second time.
			if err := f.db.Settle(f.ctx, id, domain.Settlement{Status: "completed", Usage: tc.usage, ChargeMicros: tc.charge, CostMicros: tc.cost}); err != nil {
				t.Fatal(err)
			}
			totalCharge += tc.charge
			totalCost += tc.cost
		})
	}
	var balance, held int64
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros,held_micros FROM wallets WHERE tenant_id=?", f.operator.TenantID).Scan(&balance, &held); err != nil {
		t.Fatal(err)
	}
	if totalCharge != 153 || totalCost != 77 || balance != initialBalance-153 || held != 0 {
		t.Fatalf("wallet conservation failed: charges=%d cost=%d balance=%d held=%d", totalCharge, totalCost, balance, held)
	}
	for i, u := range upstreams {
		var spent, costHeld int64
		if err := f.db.DB.QueryRowContext(f.ctx, "SELECT spent_micros,held_micros FROM credentials WHERE id=?", u.credential).Scan(&spent, &costHeld); err != nil {
			t.Fatal(err)
		}
		want := int64(33)
		if i == 1 {
			want = 44
		}
		if spent != want || costHeld != 0 || u.hits.Load() != 2 {
			t.Fatalf("%s spend=%d held=%d calls=%d", u.provider, spent, costHeld, u.hits.Load())
		}
	}
	var charges int
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM ledger_entries WHERE tenant_id=? AND kind='charge'", f.operator.TenantID).Scan(&charges); err != nil {
		t.Fatal(err)
	}
	if charges != 4 {
		t.Fatalf("duplicate/missing fees: %d", charges)
	}
	// A legacy second tenant is waitlisted. Its old key must never spend
	// another tenant's wallet or send a request to either mocked supplier.
	response, raw := request(cases[0], emptyAccess)
	if response.StatusCode != 401 {
		t.Fatalf("zero-balance request status=%d body=%s", response.StatusCode, raw)
	}
	if upstreams[0].hits.Load() != 2 || upstreams[1].hits.Load() != 2 {
		t.Fatal("zero-balance request reached upstream")
	}
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros,held_micros FROM wallets WHERE tenant_id=?", f.other.TenantID).Scan(&balance, &held); err != nil {
		t.Fatal(err)
	}
	if balance != 0 || held != 0 {
		t.Fatal("zero-balance rejection changed wallet")
	}
	var emptyRequests int
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM requests WHERE tenant_id=?", f.other.TenantID).Scan(&emptyRequests); err != nil {
		t.Fatal(err)
	}
	if emptyRequests != 0 {
		t.Fatal("rejected request left a durable reservation")
	}
}

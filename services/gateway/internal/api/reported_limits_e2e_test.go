package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/relay"
)

// The rate limits an upstream states in its response headers reach the
// administrator's view of the credential, and never a customer's.
func TestReportedRateLimitsReachOperators(t *testing.T) {
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
	f.s.Redis = redisClient
	f.s.AllowPrivate = true
	f.s.Relay = relay.New(isolatedE2ERepository{Store: f.db, prefix: f.prefix}, redisClient, true, true)
	f.h = f.s.Handler()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.db.DB.ExecContext(ctx, "DELETE a FROM attempts a JOIN requests r ON r.id=a.request_id WHERE r.tenant_id=?", f.operator.TenantID)
		_, _ = f.db.DB.ExecContext(ctx, "DELETE FROM request_payloads WHERE request_id IN (SELECT id FROM requests WHERE tenant_id=?)", f.operator.TenantID)
		_, _ = f.db.DB.ExecContext(ctx, "DELETE FROM requests WHERE tenant_id=?", f.operator.TenantID)
	})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Ratelimit-Limit-Requests", "10000")
		w.Header().Set("X-Ratelimit-Limit-Tokens", "4000000")
		_, _ = io.WriteString(w, `{"id":"chat_limits","object":"chat.completion","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)
	model, endpoint, credential := f.prefix+"m_limits", f.prefix+"e_limits", f.prefix+"c_limits"
	f.models, f.endpoints, f.credentials = append(f.models, model), append(f.endpoints, endpoint), append(f.credentials, credential)
	t.Cleanup(func() { redisClient.Del(context.Background(), "rp:reported-limits:"+credential) })
	admin := f.s.AdminAppURL
	expectStatus(t, f.call(t, "POST", "/api/admin/endpoints", f.operatorCookie, admin, domain.Endpoint{ID: endpoint, Name: "Limits mock", Provider: "openai", BaseURL: upstream.URL + "/v1", Enabled: true, Models: []string{model}}), 200)
	expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, admin, domain.Model{ID: model, Name: "Limits model", Provider: "openai", UpstreamModel: "limits-upstream", Enabled: true, MaxOutputTokens: 100,
		InputRate: 1_000_000, OutputRate: 2_000_000, CostInputRate: 1_000_000, CostOutputRate: 2_000_000}), 200)
	expectStatus(t, f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, admin, map[string]any{"id": credential, "name": "Limits mock", "endpoint_id": endpoint, "secret": "mock-limits-" + testID(), "quota_group": f.prefix + "limits", "enabled": true, "rpm": 100, "tpm": 1000000, "max_concurrency": 10}), 200)
	issued := f.call(t, "POST", "/api/keys", f.operatorCookie, admin, map[string]any{"name": "Limits", "rpm": 100, "models": []string{model}})
	expectStatus(t, issued, 201)
	access := decodeResponse[struct {
		Key    domain.AccessKey `json:"key"`
		Secret string           `json:"secret"`
	}](t, issued)
	t.Cleanup(func() {
		for _, scope := range []string{"tenant:" + f.operator.TenantID, "access:" + access.Key.ID, "credential:" + credential, "group:" + f.prefix + "limits"} {
			sum := sha256.Sum256([]byte(scope))
			keys, _ := redisClient.Keys(context.Background(), "relay:"+hex.EncodeToString(sum[:16])+":*").Result()
			if len(keys) > 0 {
				redisClient.Del(context.Background(), keys...)
			}
		}
	})
	expectStatus(t, f.call(t, "POST", "/api/admin/credits", f.operatorCookie, admin, map[string]any{"tenant_id": f.operator.TenantID, "amount_micros": 1_000_000, "reason": "Reported limits credit", "idempotency_key": f.prefix + "limits-funds"}), 200)
	gateway := httptest.NewServer(f.h)
	t.Cleanup(gateway.Close)
	req, _ := http.NewRequest("POST", gateway.URL+"/openai/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+access.Secret)
	req.Header.Set("Content-Type", "application/json")
	response, err := gateway.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("HTTP %d", response.StatusCode)
	}
	var reported []domain.ReportedLimit
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && len(reported) == 0; time.Sleep(50 * time.Millisecond) {
		reported = decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", f.operatorCookie, admin, nil)).ReportedLimits[credential]
	}
	if len(reported) != 1 || reported[0].Model != "limits-upstream" || reported[0].Requests != 10000 || reported[0].Tokens != 4000000 || time.Since(reported[0].ObservedAt) > time.Minute {
		t.Fatalf("reported limits %+v", reported)
	}
	_, customerCookie := f.verifiedCustomer(t, "limitscustomer")
	w := f.call(t, "GET", "/api/state", customerCookie, f.s.AppURL, nil)
	expectStatus(t, w, 200)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || raw["reported_limits"] != nil {
		t.Fatalf("customer saw reported limits: %v", err)
	}
}

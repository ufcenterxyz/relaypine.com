package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

const smallChatReply = `{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4}}}`

// A settled request holds exactly the tokens it used, in every bucket it was
// admitted to: 8 uncached and 4 cached input tokens plus 3 output tokens.
func TestTokenEstimateSettlesToUsage(t *testing.T) {
	up := newImageUpstream(t, "application/json", smallChatReply)
	h, _ := fixture("openai", up.server.URL)
	l := &fakeLimiter{}
	h.limiter = l
	w := serveRequest(h, newRequest("/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"ping"}]}`))
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	for scope, tokens := range l.netTokens() {
		if tokens != 15 {
			t.Errorf("%s holds %d tokens after settlement, want 15", scope, tokens)
		}
	}
	// Tenant, access key and credential; this credential has no quota group.
	if len(l.charged) != 3 {
		t.Fatalf("buckets charged %v", l.charged)
	}
}

// A key that refuses the request runs no inference, so the route returns its
// whole estimate; the request's shared buckets settle to the tokens used.
func TestRefusedRouteReturnsItsTokenEstimate(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Header.Get("Authorization") == "Bearer upstream-secret" {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, smallChatReply)
	}))
	t.Cleanup(up.Close)
	h, f := fixture("openai", up.URL)
	f.credentials = append(f.credentials, domain.Credential{ID: "second", EndpointID: "endpoint", Secret: "second-secret", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5, BudgetMicros: 100000000, QuotaGroup: "other"})
	l := &fakeLimiter{}
	h.limiter = l
	w := serveRequest(h, newRequest("/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"ping"}]}`))
	if w.Code != 200 || len(f.reroutes) != 1 {
		t.Fatalf("HTTP %d reroutes %+v", w.Code, f.reroutes)
	}
	net := l.netTokens()
	if net["credential:credential"] != 0 || net["credential:second"] != 15 || net["tenant:tenant"] != 15 || net["access:access"] != 15 {
		t.Fatalf("tokens after settlement %v", net)
	}
}

// A stream cut before its usage keeps the estimate, which is an upper bound.
func TestUnknownUsageKeepsTheTokenEstimate(t *testing.T) {
	up := newImageUpstream(t, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"po\"}}]}\n\n")
	h, _ := fixture("openai", up.server.URL)
	l := &fakeLimiter{}
	h.limiter = l
	serveRequest(h, newRequest("/openai/v1/chat/completions", `{"model":"model","stream":true,"messages":[{"role":"user","content":"ping"}]}`))
	if len(l.adjusted) != 0 || len(l.charged) == 0 {
		t.Fatalf("charged %v adjusted %v", l.charged, l.adjusted)
	}
}

// A route whose money reservation fails returns the tokens it was charged.
func TestFailedReservationReturnsItsTokens(t *testing.T) {
	up := newImageUpstream(t, "application/json", smallChatReply)
	h, f := fixture("openai", up.server.URL)
	f.reserveError = store.ErrBudgetExceeded
	l := &fakeLimiter{}
	h.limiter = l
	w := serveRequest(h, newRequest("/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"ping"}]}`))
	if w.Code != 402 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	for scope, tokens := range l.netTokens() {
		if tokens != 0 {
			t.Errorf("%s still holds %d tokens", scope, tokens)
		}
	}
}

func redisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	address := os.Getenv("REDIS_TEST_ADDR")
	if address == "" {
		t.Skip("set REDIS_TEST_ADDR for Redis Lua integration")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestRedisAdjustSettlesTokens(t *testing.T) {
	client := redisTestClient(t)
	ctx := context.Background()
	l := &redisLimiter{client: client}
	bucket := limits{scope: "test:" + requestID() + ":tokens", rpm: 100, tpm: 1500, concurrency: 10}
	t.Cleanup(func() { client.Del(ctx, bucketKeys(bucket.scope, time.Now())...) })
	w, err := l.acquire(ctx, requestID(), []limits{bucket}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.acquire(ctx, requestID(), []limits{bucket}, 1000); err != errCapacity {
		t.Fatalf("second estimate admitted: %v", err)
	}
	// The first request used 100 tokens.
	if err = l.adjust(ctx, []limits{bucket}, w, -900); err != nil {
		t.Fatal(err)
	}
	if _, err = l.acquire(ctx, requestID(), []limits{bucket}, 1000); err != nil {
		t.Fatalf("estimate refused after settlement: %v", err)
	}
	if n, _ := client.Get(ctx, tpmKey(bucket.scope, w)).Int64(); n != 1100 && window(time.Now()) == w {
		t.Fatalf("window holds %d tokens, want 1100", n)
	}
	if err = l.adjust(ctx, []limits{bucket}, w, -1_000_000); err != nil {
		t.Fatal(err)
	}
	if n, _ := client.Get(ctx, tpmKey(bucket.scope, w)).Int64(); n != 0 {
		t.Fatalf("refund went below zero: %d", n)
	}
	if ttl := client.TTL(ctx, tpmKey(bucket.scope, w)).Val(); ttl <= 0 {
		t.Fatalf("adjustment dropped the expiry: %v", ttl)
	}
	// An expired window is left alone.
	if err = l.adjust(ctx, []limits{bucket}, w-5, 500); err != nil {
		t.Fatal(err)
	}
	if n := client.Exists(ctx, tpmKey(bucket.scope, w-5)).Val(); n != 0 {
		t.Fatal("adjustment created an expired window")
	}
}

// Requests that name no output limit reserve the model's whole output cap.
// Once settled they hold only the tokens used, so a 100,000 TPM key keeps
// admitting them: before, the second request in the minute was refused.
func TestRequestsWithoutOutputLimitKeepAdmitting(t *testing.T) {
	client := redisTestClient(t)
	up := newImageUpstream(t, "application/json", smallChatReply)
	h, f := fixture("openai", up.server.URL)
	run := requestID()
	f.key.ID, f.key.TenantID = "access-"+run, "tenant-"+run
	f.credentials[0].ID, f.credentials[0].QuotaGroup = "credential-"+run, "group-"+run
	f.models[0].MaxOutputTokens = 60000
	h.limiter = &redisLimiter{client: client}
	t.Cleanup(func() {
		for _, scope := range []string{"tenant:" + f.key.TenantID, "access:" + f.key.ID, "credential:" + f.credentials[0].ID, "group:" + f.credentials[0].QuotaGroup} {
			client.Del(context.Background(), bucketKeys(scope, time.Now())...)
		}
	})
	start := window(time.Now())
	for i := 0; i < 3; i++ {
		w := serveRequest(h, newRequest("/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"ping"}]}`))
		if w.Code != 200 {
			t.Fatalf("request %d: HTTP %d %s", i+1, w.Code, w.Body.String())
		}
	}
	if window(time.Now()) != start {
		t.Skip("the minute changed during the test; counts span two windows")
	}
	if n, _ := client.Get(context.Background(), tpmKey("credential:"+f.credentials[0].ID, start)).Int64(); n != 45 {
		t.Fatalf("key holds %d tokens after three settled requests, want 45", n)
	}
}

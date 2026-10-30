package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

func TestRetryAfterRoundsUp(t *testing.T) {
	for _, tc := range []struct {
		wait time.Duration
		want int64
	}{{0, 1}, {900 * time.Millisecond, 1}, {time.Second, 1}, {22500 * time.Millisecond, 23}, {time.Minute, 60}} {
		if got := retryAfter(tc.wait); got != tc.want {
			t.Errorf("retryAfter(%v) = %d, want %d", tc.wait, got, tc.want)
		}
	}
}

func refusingUpstream(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a refused request reached the upstream")
	}))
	t.Cleanup(server.Close)
	return server
}

func alternateCredential() domain.Credential {
	return domain.Credential{ID: "alternate", EndpointID: "endpoint", Secret: "second", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5, BudgetMicros: 100000000}
}

// A workspace or API key limit refuses the request on every route, so the
// reply names that limit instead of upstream capacity, and no other route is
// tried.
func TestOwnLimitsExplainTheRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, scope    string
		refusal        capacityError
		message, retry string
	}{
		{"workspace concurrency", "tenant:tenant", capacityError{limit: "concurrency"}, "This workspace already has 10 requests in progress, its concurrency limit. Retry when one finishes.", "1"},
		{"workspace rpm", "tenant:tenant", capacityError{limit: "rpm", wait: 900 * time.Millisecond}, "This workspace has reached its limit of 120 requests per minute. Retry in 1 second.", "1"},
		{"api key rpm", "access:access", capacityError{limit: "rpm", wait: 22500 * time.Millisecond}, "This API key has reached its limit of 60 requests per minute. Retry in 23 seconds. A key with a higher limit can be created in the console.", "23"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f := fixture("openai", refusingUpstream(t).URL)
			f.routing = domain.RouteConfig{Strategy: "least_load", TenantRPM: 120, TenantConcurrency: 10}
			f.credentials = append(f.credentials, alternateCredential())
			l := &fakeLimiter{refusals: map[string]capacityError{tc.scope: tc.refusal}}
			h.limiter = l
			w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
			var body struct {
				Error struct{ Code, Message string }
			}
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != 429 || body.Error.Code != "rate_limit_exceeded" || body.Error.Message != tc.message || w.Header().Get("Retry-After") != tc.retry {
				t.Fatalf("%d %s Retry-After %q", w.Code, w.Body, w.Header().Get("Retry-After"))
			}
			if l.attempts != 1 || len(f.reservations) != 0 {
				t.Fatalf("tried %d routes and reserved %d after the customer's own limit refused", l.attempts, len(f.reservations))
			}
		})
	}
}

// When every upstream route is full, the client may retry as soon as the
// first of them frees.
func TestUpstreamRefusalRetriesWhenARouteFrees(t *testing.T) {
	h, f := fixture("openai", refusingUpstream(t).URL)
	f.credentials = append(f.credentials, alternateCredential())
	l := &fakeLimiter{refusals: map[string]capacityError{
		"credential:credential":                     {limit: "rpm", wait: 40 * time.Second},
		"credential:alternate|model:upstream-model": {limit: "cooldown", wait: 19200 * time.Millisecond},
	}}
	h.limiter = l
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "All eligible upstream capacity is in use") || w.Header().Get("Retry-After") != "20" || l.attempts != 2 {
		t.Fatalf("%d %s Retry-After %q after %d attempts", w.Code, w.Body, w.Header().Get("Retry-After"), l.attempts)
	}
	// A concurrency slot may free at any moment.
	l.refusals["credential:credential"] = capacityError{limit: "concurrency"}
	w = serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("%d Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
}

// In Redis, a refusal names the bucket that refused, the limit it hit and how
// long until it admits again.
func TestRedisRefusalNamesBucketAndLimit(t *testing.T) {
	client := redisTestClient(t)
	ctx := context.Background()
	l := &redisLimiter{client: client}
	// Counts reset each minute, so start well inside one.
	if s := time.Now().Second(); s >= 50 {
		time.Sleep(time.Duration(61-s) * time.Second)
	}
	prefix := "test:" + requestID()
	open := limits{scope: prefix + ":open", rpm: 100, tpm: 1000, concurrency: 10}
	concurrency := limits{scope: prefix + ":concurrency", concurrency: 1}
	rpm := limits{scope: prefix + ":rpm", rpm: 1}
	tpm := limits{scope: prefix + ":tpm", tpm: 10}
	cooling := limits{scope: prefix + ":cooling"}
	t.Cleanup(func() {
		for _, b := range []limits{open, concurrency, rpm, tpm, cooling} {
			client.Del(ctx, bucketKeys(b.scope, time.Now())...)
		}
	})
	for _, b := range []limits{concurrency, rpm, tpm} {
		if _, err := l.acquire(ctx, requestID(), []limits{b}, 10); err != nil {
			t.Fatalf("%s refused its first request: %v", b.scope, err)
		}
	}
	if err := l.cool(ctx, []limits{cooling}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		bucket   limits
		limit    string
		min, max time.Duration
	}{
		{concurrency, "concurrency", 0, 0},
		{rpm, "rpm", time.Second, time.Minute},
		{tpm, "tpm", time.Second, time.Minute},
		{cooling, "cooldown", 29 * time.Second, 30 * time.Second},
	} {
		// The refusing bucket is named even behind one with room.
		_, err := l.acquire(ctx, requestID(), []limits{open, tc.bucket}, 1)
		full := refusal(err)
		if !errors.Is(err, errCapacity) || full.bucket != tc.bucket || full.limit != tc.limit || full.wait < tc.min || full.wait > tc.max {
			t.Errorf("%s: %v bucket %q limit %q wait %v", tc.limit, err, full.bucket.scope, full.limit, full.wait)
		}
	}
	if n, _ := client.Get(ctx, bucketKeys(open.scope, time.Now())[0]).Int64(); n != 0 {
		t.Fatalf("refused requests counted %d against the open bucket", n)
	}
}

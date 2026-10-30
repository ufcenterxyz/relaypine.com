package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimitKindReadsProviderErrors(t *testing.T) {
	for _, c := range []struct {
		provider, body string
		want           limitKind
	}{
		{"openai", `{"error":{"message":"Rate limit reached for gpt-image-2 in organization org-x on images per min","type":"requests","param":null,"code":"rate_limit_exceeded"}}`, limitModel},
		{"openai", `{"error":{"message":"Your request rate increased too quickly.","type":"rate_limit_error","code":"slow_down"}}`, limitModel},
		{"openai", `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`, limitAccount},
		{"openai", `{"error":{"message":"No prepaid credits remain","type":"insufficient_quota","code":"credit_balance_exhausted"}}`, limitAccount},
		{"openai", `{"error":{"message":"Spend limit","type":"insufficient_quota","code":"project_spend_limit_exceeded"}}`, limitAccount},
		{"openai", `{"error":{"message":"Spend limit","code":"organization_spend_limit_exceeded"}}`, limitAccount},
		{"openai", `{"error":{"message":"Usage limit","code":"organization_usage_limit_exceeded"}}`, limitAccount},
		{"anthropic", `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the rate limit for your organization of 500,000 input tokens per minute."}}`, limitModel},
		{"anthropic", `{"type":"error","error":{"type":"rate_limit_error","message":"You have reached your API usage limits","details":{"error_code":"enforced_spend_limit_reached"}}}`, limitAccount},
		// Anything else keeps pausing the whole key.
		{"openai", ``, limitUnknown},
		{"openai", `{"error":{"message":"Rate limit exceeded","code":429}}`, limitUnknown},
		{"anthropic", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, limitUnknown},
		{"anthropic", `{"type":"error","error":{"type":"rate_limit_error","details":{"error_code":"something_new"}}}`, limitUnknown},
	} {
		if got := rateLimitKind(c.provider, []byte(c.body)); got != c.want {
			t.Errorf("%s %.70s: %d, want %d", c.provider, c.body, got, c.want)
		}
	}
}

// A model's rate limit pauses that model on the key and its quota group; an
// account-wide refusal pauses the whole key for at least five minutes.
func TestRateLimitedModelsPauseAlone(t *testing.T) {
	for _, c := range []struct {
		name, provider, path, body, retryAfter string
		cooled                                 map[string]time.Duration
	}{
		{"openai model", "openai", "/openai/v1/chat/completions", `{"error":{"message":"Rate limit reached for upstream-model","type":"tokens","code":"rate_limit_exceeded"}}`, "7",
			map[string]time.Duration{"credential:credential|model:upstream-model": 7 * time.Second, "group:org|model:upstream-model": 7 * time.Second}},
		{"openai quota", "openai", "/openai/v1/chat/completions", `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`, "",
			map[string]time.Duration{"credential:credential": 5 * time.Minute, "group:org": 5 * time.Minute}},
		{"anthropic model class", "anthropic", "/anthropic/v1/messages", `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your rate limit."}}`, "9",
			map[string]time.Duration{"credential:credential|model:upstream-model": 9 * time.Second, "group:org|model:upstream-model": 9 * time.Second}},
		{"anthropic spend cap", "anthropic", "/anthropic/v1/messages", `{"type":"error","error":{"type":"rate_limit_error","message":"You have reached your API usage limits","details":{"error_code":"enforced_spend_limit_reached"}}}`, "",
			map[string]time.Duration{"credential:credential": 5 * time.Minute, "group:org": 5 * time.Minute}},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				if c.retryAfter != "" {
					w.Header().Set("Retry-After", c.retryAfter)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				_, _ = io.WriteString(w, c.body)
			}))
			defer server.Close()
			h, f := fixture(c.provider, server.URL)
			f.credentials[0].QuotaGroup = "org"
			l := &fakeLimiter{}
			h.limiter = l
			body := `{"model":"model","messages":[{"role":"user","content":"hi"}]}`
			if w := serve(h, c.path, body); w.Code != 429 {
				t.Fatalf("HTTP %d %s", w.Code, w.Body)
			}
			if len(l.cooled) != len(c.cooled) {
				t.Fatalf("cooled %v, want %v", l.cooled, c.cooled)
			}
			for scope, d := range c.cooled {
				if l.cooled[scope] != d {
					t.Fatalf("cooled %v, want %v", l.cooled, c.cooled)
				}
			}
		})
	}
}

// In Redis, a paused model refuses its own requests while the key keeps
// serving other models.
func TestRedisPausedModelLeavesOtherModels(t *testing.T) {
	client := redisTestClient(t)
	ctx := context.Background()
	l := &redisLimiter{client: client}
	key := "credential:" + requestID()
	busy, free := limits{scope: modelScope(key, "gpt-image-2")}, limits{scope: modelScope(key, "gpt-5.5")}
	credential := limits{scope: key, rpm: 100, tpm: 1_000_000, concurrency: 10}
	t.Cleanup(func() {
		for _, b := range []limits{busy, free, credential} {
			client.Del(ctx, bucketKeys(b.scope, time.Now())...)
		}
	})
	if err := l.cool(ctx, []limits{busy}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := l.acquire(ctx, requestID(), []limits{credential, busy}, 10); err != errCapacity {
		t.Fatalf("a paused model was admitted: %v", err)
	}
	if score, err := l.load(ctx, []limits{credential, busy}); err != nil || score < 1 {
		t.Fatalf("a paused model scored %v %v", score, err)
	}
	if _, err := l.acquire(ctx, requestID(), []limits{credential, free}, 10); err != nil {
		t.Fatalf("another model on the same key was refused: %v", err)
	}
}

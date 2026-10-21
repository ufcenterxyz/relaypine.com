package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

// Usage-only snapshots remain readable for historical settlement, but they
// cannot authorize a new uncharged request after reference pricing is enabled.
func TestUnpricedLegacyModelsFailBeforeReservationOrUpstream(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		for _, stream := range []bool{false, true} {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			h, f := fixture(provider, upstream.URL)
			f.models[0].BillingMode = domain.BillingUsageOnly
			f.models[0].InputRate, f.models[0].OutputRate, f.models[0].CachedRate, f.models[0].CacheWriteRate = 0, 0, 0, 0
			f.models[0].CostInputRate, f.models[0].CostOutputRate, f.models[0].CostCachedRate, f.models[0].CostCacheWriteRate = 0, 0, 0, 0
			path := "/openai/v1/chat/completions"
			if provider == "anthropic" {
				path = "/anthropic/v1/messages"
			}
			body := `{"model":"model","messages":[]}`
			if stream {
				body = `{"model":"model","messages":[],"stream":true}`
			}
			w := serve(h, path, body)
			if w.Code != 400 || calls.Load() != 0 || len(f.reservations) != 0 {
				t.Fatalf("unpriced request admitted: %d %s", w.Code, w.Body)
			}
			r := httptest.NewRequest("GET", "/"+provider+"/v1/models", nil)
			r.Header.Set("Authorization", "Bearer rp_test")
			list := httptest.NewRecorder()
			h.ServeHTTP(list, r)
			if list.Code != 200 || strings.Contains(list.Body.String(), `"id":"model"`) {
				t.Fatal("unpriced model advertised as usable")
			}
			charge, cost, err := charges(domain.Usage{InputTokens: 100, OutputTokens: 10, Known: true}, f.models[0])
			if err != nil || charge != 0 || cost != 0 {
				t.Fatal("historical usage-only charge changed")
			}
			upstream.Close()
		}
	}
}

func TestOpenAICacheWritesUseSeparateBilledTokens(t *testing.T) {
	// https://developers.openai.com/api/docs/guides/prompt-caching describes
	// ordinary input = total input - cached reads - cache writes. 30m writes
	// replace ordinary input at 1.25x; they are not an extra surcharge.
	raw := []byte(`{"input_tokens":12000,"output_tokens":100,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000,"cache_write_tokens_details":{"ttl_30m_tokens":7000,"ttl_12h_tokens":0}}}`)
	u, ok := parseUsage(raw, "openai")
	if !ok || u.InputTokens != 1000 || u.CachedTokens != 4000 || u.CacheWriteTokens != 7000 {
		t.Fatalf("overlapping token categories: %+v", u)
	}
	m := domain.Model{BillingMode: domain.BillingMetered, InputRate: 1_000_000, OutputRate: 5_000_000, CachedRate: 100_000, CacheWriteRate: 1_250_000, CostInputRate: 1_000_000, CostOutputRate: 5_000_000, CostCachedRate: 100_000, CostCacheWriteRate: 1_250_000}
	charge, cost, err := charges(u, m)
	if err != nil || charge != 10650 || cost != 10650 {
		t.Fatalf("cache write charge=%d cost=%d err=%v", charge, cost, err)
	}
	for _, bad := range []string{
		`{"input_tokens":3,"output_tokens":1,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":2}}`,
		`{"input_tokens":12000,"output_tokens":100,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000,"cache_write_tokens_details":{"ttl_30m_tokens":3000,"ttl_12h_tokens":4000}}}`,
	} {
		if _, ok := parseUsage([]byte(bad), "openai"); ok {
			t.Fatal("unsupported cache usage accepted")
		}
	}
	for _, body := range []string{`{"prompt_cache_options":{"ttl":"12h"}}`, `{"messages":[{"role":"user","content":[{"type":"input_text","text":"test","prompt_cache_breakpoint":{"ttl":"12h"}}]}]}`} {
		if validatePayload(object([]byte(body)), "/v1/responses") == nil {
			t.Fatal("premium cache request accepted")
		}
	}
}

func TestReferencePricesRequireOfficialRoutesAndBoundContext(t *testing.T) {
	h, f := fixture("anthropic", "https://compatible.example")
	f.models[0].PricingReference = &domain.PriceReference{Provider: "anthropic", MaxInputTokens: 100000, Scope: "standard_text"}
	p := plan{provider: "anthropic", model: f.models[0], key: f.key, path: "/v1/messages"}
	candidates, err := h.candidates(httptest.NewRequest("GET", "/", nil).Context(), p, domain.RouteConfig{Strategy: "least_load"})
	if err != nil || len(candidates) != 0 {
		t.Fatal("official reference cost was used for compatible host")
	}
	body := `{"model":"model","messages":[{"role":"user","content":"` + strings.Repeat("x", 50000) + `"}]}`
	w := serve(h, "/anthropic/v1/messages", body)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "context limit") {
		t.Fatalf("reference tier bound bypassed %d %s", w.Code, w.Body)
	}
}

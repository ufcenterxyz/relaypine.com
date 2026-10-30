package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

// longContext doubles the fixture model's rates above 1,000 prompt tokens,
// with output at 1.5 times, the way OpenAI prices prompts over 272K tokens.
func longContext() *domain.LongContext {
	return &domain.LongContext{AboveInputTokens: 1000,
		InputRate: 2000000, OutputRate: 3000000, CachedRate: 1000000, CacheWriteRate: 2500000,
		CostInputRate: 1000000, CostOutputRate: 1500000, CostCachedRate: 500000, CostCacheWriteRate: 1250000}
}

// The prompt length, cache reads and writes included, picks the rates for the
// whole request. A prompt exactly at the threshold keeps the standard rates.
func TestLongPromptsPayLongContextRates(t *testing.T) {
	for _, tc := range []struct {
		name, provider, path, usage string
		charge, cost                int64
	}{
		{"openai at threshold", "openai", "/openai/v1/chat/completions",
			`{"prompt_tokens":1000,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":200}}`, 920, 460},
		{"openai above threshold", "openai", "/openai/v1/chat/completions",
			`{"prompt_tokens":1001,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":200}}`, 1832, 916},
		{"responses above threshold", "openai", "/openai/v1/responses",
			`{"input_tokens":1001,"output_tokens":10,"input_tokens_details":{"cached_tokens":200}}`, 1832, 916},
		// 400 input, 500 cache reads and 101 cache writes make a 1,001 token prompt.
		{"claude above threshold", "anthropic", "/anthropic/v1/messages",
			`{"input_tokens":400,"cache_read_input_tokens":500,"cache_creation_input_tokens":101,"output_tokens":10}`, 1583, 792},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				if tc.provider == "anthropic" {
					_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","usage":`+tc.usage+`}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","usage":`+tc.usage+`}`)
			}))
			defer server.Close()
			h, f := fixture(tc.provider, server.URL)
			f.models[0].LongContext = longContext()
			body := `{"model":"model","messages":[{"role":"user","content":"hello"}]}`
			if strings.HasSuffix(tc.path, "/responses") {
				body = `{"model":"model","input":"hello","store":false}`
			}
			w := serve(h, tc.path, body)
			if w.Code != 200 || len(f.settlements) != 1 {
				t.Fatalf("response %d %s %+v", w.Code, w.Body, f.settlements)
			}
			s := f.settlements[0]
			if s.ChargeMicros != tc.charge || s.CostMicros != tc.cost {
				t.Fatalf("charge %d cost %d, want %d and %d", s.ChargeMicros, s.CostMicros, tc.charge, tc.cost)
			}
			// The body's input bound is above the threshold, so the hold is
			// taken at the long context rates and covers either tier.
			input, _ := estimatedInputTokens(len(body), imageInputSummary{})
			reserved := f.reservations[0].Request.ReservedMicros
			if want, _, _ := hold(input, 128, f.models[0].PricedFor(input)); reserved != want || want < s.ChargeMicros {
				t.Fatalf("hold %d, want %d covering %d", reserved, want, s.ChargeMicros)
			}
		})
	}
}

func TestLongContextHoldCoversEitherTier(t *testing.T) {
	_, f := fixture("openai", "https://api.openai.com/v1")
	m := f.models[0]
	m.LongContext = longContext()
	standardSell, standardCost, _ := hold(1000, 100, m)
	if sell, cost, err := estimate(1000, 100, m); err != nil || sell != standardSell || cost != standardCost {
		t.Fatalf("a bound at the threshold took %d/%d, want the standard %d/%d", sell, cost, standardSell, standardCost)
	}
	longSell, longCost, _ := hold(1001, 100, m.PricedFor(1001))
	if sell, cost, err := estimate(1001, 100, m); err != nil || sell != longSell || cost != longCost || longSell <= standardSell {
		t.Fatalf("a bound above the threshold took %d/%d, want the long %d/%d", sell, cost, longSell, longCost)
	}
	// Even long rates below the standard ones, which validation refuses,
	// would not shrink the hold below the standard tier.
	m.LongContext = &domain.LongContext{AboveInputTokens: 1000, InputRate: 1, OutputRate: 1, CostInputRate: 1, CostOutputRate: 1}
	short, _, _ := hold(1001, 100, m)
	if sell, _, _ := estimate(1001, 100, m); sell != short {
		t.Fatalf("hold %d below the standard tier %d", sell, short)
	}
	if domain.CheckLongContext(m) == nil {
		t.Fatal("long context rates below the standard ones were accepted")
	}
}

// A verified standard price refuses prompts that may exceed it only when the
// provider charges more for them and the model has no long context price.
func TestLongPromptsNeedALongContextPriceOrNone(t *testing.T) {
	body := `{"model":"model","messages":[{"role":"user","content":"` + strings.Repeat("x", 60000) + `"}]}`
	for _, tc := range []struct {
		name    string
		ref     domain.PriceReference
		long    *domain.LongContext
		refused bool
	}{
		{"reference saved before long context prices", domain.PriceReference{MaxInputTokens: 100000}, nil, true},
		{"higher price not published as rates", domain.PriceReference{MaxInputTokens: 100000, AllPromptLengths: false}, nil, true},
		{"one price for every prompt length", domain.PriceReference{MaxInputTokens: 100000, AllPromptLengths: true}, nil, false},
		{"long context price", domain.PriceReference{MaxInputTokens: 100000}, &domain.LongContext{AboveInputTokens: 100000,
			InputRate: 2000000, OutputRate: 4000000, CachedRate: 500000, CacheWriteRate: 1250000,
			CostInputRate: 1000000, CostOutputRate: 2000000, CostCachedRate: 250000, CostCacheWriteRate: 625000}, false},
	} {
		h, f := fixture("anthropic", "https://api.anthropic.com")
		ref := tc.ref
		f.models[0].PricingReference = &ref
		f.models[0].LongContext = tc.long
		p := plan{provider: "anthropic", key: f.key, path: "/v1/messages"}
		err := h.prepare(httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(body)), &p, f.models)
		if refused := err != nil && strings.Contains(err.Error(), "context limit"); refused != tc.refused {
			t.Fatalf("%s: err %v", tc.name, err)
		}
		if !tc.refused && (err != nil || p.input <= ref.MaxInputTokens) {
			t.Fatalf("%s: the input bound %d should exceed the reference's %d: %v", tc.name, p.input, ref.MaxInputTokens, err)
		}
	}
}

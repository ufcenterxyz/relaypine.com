package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

func usageOnlyModel(m domain.Model) domain.Model {
	m.BillingMode = domain.BillingUsageOnly
	m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate = 0, 0, 0, 0
	m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate = 0, 0, 0, 0
	return m
}

type observingLimiter struct {
	*fakeLimiter
	tokens  int64
	buckets []limits
}

func (l *observingLimiter) acquire(ctx context.Context, id string, buckets []limits, tokens int64) error {
	l.tokens, l.buckets = tokens, buckets
	return l.fakeLimiter.acquire(ctx, id, buckets, tokens)
}

func TestUsageOnlyNativeForwardingTracksUsageWithoutPricing(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		for _, known := range []bool{false, true} {
			t.Run(provider+map[bool]string{true: "-known", false: "-unknown"}[known], func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					payload := object(body)
					if string(payload["max_tokens"]) != "64" || str(payload["model"]) != "upstream-model" {
						t.Errorf("output policy or alias changed: %s", body)
					}
					if provider == "anthropic" && r.Header.Get("Anthropic-Workspace-Id") != "wrkspc_Test" {
						t.Error("workspace lost")
					}
					if !known {
						_, _ = io.WriteString(w, `{"id":"message","content":[]}`)
						return
					}
					if provider == "openai" {
						_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":12,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":2}}}`)
					} else {
						_, _ = io.WriteString(w, `{"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}`)
					}
				}))
				defer upstream.Close()
				h, f := fixture(provider, upstream.URL)
				f.models[0] = usageOnlyModel(f.models[0])
				f.credentials[0].BudgetMicros = 0
				f.credentials[0].QuotaGroup = "shared"
				f.credentials[0].WorkspaceID = "wrkspc_Test"
				l := &observingLimiter{fakeLimiter: &fakeLimiter{}}
				h.limiter = l
				path := "/openai/v1/chat/completions"
				if provider == "anthropic" {
					path = "/anthropic/v1/messages"
				}
				w := serve(h, path, `{"model":"model","messages":[],"max_tokens":64}`)
				if w.Code != 200 || len(f.reservations) != 1 || len(f.settlements) != 1 {
					t.Fatalf("request failed %d %s", w.Code, w.Body)
				}
				reservation := f.reservations[0]
				settlement := f.settlements[0]
				if reservation.Request.ReservedMicros != 0 || reservation.CostReserve != 0 || reservation.Model.EffectiveBillingMode() != domain.BillingUsageOnly {
					t.Fatalf("money reserved: %+v", reservation)
				}
				if settlement.Usage.Known != known || settlement.Status != "completed" || settlement.ChargeMicros != 0 || settlement.CostMicros != 0 {
					t.Fatalf("invalid usage-only outcome: %+v", settlement)
				}
				if known && (settlement.Usage.InputTokens != 10 || settlement.Usage.OutputTokens != 4 || settlement.Usage.CachedTokens != 2) {
					t.Fatalf("token evidence changed: %+v", settlement.Usage)
				}
				if l.tokens <= 64 || len(l.buckets) != 4 || l.acquired != 1 || l.released != 1 {
					t.Fatalf("capacity bypassed: %+v", l)
				}
				if l.buckets[2].tpm != 100000 || l.buckets[3].scope != "group:shared" {
					t.Fatal("credential/shared capacity lost")
				}
			})
		}
	}
}

func TestUsageOnlyStreamingPreservesKnownAndUnknownTotals(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		for _, outcome := range []string{"terminal", "interrupted", "terminal disconnect", "early disconnect"} {
			t.Run(provider+"/"+outcome, func(t *testing.T) {
				path := "/openai/v1/chat/completions"
				events := "data: {\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":3}}\n\n"
				terminal := "[DONE]"
				terminalEvent := "data: [DONE]\n\n"
				if provider == "anthropic" {
					path = "/anthropic/v1/messages"
					events = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":8,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n"
					terminal = "message_stop"
					terminalEvent = "data: {\"type\":\"message_stop\"}\n\n"
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, events)
					if outcome != "interrupted" {
						_, _ = io.WriteString(w, terminalEvent)
					}
				}))
				defer upstream.Close()
				h, f := fixture(provider, upstream.URL)
				f.models[0] = usageOnlyModel(f.models[0])
				f.credentials[0].BudgetMicros = 0
				r := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"model","messages":[],"stream":true}`))
				r.Header.Set("Authorization", "Bearer rp_test")
				var w http.ResponseWriter = httptest.NewRecorder()
				if outcome == "terminal disconnect" {
					w = &terminalDisconnectWriter{ResponseRecorder: httptest.NewRecorder(), terminal: terminal}
				} else if outcome == "early disconnect" {
					w = &disconnectedWriter{header: http.Header{}}
				}
				h.ServeHTTP(w, r)
				if len(f.reservations) != 1 || len(f.settlements) != 1 {
					t.Fatal("stream missing durable admission or settlement")
				}
				v := f.settlements[0]
				known := outcome == "terminal" || outcome == "terminal disconnect"
				if v.Usage.Known != known || v.ChargeMicros != 0 || v.CostMicros != 0 {
					t.Fatalf("unreliable partial usage or pricing: %+v", v)
				}
				if known && (v.Usage.InputTokens != 8 || v.Usage.OutputTokens != 3) {
					t.Fatalf("verified usage was lost: %+v", v)
				}
				status := "completed"
				if outcome == "interrupted" {
					status = "failed"
				} else if strings.Contains(outcome, "disconnect") {
					status = "canceled"
				}
				if v.Status != status {
					t.Fatalf("execution status lost: %+v", v)
				}
			})
		}
	}
}

func TestUsageOnlyRetainsAuthenticationModelAndCapacityPolicies(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Handler, *fakeRepo)
		body   string
		want   int
	}{
		{"disabled access key", func(_ *Handler, f *fakeRepo) { f.key.Enabled = false }, `{"model":"model","messages":[]}`, 401},
		{"model permission", func(_ *Handler, f *fakeRepo) { f.key.Models = []string{"other"} }, `{"model":"model","messages":[]}`, 400},
		{"disabled model", func(_ *Handler, f *fakeRepo) { f.models[0].Enabled = false }, `{"model":"model","messages":[]}`, 400},
		{"output cap", func(_ *Handler, _ *fakeRepo) {}, `{"model":"model","messages":[],"max_tokens":129}`, 400},
		{"shared capacity", func(h *Handler, f *fakeRepo) {
			f.credentials[0].QuotaGroup = "shared"
			h.limiter = &fakeLimiter{denied: map[string]bool{"group:shared": true}}
		}, `{"model":"model","messages":[]}`, 429},
		{"missing priced rates remain invalid", func(_ *Handler, f *fakeRepo) { f.models[0].BillingMode = "" }, `{"model":"model","messages":[]}`, 400},
		{"unknown mode", func(_ *Handler, f *fakeRepo) { f.models[0].BillingMode = "free" }, `{"model":"model","messages":[]}`, 400},
		{"usage mode rejects misleading price", func(_ *Handler, f *fakeRepo) { f.models[0].CostInputRate = 1 }, `{"model":"model","messages":[]}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			h, f := fixture("openai", upstream.URL)
			f.models[0] = usageOnlyModel(f.models[0])
			f.credentials[0].BudgetMicros = 0
			tc.mutate(h, f)
			w := serve(h, "/openai/v1/chat/completions", tc.body)
			if w.Code != tc.want || calls.Load() != 0 || len(f.reservations) != 0 {
				t.Fatalf("policy bypassed: %d %s calls=%d", w.Code, w.Body, calls.Load())
			}
		})
	}
}

func TestUsageOnlyMonetaryBudgetEligibilityAndRace(t *testing.T) {
	for _, scenario := range []string{"configured budget", "budget changed after routing", "race retries zero-budget key"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
			}))
			defer upstream.Close()
			h, f := fixture("openai", upstream.URL)
			f.models[0] = usageOnlyModel(f.models[0])
			f.credentials[0].BudgetMicros = 0
			if scenario == "configured budget" {
				f.credentials[0].BudgetMicros = 100
			} else {
				h.repo = &perCredentialReserveRepo{fakeRepo: f, errors: map[string]error{"credential": store.ErrUntrackedCostBudget}}
			}
			if scenario == "race retries zero-budget key" {
				f.credentials = append(f.credentials, domain.Credential{ID: "second", EndpointID: "endpoint", Secret: "second-secret", Enabled: true, Priority: -1, RPM: 60, TPM: 100000, MaxConcurrency: 5})
			}
			w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[]}`)
			if scenario == "race retries zero-budget key" {
				if w.Code != 200 || calls.Load() != 1 || len(f.reservations) != 1 || f.reservations[0].CredentialID != "second" {
					t.Fatalf("race fallback failed %d %s", w.Code, w.Body)
				}
			} else if w.Code != 402 || !strings.Contains(w.Body.String(), "untracked_cost_budget") || calls.Load() != 0 || len(f.reservations) != 0 {
				t.Fatalf("unknown cost bypassed budget: %d %s", w.Code, w.Body)
			}
			l := h.limiter.(*fakeLimiter)
			if l.acquired != l.released {
				t.Fatal("candidate rejection leaked active capacity")
			}
		})
	}
}

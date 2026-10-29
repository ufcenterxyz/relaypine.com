package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

type perCredentialReserveRepo struct {
	*fakeRepo
	errors map[string]error
	tried  []string
}

func (f *perCredentialReserveRepo) Reserve(ctx context.Context, v domain.Reservation) error {
	f.tried = append(f.tried, v.CredentialID)
	if err := f.errors[v.CredentialID]; err != nil {
		return err
	}
	return f.fakeRepo.Reserve(ctx, v)
}

func TestRoutingBudgetEligibilityAndAtomicRaceFallback(t *testing.T) {
	cases := []struct {
		name                                                             string
		firstBudget, secondBudget                                        int64
		reserveErr                                                       error
		secondBusy                                                       bool
		upstreamStatus, wantStatus, wantTries, wantAcquired, wantNetwork int
		wantCredential                                                   string
	}{
		{name: "skip unaffordable lowest load", firstBudget: 1, secondBudget: 100000000, upstreamStatus: 200, wantStatus: 200, wantTries: 1, wantAcquired: 1, wantNetwork: 1, wantCredential: "second"},
		{name: "atomic budget race retries before send", firstBudget: 100000000, secondBudget: 100000000, reserveErr: fmt.Errorf("concurrent spend: %w", store.ErrBudgetExceeded), upstreamStatus: 200, wantStatus: 200, wantTries: 2, wantAcquired: 2, wantNetwork: 1, wantCredential: "second"},
		{name: "customer insufficient fails immediately", firstBudget: 100000000, secondBudget: 100000000, reserveErr: store.ErrInsufficientFunds, upstreamStatus: 200, wantStatus: 402, wantTries: 1, wantAcquired: 1},
		{name: "all budgets too small consume no capacity", firstBudget: 1, secondBudget: 1, upstreamStatus: 200, wantStatus: 402},
		{name: "funded candidate busy returns rate limit", firstBudget: 1, secondBudget: 100000000, secondBusy: true, upstreamStatus: 200, wantStatus: 429},
		{name: "service failure skips other keys on the same upstream", firstBudget: 100000000, secondBudget: 100000000, upstreamStatus: 503, wantStatus: 503, wantTries: 1, wantAcquired: 1, wantNetwork: 1, wantCredential: "credential"},
		{name: "request error is never sent to another key", firstBudget: 100000000, secondBudget: 100000000, upstreamStatus: 422, wantStatus: 422, wantTries: 1, wantAcquired: 1, wantNetwork: 1, wantCredential: "credential"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wantSecret := "upstream-secret"
				if tc.wantCredential == "second" {
					wantSecret = "second-upstream-secret"
				}
				if r.Header.Get("Authorization") != "Bearer "+wantSecret {
					t.Error("request sent to wrong upstream credential")
				}
				w.WriteHeader(tc.upstreamStatus)
				_, _ = io.WriteString(w, `{"id":"chat_1","usage":{"prompt_tokens":5,"completion_tokens":2}}`)
			}))
			defer upstream.Close()
			h, base := fixture("openai", upstream.URL)
			base.routing.Strategy = "least_load"
			base.credentials[0].BudgetMicros = tc.firstBudget
			second := base.credentials[0]
			second.ID = "second"
			second.Secret = "second-upstream-secret"
			second.BudgetMicros = tc.secondBudget
			base.credentials = append(base.credentials, second)
			repo := &perCredentialReserveRepo{fakeRepo: base, errors: map[string]error{"credential": tc.reserveErr}}
			h.repo = repo
			limiter := &fakeLimiter{loads: map[string]float64{"credential:credential": 0.1, "credential:second": 0.5}, denied: map[string]bool{"credential:second": tc.secondBusy}}
			h.limiter = limiter
			w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}]}`)
			if w.Code != tc.wantStatus {
				t.Fatalf("status %d want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if len(repo.tried) != tc.wantTries || limiter.acquired != tc.wantAcquired || limiter.released != tc.wantAcquired || int(calls.Load()) != tc.wantNetwork {
				t.Fatalf("tried=%v acquired=%d released=%d network=%d", repo.tried, limiter.acquired, limiter.released, calls.Load())
			}
			if tc.wantNetwork > 0 {
				if len(base.reservations) != 1 || base.reservations[0].CredentialID != tc.wantCredential || len(base.settlements) != 1 {
					t.Fatalf("reservation/settlement mismatch: %+v %+v", base.reservations, base.settlements)
				}
			} else if len(base.reservations) != 0 || len(base.settlements) != 0 {
				t.Fatal("failed routing created a money reservation or settlement")
			}
		})
	}
}

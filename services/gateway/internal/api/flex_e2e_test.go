package api

import (
	"context"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/pricing"
)

// Official Flex prices arrive with the reference prices: the preview shows
// them, applying stores them, customers see their sales rates without the
// upstream cost, an edit that keeps the rates keeps them, and a manual price
// drops them.
func TestFlexReferencePricesEndToEnd(t *testing.T) {
	f := referencePriceFixture(t)
	f.s.PricingFetcher = func(context.Context, []string) pricing.Catalog {
		now := time.Now().UTC()
		return pricing.Catalog{Sources: []pricing.Source{{Provider: "openai", URL: pricing.OpenAIURL, CheckedAt: now}}, Rates: map[string]pricing.Rate{
			"openai:gpt-6.1-terra": {Input: 1_000_000, Output: 5_000_000, Cached: 50_000, CacheWrite: 1_250_000,
				Long:      &pricing.LongRate{Input: 2_000_000, Output: 7_500_000, Cached: 100_000, CacheWrite: 2_500_000},
				Flex:      &pricing.FlexRate{Input: 500_000, Output: 2_500_000, Cached: 25_000, CacheWrite: 625_000, Long: &pricing.LongRate{Input: 1_000_000, Output: 3_750_000, Cached: 50_000, CacheWrite: 1_250_000}},
				Reference: domain.PriceReference{Provider: "openai", Model: "gpt-6.1-terra", SourceURL: pricing.OpenAIURL, SourceName: "OpenAI official reference", CheckedAt: now, Hash: "test-source", Scope: "standard_text", MaxInputTokens: 272000}},
		}}
	}
	f.h = f.s.Handler()
	admin := f.s.AdminAppURL
	id, official := f.prefix+"m_flex", f.prefix+"e_flex_official"
	f.models = append(f.models, id)
	f.endpoints = append(f.endpoints, official)
	// Reference prices need an enabled official upstream; it has no key.
	if _, err := f.db.SaveEndpoint(f.ctx, domain.Endpoint{ID: official, Name: "Official", Provider: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true, Models: []string{id}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.CreateModel(f.ctx, domain.Model{ID: id, Name: "GPT-6.1 Terra", Provider: "openai", UpstreamModel: "gpt-6.1-terra", Enabled: true, BillingMode: domain.BillingUsageOnly, MaxOutputTokens: 1000}); err != nil {
		t.Fatal(err)
	}
	want := domain.FlexPrice{InputRate: 500_000, OutputRate: 2_500_000, CachedRate: 25_000, CacheWriteRate: 625_000,
		CostInputRate: 500_000, CostOutputRate: 2_500_000, CostCachedRate: 25_000, CostCacheWriteRate: 625_000}
	wantLong := domain.LongContext{AboveInputTokens: 272000, InputRate: 1_000_000, OutputRate: 3_750_000, CachedRate: 50_000, CacheWriteRate: 1_250_000,
		CostInputRate: 1_000_000, CostOutputRate: 3_750_000, CostCachedRate: 50_000, CostCacheWriteRate: 1_250_000}
	sameFlex := func(got *domain.FlexPrice) bool {
		if got == nil || got.LongContext == nil || *got.LongContext != wantLong {
			return false
		}
		flat := *got
		flat.LongContext = nil
		return flat == want
	}
	p := pricePreview(t, f, map[string]any{"model_ids": []string{id}})
	if len(p.Items) != 1 || p.Items[0].Status != "matched" || !sameFlex(p.Items[0].After.Flex) {
		t.Fatalf("preview %+v", p.Items)
	}
	expectStatus(t, f.call(t, "POST", "/api/admin/models/prices/apply", f.operatorCookie, admin, map[string]any{"preview_id": p.ID, "model_ids": []string{id}}), 200)
	var stored domain.Model
	for _, m := range decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", f.operatorCookie, admin, nil)).Models {
		if m.ID == id {
			stored = m
		}
	}
	if !sameFlex(stored.Flex) {
		t.Fatalf("applied Flex price %+v", stored.Flex)
	}

	_, customerCookie := f.verifiedCustomer(t, "flexcustomer")
	seen := false
	for _, m := range decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", customerCookie, f.s.AppURL, nil)).Models {
		if m.ID == id {
			seen = true
			if fx := m.Flex; fx == nil || fx.InputRate != 500_000 || fx.CostInputRate != 0 || fx.CostOutputRate != 0 || fx.LongContext == nil || fx.LongContext.InputRate != 1_000_000 || fx.LongContext.CostInputRate != 0 {
				t.Fatalf("customer view of the Flex price %+v", fx)
			}
		}
	}
	if !seen {
		t.Fatal("customer cannot see the model")
	}

	// The console saves the whole model back. Unchanged rates keep the Flex
	// price, whatever Flex price the body claims.
	edited := stored
	edited.Name = "Terra"
	edited.Flex = &domain.FlexPrice{InputRate: 1, OutputRate: 1, CostInputRate: 1, CostOutputRate: 1}
	w := f.call(t, "POST", "/api/admin/models", f.operatorCookie, admin, edited)
	expectStatus(t, w, 200)
	if m := decodeResponse[domain.Model](t, w); !sameFlex(m.Flex) || m.PricingReference == nil {
		t.Fatalf("edit changed the Flex price %+v", m.Flex)
	}
	// Manual prices are not official: the Flex price goes with the reference.
	w = f.call(t, "POST", "/api/admin/models", f.operatorCookie, admin, struct {
		domain.Model
		Manual bool `json:"manual_pricing"`
	}{edited, true})
	expectStatus(t, w, 200)
	if m := decodeResponse[domain.Model](t, w); m.Flex != nil || m.PricingReference != nil {
		t.Fatalf("manual price kept the Flex price %+v", m.Flex)
	}
}

package api

import (
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestModelInputBillingModesAndPublicBounds(t *testing.T) {
	base := domain.Model{ID: "imported", Name: "Imported model", Provider: "anthropic", UpstreamModel: "upstream-model", Enabled: false, BillingMode: domain.BillingUsageOnly, MaxOutputTokens: 4096}
	if problem := validateModelInput(&base); problem != nil {
		t.Fatalf("disabled legacy usage-only draft rejected: %+v", problem)
	}
	enabled := base
	enabled.Enabled = true
	if problem := validateModelInput(&enabled); problem == nil {
		t.Fatal("new usage-only publication allowed")
	}
	for _, field := range []string{"input", "output", "cached", "cache_write", "cost_input", "cost_output", "cost_cached", "cost_cache_write"} {
		t.Run("usage_only_"+field, func(t *testing.T) {
			m := base
			rates := map[string]*int64{"input": &m.InputRate, "output": &m.OutputRate, "cached": &m.CachedRate, "cache_write": &m.CacheWriteRate, "cost_input": &m.CostInputRate, "cost_output": &m.CostOutputRate, "cost_cached": &m.CostCachedRate, "cost_cache_write": &m.CostCacheWriteRate}
			*rates[field] = 1
			if problem := validateModelInput(&m); problem == nil || problem.Code != "invalid_price" {
				t.Fatal("usage-only model accepted a nonzero rate")
			}
		})
	}
	for _, mode := range []string{"", domain.BillingMetered} {
		m := base
		m.BillingMode = mode
		m.Enabled = true
		if problem := validateModelInput(&m); problem == nil || problem.Code != "invalid_price" || m.BillingMode != domain.BillingMetered {
			t.Fatal("missing or metered mode bypassed required rates")
		}
		m.InputRate, m.OutputRate, m.CostInputRate, m.CostOutputRate = 1, 1, 1, 1
		if problem := validateModelInput(&m); problem != nil || m.BillingMode != domain.BillingMetered {
			t.Fatal("priced metered model was not normalized and accepted")
		}
		m.Enabled, m.InputRate = false, 0
		if problem := validateModelInput(&m); problem != nil {
			t.Fatal("incomplete metered draft rejected")
		}
	}
	for _, mode := range []string{"free", "usage-only", "METERED", " usage_only "} {
		m := base
		m.BillingMode = mode
		if problem := validateModelInput(&m); problem == nil || problem.Code != "invalid_billing_mode" {
			t.Fatal("unknown billing mode accepted")
		}
	}
	for _, cap := range []int{0, -1, 65537} {
		m := base
		m.MaxOutputTokens = cap
		if problem := validateModelInput(&m); problem == nil || problem.Code != "invalid_model" {
			t.Fatal("usage-only mode bypassed output cap")
		}
	}
	priced := base
	priced.BillingMode, priced.Enabled, priced.InputRate = domain.BillingMetered, false, 1000000001
	if problem := validateModelInput(&priced); problem == nil || problem.Code != "invalid_price" {
		t.Fatal("draft bypassed public price upper bound")
	}
}

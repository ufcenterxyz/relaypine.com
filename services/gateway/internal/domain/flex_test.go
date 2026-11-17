package domain

import "testing"

// terra is gpt-6.1-terra at OpenAI's published standard and Flex prices.
func terra() Model {
	return Model{
		ID: "terra", Provider: "openai", UpstreamModel: "gpt-6.1-terra", Enabled: true, BillingMode: BillingMetered, MaxOutputTokens: 1000,
		InputRate: 1_000_000, OutputRate: 5_000_000, CachedRate: 50_000, CacheWriteRate: 1_250_000,
		CostInputRate: 1_000_000, CostOutputRate: 5_000_000, CostCachedRate: 50_000, CostCacheWriteRate: 1_250_000,
		LongContext: &LongContext{AboveInputTokens: 272_000, InputRate: 2_000_000, OutputRate: 7_500_000, CachedRate: 100_000, CacheWriteRate: 2_500_000,
			CostInputRate: 2_000_000, CostOutputRate: 7_500_000, CostCachedRate: 100_000, CostCacheWriteRate: 2_500_000},
		Flex: &FlexPrice{InputRate: 500_000, OutputRate: 2_500_000, CachedRate: 25_000, CacheWriteRate: 625_000,
			CostInputRate: 500_000, CostOutputRate: 2_500_000, CostCachedRate: 25_000, CostCacheWriteRate: 625_000,
			LongContext: &LongContext{AboveInputTokens: 272_000, InputRate: 1_000_000, OutputRate: 3_750_000, CachedRate: 50_000, CacheWriteRate: 1_250_000,
				CostInputRate: 1_000_000, CostOutputRate: 3_750_000, CostCachedRate: 50_000, CostCacheWriteRate: 1_250_000}},
		PricingReference: &PriceReference{Provider: "openai", Model: "gpt-6.1-terra", MaxInputTokens: 272_000},
	}
}

func TestFlexPricedReplacesEveryRate(t *testing.T) {
	m := terra()
	f := m.FlexPriced()
	if f.Flex != nil || f.InputRate != 500_000 || f.OutputRate != 2_500_000 || f.CachedRate != 25_000 || f.CacheWriteRate != 625_000 ||
		f.CostInputRate != 500_000 || f.CostOutputRate != 2_500_000 || f.CostCachedRate != 25_000 || f.CostCacheWriteRate != 625_000 ||
		f.LongContext == nil || *f.LongContext != *m.Flex.LongContext {
		t.Fatalf("Flex priced model %+v", f)
	}
	if m.InputRate != 1_000_000 || m.LongContext.InputRate != 2_000_000 || m.Flex == nil {
		t.Fatal("pricing at Flex changed the model it was called on")
	}
	if !m.FlexCovers(1_000_000) {
		t.Fatal("a Flex long context price does not cover long prompts")
	}
	m.Flex.LongContext = nil
	if !m.FlexCovers(272_000) || m.FlexCovers(272_001) {
		t.Fatal("a Flex price without long context rates covers long prompts")
	}
	if (Model{}).FlexCovers(1) {
		t.Fatal("a model without a Flex price covers Flex requests")
	}
}

func TestCheckFlex(t *testing.T) {
	if err := CheckFlex(terra()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Model){
		"anthropic":           func(m *Model) { m.Provider = "anthropic" },
		"usage only":          func(m *Model) { m.BillingMode = BillingUsageOnly },
		"image":               func(m *Model) { m.Kind = ModelKindImage },
		"manual price":        func(m *Model) { m.PricingReference = nil },
		"free input":          func(m *Model) { m.Flex.InputRate = 0 },
		"free upstream":       func(m *Model) { m.Flex.CostOutputRate = 0 },
		"negative cache":      func(m *Model) { m.Flex.CachedRate = -1 },
		"above standard":      func(m *Model) { m.Flex.OutputRate = 5_000_001 },
		"cost above standard": func(m *Model) { m.Flex.CostCachedRate = 50_001 },
		"long without standard long": func(m *Model) {
			m.LongContext = nil
		},
		"other threshold":   func(m *Model) { m.Flex.LongContext.AboveInputTokens = 200_000 },
		"long below flex":   func(m *Model) { m.Flex.LongContext.InputRate = 400_000 },
		"long above long":   func(m *Model) { m.Flex.LongContext.CostOutputRate = 7_500_001 },
		"free long output":  func(m *Model) { m.Flex.LongContext.OutputRate = 0 },
		"long cache writes": func(m *Model) { m.Flex.LongContext.CacheWriteRate = 2_500_001 },
	} {
		m := terra()
		change(&m)
		if CheckFlex(m) == nil {
			t.Errorf("%s: accepted %+v", name, m.Flex)
		}
	}
	m := terra()
	m.Flex = nil
	if CheckFlex(m) != nil {
		t.Fatal("a model without a Flex price failed the Flex check")
	}
}

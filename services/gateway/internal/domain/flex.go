package domain

import "errors"

// OpenAI processing tiers a request can run at. RelayPine sends
// ServiceTierDefault unless the client asks for ServiceTierFlex.
const (
	ServiceTierDefault = "default"
	ServiceTierFlex    = "flex"
)

// FlexPrice is OpenAI's published price of Flex processing for one model:
// slower processing at lower rates, refused with 429 when OpenAI lacks the
// capacity. It comes only from OpenAI's official price table, with the
// model's official reference price, so a model without one refuses
// service_tier "flex". Sales and cost rates are micro-USD per one million
// tokens, like the standard ones they replace.
//
// LongContext prices Flex prompts above the standard long context threshold.
// When the standard price has a long context tier and Flex has none, the Flex
// price covers prompts up to the threshold only.
// https://developers.openai.com/api/docs/guides/flex-processing
type FlexPrice struct {
	InputRate          int64        `json:"input_rate"`
	OutputRate         int64        `json:"output_rate"`
	CachedRate         int64        `json:"cached_rate"`
	CacheWriteRate     int64        `json:"cache_write_rate"`
	CostInputRate      int64        `json:"cost_input_rate"`
	CostOutputRate     int64        `json:"cost_output_rate"`
	CostCachedRate     int64        `json:"cost_cached_rate"`
	CostCacheWriteRate int64        `json:"cost_cache_write_rate"`
	LongContext        *LongContext `json:"long_context,omitempty"`
}

func (f *FlexPrice) rates() []int64 {
	return []int64{f.InputRate, f.OutputRate, f.CachedRate, f.CacheWriteRate, f.CostInputRate, f.CostOutputRate, f.CostCachedRate, f.CostCacheWriteRate}
}

// FlexRates lists every Flex sales and cost rate, long context included.
func (m Model) FlexRates() []int64 {
	f := m.Flex
	if f == nil {
		return nil
	}
	rates := f.rates()
	if l := f.LongContext; l != nil {
		rates = append(rates, l.InputRate, l.OutputRate, l.CachedRate, l.CacheWriteRate, l.CostInputRate, l.CostOutputRate, l.CostCachedRate, l.CostCacheWriteRate)
	}
	return rates
}

// FlexCovers reports whether the model's Flex price applies to a prompt of
// this many tokens: a model without a Flex long context price covers prompts
// up to its standard long context threshold.
func (m Model) FlexCovers(promptTokens int64) bool {
	if m.Flex == nil {
		return false
	}
	return m.Flex.LongContext != nil || !m.LongPrompt(promptTokens)
}

// FlexPriced returns the model priced at its Flex rates, which replace the
// standard ones; the Flex long context price replaces the standard one.
func (m Model) FlexPriced() Model {
	f := m.Flex
	if f == nil {
		return m
	}
	m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate = f.InputRate, f.OutputRate, f.CachedRate, f.CacheWriteRate
	m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate = f.CostInputRate, f.CostOutputRate, f.CostCachedRate, f.CostCacheWriteRate
	m.LongContext = f.LongContext
	m.Flex = nil
	return m
}

// CheckFlex validates a model's Flex price. Only metered OpenAI text models
// with an official reference price have one. It needs positive input and
// output rates on both the sales and the cost side, and no rate above the
// standard rate it replaces. A Flex long context price needs a standard one
// with the same threshold, and lies between the Flex rates and the standard
// long context rates.
func CheckFlex(m Model) error {
	f := m.Flex
	if f == nil {
		return nil
	}
	if m.Provider != "openai" || m.IsImage() || m.EffectiveBillingMode() != BillingMetered {
		return errors.New("only metered OpenAI text models have Flex prices")
	}
	if m.PricingReference == nil {
		return errors.New("Flex prices come only with an official reference price")
	}
	if f.InputRate <= 0 || f.OutputRate <= 0 || f.CostInputRate <= 0 || f.CostOutputRate <= 0 {
		return errors.New("Flex prices need positive sales and upstream input and output rates")
	}
	standard := []int64{m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate, m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate}
	for i, rate := range f.rates() {
		if rate < 0 || rate > standard[i] {
			return errors.New("Flex rates cannot be higher than the standard rates")
		}
	}
	l := f.LongContext
	if l == nil {
		return nil
	}
	if m.LongContext == nil || l.AboveInputTokens != m.LongContext.AboveInputTokens {
		return errors.New("a Flex long context price needs a standard long context price with the same threshold")
	}
	if l.InputRate <= 0 || l.OutputRate <= 0 || l.CostInputRate <= 0 || l.CostOutputRate <= 0 {
		return errors.New("Flex long context prices need positive sales and upstream input and output rates")
	}
	flex, standardLong := f.rates(), m.LongContextRates()
	long := []int64{l.InputRate, l.OutputRate, l.CachedRate, l.CacheWriteRate, l.CostInputRate, l.CostOutputRate, l.CostCachedRate, l.CostCacheWriteRate}
	for i, rate := range long {
		if rate < flex[i] || rate > standardLong[i] {
			return errors.New("Flex long context rates must lie between the Flex rates and the standard long context rates")
		}
	}
	return nil
}

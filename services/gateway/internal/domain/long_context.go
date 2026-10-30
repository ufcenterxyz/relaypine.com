package domain

import "errors"

// MaxLongContextThreshold bounds the prompt length a long context price can
// start above.
const MaxLongContextThreshold = 100_000_000

// LongContext is the price of a request whose prompt is longer than
// AboveInputTokens. OpenAI prices prompts over 272K input tokens this way on
// its newer models, and Anthropic prices prompts over 100K tokens this way on
// Claude Haiku 5.5. A longer prompt moves the whole request, output included,
// to these rates.
// https://developers.openai.com/api/docs/pricing
// https://platform.claude.com/docs/en/about-claude/pricing#long-context-pricing
type LongContext struct {
	AboveInputTokens   int64 `json:"above_input_tokens"`
	InputRate          int64 `json:"input_rate"`
	OutputRate         int64 `json:"output_rate"`
	CachedRate         int64 `json:"cached_rate"`
	CacheWriteRate     int64 `json:"cache_write_rate"`
	CostInputRate      int64 `json:"cost_input_rate"`
	CostOutputRate     int64 `json:"cost_output_rate"`
	CostCachedRate     int64 `json:"cost_cached_rate"`
	CostCacheWriteRate int64 `json:"cost_cache_write_rate"`
}

// PromptTokens is the prompt length that picks a request's price: all of its
// input tokens, including cache reads and cache writes. An image tool's tokens
// belong to the image model and are not part of it.
func (u Usage) PromptTokens() int64 {
	return u.InputTokens + u.CachedTokens + u.CacheWriteTokens
}

// LongPrompt reports whether a prompt of this many tokens pays the model's
// long context rates.
func (m Model) LongPrompt(promptTokens int64) bool {
	return m.LongContext != nil && promptTokens > m.LongContext.AboveInputTokens
}

// PricedFor returns the model with its long context rates in place of the
// standard ones when a prompt of this many tokens pays them.
func (m Model) PricedFor(promptTokens int64) Model {
	if !m.LongPrompt(promptTokens) {
		return m
	}
	l := m.LongContext
	m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate = l.InputRate, l.OutputRate, l.CachedRate, l.CacheWriteRate
	m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate = l.CostInputRate, l.CostOutputRate, l.CostCachedRate, l.CostCacheWriteRate
	return m
}

// LongContextRates lists the long context sales and upstream cost rates.
func (m Model) LongContextRates() []int64 {
	l := m.LongContext
	if l == nil {
		return nil
	}
	return []int64{l.InputRate, l.OutputRate, l.CachedRate, l.CacheWriteRate, l.CostInputRate, l.CostOutputRate, l.CostCachedRate, l.CostCacheWriteRate}
}

// CheckLongContext validates a model's long context price. Only metered text
// models have one. It needs a threshold, positive input and output rates on
// both the sales and the cost side, and no rate below its standard rate, so a
// hold taken at the long rates covers either tier.
func CheckLongContext(m Model) error {
	l := m.LongContext
	if l == nil {
		return nil
	}
	if m.IsImage() || m.EffectiveBillingMode() != BillingMetered {
		return errors.New("only metered text models have long context prices")
	}
	if l.AboveInputTokens < 1 || l.AboveInputTokens > MaxLongContextThreshold {
		return errors.New("long context prices need a threshold between 1 and 100,000,000 input tokens")
	}
	if l.InputRate <= 0 || l.OutputRate <= 0 || l.CostInputRate <= 0 || l.CostOutputRate <= 0 {
		return errors.New("long context prices need positive sales and upstream input and output rates")
	}
	standard := []int64{m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate, m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate}
	for i, rate := range m.LongContextRates() {
		if rate < standard[i] {
			return errors.New("long context rates cannot be lower than the standard rates")
		}
	}
	return nil
}

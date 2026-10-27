package relay

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"

	"relaypine.com/gateway/internal/domain"
)

func amount(u domain.Usage, input, output, cached, write int64) (int64, error) {
	if !u.Known {
		return 0, errors.New("usage unknown")
	}
	sum := new(big.Int)
	for _, p := range [][2]int64{{u.InputTokens, input}, {u.OutputTokens, output}, {u.CachedTokens, cached}, {u.CacheWriteTokens, write}} {
		if p[0] < 0 || p[1] < 0 {
			return 0, errors.New("negative price or usage")
		}
		sum.Add(sum, new(big.Int).Mul(big.NewInt(p[0]), big.NewInt(p[1])))
	}
	sum.Add(sum, big.NewInt(999999))
	sum.Div(sum, big.NewInt(1000000))
	if !sum.IsInt64() {
		return 0, errors.New("price overflow")
	}
	return sum.Int64(), nil
}
func charges(u domain.Usage, m domain.Model) (int64, int64, error) {
	if m.EffectiveBillingMode() == domain.BillingUsageOnly {
		return 0, 0, nil
	}
	sell, err := amount(u, m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate)
	if err != nil {
		return 0, 0, err
	}
	cost, err := amount(u, m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate)
	return sell, cost, err
}
func estimate(input, output int64, m domain.Model) (int64, int64, error) {
	if m.EffectiveBillingMode() == domain.BillingUsageOnly {
		return 0, 0, nil
	}
	u := domain.Usage{InputTokens: input, OutputTokens: output, Known: true}
	sell, err := amount(u, max(m.InputRate, m.CachedRate, m.CacheWriteRate), m.OutputRate, 0, 0)
	if err != nil {
		return 0, 0, err
	}
	cost, err := amount(u, max(m.CostInputRate, m.CostCachedRate, m.CostCacheWriteRate), m.CostOutputRate, 0, 0)
	return sell, cost, err
}

func validAccounting(m domain.Model) bool {
	return m.EffectiveBillingMode() == domain.BillingMetered && m.InputRate > 0 && m.OutputRate > 0 && m.CostInputRate > 0 && m.CostOutputRate > 0 && m.CachedRate >= 0 && m.CacheWriteRate >= 0 && m.CostCachedRate >= 0 && m.CostCacheWriteRate >= 0
}
func integer(raw json.RawMessage) (int64, bool) {
	var n int64
	if len(raw) == 0 || json.Unmarshal(raw, &n) != nil || n < 0 || n > 1<<40 {
		return 0, false
	}
	return n, true
}
func object(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	return m
}
func str(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }

// Internal usage always records uncached input separately. OpenAI includes cache
// reads in input_tokens/prompt_tokens; Anthropic excludes cache reads and writes.
func parseUsage(raw json.RawMessage, provider string) (domain.Usage, bool) {
	m := object(raw)
	var u domain.Usage
	var ok bool
	if provider == "anthropic" {
		u.InputTokens, ok = integer(m["input_tokens"])
		if !ok {
			return u, false
		}
		u.OutputTokens, ok = integer(m["output_tokens"])
		if !ok {
			return u, false
		}
		if v, exists := m["cache_read_input_tokens"]; exists {
			u.CachedTokens, ok = integer(v)
			if !ok {
				return u, false
			}
		}
		if raw, exists := m["cache_creation"]; exists {
			detail := object(raw)
			if v, present := detail["ephemeral_1h_input_tokens"]; present {
				n, valid := integer(v)
				if !valid || n != 0 {
					return u, false
				}
			}
		}
		if v, exists := m["cache_creation_input_tokens"]; exists {
			u.CacheWriteTokens, ok = integer(v)
			if !ok {
				return u, false
			}
		}
	} else {
		in, out, details := "input_tokens", "output_tokens", "input_tokens_details"
		if _, exists := m["prompt_tokens"]; exists {
			in, out, details = "prompt_tokens", "completion_tokens", "prompt_tokens_details"
		}
		u.InputTokens, ok = integer(m[in])
		if !ok {
			return u, false
		}
		u.OutputTokens, ok = integer(m[out])
		if !ok {
			return u, false
		}
		if v, exists := object(m[details])["cached_tokens"]; exists {
			u.CachedTokens, ok = integer(v)
			if !ok || u.CachedTokens > u.InputTokens {
				return u, false
			}
			u.InputTokens -= u.CachedTokens
		}
		detail := object(m[details])
		if v, exists := detail["cache_write_tokens"]; exists {
			u.CacheWriteTokens, ok = integer(v)
			if !ok || u.CacheWriteTokens > u.InputTokens {
				return u, false
			}
			u.InputTokens -= u.CacheWriteTokens
		}
		if raw, exists := detail["cache_write_tokens_details"]; exists {
			writes := object(raw)
			short, valid := integer(writes["ttl_30m_tokens"])
			long, validLong := integer(writes["ttl_12h_tokens"])
			if !valid || !validLong || long != 0 || short != u.CacheWriteTokens {
				return u, false
			}
		}
	}
	u.Known = true
	return u, true
}

func validatePayload(m map[string]json.RawMessage, path string) error {
	_, err := validatePayloadWithImages(m, path)
	return err
}

func validatePayloadWithImages(m map[string]json.RawMessage, path string) (imageInputSummary, error) {
	var images imageInputSummary
	if err := validatePayloadCapabilities(m, path); err != nil {
		return images, err
	}
	for _, field := range []string{"input", "messages", "system"} {
		if raw, ok := m[field]; ok {
			if field == "messages" && (path == "/v1/messages" || path == "/v1/messages/count_tokens") {
				var err error
				images, err = inspectAnthropicImages(raw)
				if err != nil {
					return imageInputSummary{}, err
				}
				continue
			}
			var v any
			if json.Unmarshal(raw, &v) != nil {
				return imageInputSummary{}, errors.New("invalid " + field)
			}
			if err := validateContent(v, ""); err != nil {
				return imageInputSummary{}, err
			}
		}
	}
	return images, nil
}

func validatePayloadCapabilities(m map[string]json.RawMessage, path string) error {
	// A server-side retry can run another model with different permissions,
	// limits and prices. The current reservation and settlement cover one model.
	if _, exists := m["fallbacks"]; exists {
		return errors.New("server-side model fallbacks are not supported; send a separate request for each authorized model")
	}
	for _, field := range []string{"background", "conversation", "file_ids", "attachments", "audio", "modalities", "web_search_options", "prediction", "prompt", "mcp_servers", "container", "context_management", "skills"} {
		if v, ok := m[field]; ok && string(v) != "null" && string(v) != "false" {
			return errors.New("unsupported unmetered capability: " + field)
		}
	}
	if n, ok := m["n"]; ok && string(n) != "1" {
		return errors.New("only n=1 is supported")
	}
	tierName := "default"
	if strings.HasPrefix(path, "/v1/messages") {
		tierName = "standard_only"
	}
	if tier, ok := m["service_tier"]; ok && str(tier) != tierName && str(tier) != "auto" {
		return errors.New("only standard service tier is supported")
	}
	// Omitting this parameter permits upstream account defaults to select premium
	// processing. Force the tier represented by the model's configured rates.
	if !strings.HasSuffix(path, "/count_tokens") {
		m["service_tier"], _ = json.Marshal(tierName)
	}
	if speed, ok := m["speed"]; ok && str(speed) != "standard" {
		return errors.New("premium inference speed is not supported")
	}
	if geo, ok := m["inference_geo"]; ok && str(geo) != "global" {
		return errors.New("regional premium inference is not supported")
	}
	if cache, ok := m["prompt_cache_retention"]; ok && str(cache) != "in-memory" && str(cache) != "in_memory" {
		return errors.New("extended prompt caching is not supported")
	}
	if raw, exists := m["prompt_cache_options"]; exists {
		options := object(raw)
		if options == nil {
			return errors.New("invalid prompt cache options")
		}
		for key, value := range options {
			switch key {
			case "ttl":
				if str(value) != "30m" {
					return errors.New("only 30-minute OpenAI cache writes are priced")
				}
			case "mode":
				if str(value) != "implicit" && str(value) != "explicit" {
					return errors.New("invalid prompt cache mode")
				}
			default:
				return errors.New("unsupported prompt cache option")
			}
		}
	}
	if _, ok := m["previous_response_id"]; ok && !strings.HasSuffix(path, "/responses") {
		return errors.New("resource references are supported only by Responses")
	}
	var tools []map[string]json.RawMessage
	if raw, ok := m["tools"]; ok {
		if json.Unmarshal(raw, &tools) != nil {
			return errors.New("invalid tools")
		}
		for _, t := range tools {
			kind := str(t["type"])
			if kind != "" && kind != "function" && kind != "custom" {
				return errors.New("built-in tools are not supported")
			}
			if _, ok := t["server_label"]; ok {
				return errors.New("remote tools are not supported")
			}
			if cache := object(t["cache_control"]); str(cache["ttl"]) == "1h" {
				return errors.New("one-hour cache writes require separate pricing")
			}
		}
	}
	if cache := object(m["cache_control"]); str(cache["ttl"]) == "1h" {
		return errors.New("one-hour cache writes require separate pricing")
	}
	return nil
}

// Ordinary text and user-defined tool input/output remain opaque. Image blocks
// are allowed only through the Anthropic message-position parser above, which
// supplies an independently bounded input-cost summary. All other references
// to external input or hosted computation remain unsupported.
func validateContent(v any, parent string) error {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			if err := validateContent(item, parent); err != nil {
				return err
			}
		}
	case map[string]any:
		if parent == "prompt_cache_breakpoint" {
			for key, value := range x {
				if key == "ttl" && value != "30m" || key == "mode" && value != "explicit" || key != "ttl" && key != "mode" {
					return errors.New("only standard 30-minute cache breakpoints are supported")
				}
			}
			return nil
		}
		if parent == "cache_control" {
			if x["ttl"] == "1h" {
				return errors.New("one-hour cache writes require separate pricing")
			}
			return nil
		}
		if typ, _ := x["type"].(string); typ != "" {
			switch typ {
			case "text", "input_text", "output_text", "message", "tool_use", "tool_result", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output", "refusal", "thinking":
			default:
				return errors.New("unsupported content type: " + typ)
			}
		}
		for k, item := range x {
			if k == "file_id" || k == "image_url" || k == "encrypted_content" || k == "cache_key" {
				return errors.New("unsupported content reference")
			}
			if k == "ttl" && item == "1h" {
				return errors.New("one-hour cache writes require separate pricing")
			}
			if k == "arguments" || k == "input" {
				continue
			}
			if err := validateContent(item, k); err != nil {
				return err
			}
		}
	}
	return nil
}

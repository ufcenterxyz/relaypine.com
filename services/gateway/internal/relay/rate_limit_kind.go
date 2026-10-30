package relay

import (
	"encoding/json"
	"strings"
)

// modelScope is a route bucket that counts and pauses one model on a key or
// quota group. It has no limits of its own.
func modelScope(scope, model string) string { return scope + "|model:" + model }

type limitKind int

const (
	// limitUnknown pauses the whole key and its quota group, as for any
	// refusal the gateway cannot place.
	limitUnknown limitKind = iota
	// limitModel is a rate limit on one model or model class.
	limitModel
	// limitAccount is a billing, quota or spend refusal for the whole account.
	limitAccount
)

// rateLimitKind reads an upstream 429. OpenAI and Anthropic set rate limits
// per model (Anthropic per model class), so a burst on one model leaves the
// others usable. Billing, quota and spend refusals apply to every model.
// https://developers.openai.com/api/docs/guides/error-codes
// https://platform.claude.com/docs/en/api/rate-limits#reaching-your-spend-cap
func rateLimitKind(provider string, body []byte) limitKind {
	var reply struct {
		Error struct {
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Details struct {
				ErrorCode string `json:"error_code"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return limitUnknown
	}
	var code string
	_ = json.Unmarshal(reply.Error.Code, &code)
	switch provider {
	case "openai":
		switch {
		case code == "rate_limit_exceeded" || code == "slow_down":
			return limitModel
		case reply.Error.Type == "insufficient_quota" || code == "insufficient_quota" || code == "credit_balance_exhausted" ||
			code == "organization_usage_limit_exceeded" || strings.HasSuffix(code, "_spend_limit_exceeded"):
			return limitAccount
		}
	case "anthropic":
		switch {
		case reply.Error.Details.ErrorCode == "enforced_spend_limit_reached":
			return limitAccount
		case reply.Error.Type == "rate_limit_error" && reply.Error.Details.ErrorCode == "":
			return limitModel
		}
	}
	return limitUnknown
}

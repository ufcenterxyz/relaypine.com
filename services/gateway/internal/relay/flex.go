package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/pricing"
)

// Flex processing: service_tier "flex" on OpenAI Responses and Chat
// Completions runs a request slower, at OpenAI's lower Flex rates, and OpenAI
// refuses it with HTTP 429 when it lacks the capacity, without charging it.
// The gateway accepts it only for a model with OpenAI's official Flex price
// and only on official OpenAI routes, holds and settles it at the Flex rates
// unless OpenAI reports that it ran at the standard tier, and passes a
// capacity refusal to the client without pausing the key or the model.
// https://developers.openai.com/api/docs/guides/flex-processing

const (
	// requestTimeout bounds a request, except a streamed Flex request.
	requestTimeout = 5 * time.Minute
	// flexStreamTimeout bounds a streamed Flex request, which can wait for
	// OpenAI's capacity: OpenAI recommends a 15-minute client timeout for
	// Flex. The heartbeat keeps the client's connection open meanwhile. A
	// Flex request that is not streamed keeps the 90-second reply limit,
	// because nothing can keep its connection open.
	flexStreamTimeout = 15 * time.Minute
	// leaseMargin keeps a request's concurrency lease past its deadline, so a
	// running request never loses its slot.
	leaseMargin = 2 * time.Minute
)

// timeout is how long the request may run.
func (p plan) timeout() time.Duration {
	if p.flex && p.stream {
		return flexStreamTimeout
	}
	return requestTimeout
}

// flexPath reports a protocol path that accepts service_tier "flex".
func flexPath(path string) bool {
	return path == "/v1/responses" || path == "/v1/chat/completions"
}

// The refusal of a Flex request for lack of capacity, in OpenAI's terms.
const (
	flexUnavailableCode    = "flex_unavailable"
	flexUnavailableType    = "resource_unavailable"
	flexUnavailableMessage = `Flex processing is temporarily unavailable. Retry later with backoff, or send the request without service_tier "flex" to run it at standard rates. This request was not charged.`
)

// failFlexUnavailable answers a Flex request OpenAI refused for capacity:
// HTTP 429 with OpenAI's error type and code, and Retry-After when OpenAI
// gave one.
func failFlexUnavailable(w http.ResponseWriter, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": flexUnavailableType, "code": flexUnavailableCode, "message": flexUnavailableMessage, "param": "service_tier"}})
}

// upstreamRetryAfter is an upstream Retry-After value in whole seconds, or
// empty when the upstream gave none that can be read.
func upstreamRetryAfter(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return ""
		}
		return strconv.Itoa(min(seconds, 86400))
	}
	if t, err := http.ParseTime(value); err == nil {
		return strconv.FormatInt(min(retryAfter(time.Until(t)), 86400), 10)
	}
	return ""
}

// flexWord finds Flex named in an error message, but not words such as
// "flexible".
var flexWord = regexp.MustCompile(`(?i)\bflex\b`)

// flexError reports an OpenAI error object that refuses Flex processing:
// OpenAI's flex_unavailable code, its resource_unavailable type, or a
// message about Flex.
func flexError(e map[string]json.RawMessage) bool {
	code, kind := str(e["code"]), str(e["type"])
	return code == flexUnavailableCode || code == flexUnavailableType || kind == flexUnavailableType || flexWord.MatchString(str(e["message"]))
}

// flexCapacityRefusal reads an upstream 429 to a Flex request. Unless OpenAI
// names a rate limit or a billing, quota or spend refusal without mentioning
// Flex, the 429 is the documented "Resource Unavailable" refusal: Flex
// capacity is short. It says nothing about this key's own limits, so it must
// not pause the key or the model for standard traffic.
func flexCapacityRefusal(body []byte) bool {
	if flexError(object(object(body)["error"])) {
		return true
	}
	return rateLimitKind("openai", body) == limitUnknown
}

// flexStreamRefusal reports a stream event that refuses a Flex request: an
// error event, which may carry its code at the top level or in an error
// object, a failed Response whose error refuses Flex, or a Chat Completions
// chunk with such an error.
func flexStreamRefusal(obj map[string]json.RawMessage) bool {
	switch str(obj["type"]) {
	case "error":
		if code := str(obj["code"]); code == flexUnavailableCode || code == flexUnavailableType || flexWord.MatchString(str(obj["message"])) {
			return true
		}
	case "response.failed":
		return flexError(object(object(obj["response"])["error"]))
	}
	if raw, ok := obj["error"]; ok && string(raw) != "null" {
		return flexError(object(raw))
	}
	return false
}

// pricedTier is the processing tier an OpenAI reply states, when it is one
// the gateway prices: "flex", or "default" for the standard tier. Any other
// value, or none, leaves the tier unknown.
func pricedTier(tier string) string {
	switch tier {
	case domain.ServiceTierFlex, domain.ServiceTierDefault:
		return tier
	}
	return ""
}

// errFlexRefused stops a stream whose first event refuses Flex processing,
// before anything reaches the client.
var errFlexRefused = errors.New("flex processing refused")

// untouched reports usage that charges nothing, whatever its tier.
func untouched(u domain.Usage) bool {
	u.ServiceTier = ""
	return u == domain.Usage{Known: true}
}

// checkFlex refuses a Flex request the gateway cannot price at OpenAI's Flex
// rates, before anything is reserved or sent.
func (p *plan) checkFlex() error {
	if p.model.Flex == nil || p.model.PricingReference == nil {
		return fmt.Errorf(`Flex processing is not available for %s: OpenAI publishes no Flex price for it. Omit service_tier "flex" or send "default".`, p.model.ID)
	}
	if p.imageTool != nil {
		return errors.New(`Flex processing is not available with the image generation tool: OpenAI publishes no Flex price for image generation. Omit service_tier "flex" or the image generation tool.`)
	}
	input := p.input
	if p.webSearch != nil {
		input += p.webSearch.inputBound
	}
	if !p.model.FlexCovers(input) {
		return fmt.Errorf(`Flex processing for %s is priced only for prompts up to %s tokens, and this request's input could exceed that. Shorten the input, lower max_tool_calls, or omit service_tier "flex".`, p.model.ID, strconv.FormatInt(p.model.LongContext.AboveInputTokens, 10))
	}
	return nil
}

// errFlexRoute is a Flex request for a model whose only routes are backups,
// which cannot offer OpenAI's Flex processing.
var errFlexRoute = errors.New(`Flex processing runs only on official OpenAI routes, and this model has none available. Omit service_tier "flex" or send "default".`)

// flexRoute reports a route that can serve a Flex request: an official OpenAI
// endpoint. Backups and compatible hosts cannot.
func flexRoute(e domain.Endpoint) bool {
	return !e.IsBackup() && e.Provider == "openai" && pricing.OfficialEndpoint(e.Provider, e.BaseURL)
}

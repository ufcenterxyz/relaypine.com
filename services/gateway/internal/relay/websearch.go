package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The Responses web_search tool. OpenAI prices it per call and bills the
// pages the model reads ("search content tokens") as the model's input
// tokens, which the response's usage already counts. Search actions incur
// the call price; opening a page or finding text in it does not.
// https://developers.openai.com/api/docs/pricing#built-in-tools
// https://developers.openai.com/api/docs/guides/tools-web-search
const (
	// $10.00 per 1,000 calls: web_search, and web_search_preview with a
	// reasoning model. Sales equal cost, as for every official price.
	webSearchCallMicros = 10_000
	// $25.00 per 1,000 calls: web_search_preview with any other model,
	// whose search content tokens are free.
	webSearchPreviewCallMicros = 25_000
	// max_tool_calls is pinned to bound the reservation. Each call is one
	// search, page open or find; a thorough answer may take several.
	defaultWebSearchCalls = 10
	maxWebSearchCalls     = 20
	// Web search limits the model's context window to 128,000 tokens. Each
	// call lets the model read its context once more, so each allowed call
	// reserves a full window of input at the model's input rates.
	webSearchContextTokens = 128_000
)

// webSearchType matches web_search and web_search_preview, with or without
// a version date such as web_search_2025_08_26.
var webSearchType = regexp.MustCompile(`^web_search(?:_preview)?(?:_[0-9]{4}_[0-9]{2}_[0-9]{2})?$`)

// webSearchFields are the documented tool fields. Any other field could
// change what a call costs, so it is refused.
var webSearchFields = map[string]bool{"type": true, "filters": true, "search_context_size": true, "user_location": true, "external_web_access": true, "return_token_budget": true, "search_content_types": true}

// webSearchPlan is the validated web search tool of one request.
type webSearchPlan struct {
	// calls is the max_tool_calls sent upstream.
	calls int
	// callMicros is OpenAI's price of one search.
	callMicros int64
	// inputBound is the input the calls can add to the request's own.
	inputBound int64
}

// fee is the reservation for every allowed call at the search price.
func (s *webSearchPlan) fee() int64 { return int64(s.calls) * s.callMicros }

var reasoningFamily = regexp.MustCompile(`^(?:gpt-(?:[5-9]|[1-9][0-9])|o[1-9])`)

// openAIReasoning reports a reasoning model by its upstream ID: GPT-5 and
// later and the o-series, except the chat snapshots.
func openAIReasoning(upstream string) bool {
	return reasoningFamily.MatchString(upstream) && !strings.Contains(upstream, "-chat")
}

// prepareWebSearch validates the web search tool of a Responses request and
// pins max_tool_calls in the body. It runs after prepareImageTool, whose call
// bound also bounds the searches when a request offers both tools.
func (h *Handler) prepareWebSearch(p *plan, m map[string]json.RawMessage) error {
	var tools []json.RawMessage
	if raw, ok := m["tools"]; !ok || json.Unmarshal(raw, &tools) != nil {
		return nil
	}
	var tool map[string]json.RawMessage
	kind := ""
	for _, raw := range tools {
		t := object(raw)
		if !webSearchType.MatchString(str(t["type"])) {
			continue
		}
		if tool != nil {
			return errors.New("declare the web search tool once")
		}
		tool, kind = t, str(t["type"])
	}
	if tool == nil {
		return nil
	}
	if p.provider != "openai" || !strings.HasSuffix(p.path, "/responses") {
		return errors.New("the web search tool is supported only by the Responses API")
	}
	for key := range tool {
		if !webSearchFields[key] {
			return errors.New("unsupported web search tool field: " + key)
		}
	}
	upstream := p.model.UpstreamModel
	preview := strings.HasPrefix(kind, "web_search_preview")
	// OpenAI bills these models' search content as a fixed block of 8,000
	// input tokens per call, which their usage does not show.
	if !preview && (strings.HasPrefix(upstream, "gpt-4o-mini") || strings.HasPrefix(upstream, "gpt-4.1-mini")) {
		return fmt.Errorf("the web search tool is not available with %s", p.model.ID)
	}
	s := &webSearchPlan{calls: defaultWebSearchCalls, callMicros: webSearchCallMicros}
	if preview && !openAIReasoning(upstream) {
		s.callMicros = webSearchPreviewCallMicros
	}
	if p.imageTool != nil {
		// Already pinned, and smaller than the search bound.
		s.calls = p.imageTool.calls
	} else if raw, ok := m["max_tool_calls"]; ok && string(raw) != "null" {
		n, valid := integer(raw)
		if !valid || n < 1 || n > maxWebSearchCalls {
			return fmt.Errorf("max_tool_calls must be between 1 and %d with the web search tool", maxWebSearchCalls)
		}
		s.calls = int(n)
	}
	m["max_tool_calls"] = json.RawMessage(strconv.Itoa(s.calls))
	s.inputBound = int64(s.calls) * max(webSearchContextTokens, p.input)
	p.webSearch = s
	return nil
}

// webSearches follows the web_search_call items of one reply by ID, with the
// type of each one's action.
type webSearches map[string]string

// add records an output item if it is a web search call. A later copy of an
// item, such as the done event after the added event, may name its action.
func (s webSearches) add(raw json.RawMessage) {
	item := object(raw)
	if str(item["type"]) != "web_search_call" {
		return
	}
	id := str(item["id"])
	if id == "" {
		// Every item has an ID; one without is counted on its own.
		id = "unnamed:" + strconv.Itoa(len(s))
	}
	if action := str(object(item["action"])["type"]); action != "" || s[id] == "" {
		s[id] = action
	}
}

// addOutput records every item of a Response object's output.
func (s webSearches) addOutput(response map[string]json.RawMessage) {
	var items []json.RawMessage
	_ = json.Unmarshal(response["output"], &items)
	for _, item := range items {
		s.add(item)
	}
}

// billed counts the calls OpenAI prices: searches, and any call whose action
// is not known to be a page open or find, whatever its status.
func (s webSearches) billed() int64 {
	n := int64(0)
	for _, action := range s {
		if action != "open_page" && action != "find_in_page" {
			n++
		}
	}
	return n
}

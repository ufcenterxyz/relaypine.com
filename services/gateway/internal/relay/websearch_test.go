package relay

import (
	"strconv"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

// searchRequest is the request a client used to compare ChatGPT's web answers.
const searchRequest = `{"model":"model","input":"best 40cc mini bike for kids","tools":[{"type":"web_search","user_location":{"type":"approximate","country":"US"}}],"tool_choice":"required","include":["web_search_call.action.sources"],"store":false}`

// searchReply has two billed searches (one failed), a page open, a find in
// page, and an answer that cites a source.
const searchReply = `{"id":"resp_search","object":"response","status":"completed","model":"upstream-model","output":[` +
	`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"best 40cc mini bike for kids","sources":[{"type":"url","url":"https://example.com/a"},{"type":"url","url":"https://example.com/b"}]}},` +
	`{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"open_page","url":"https://example.com/a"}},` +
	`{"type":"web_search_call","id":"ws_3","status":"completed","action":{"type":"find_in_page","pattern":"40cc","url":"https://example.com/a"}},` +
	`{"type":"web_search_call","id":"ws_4","status":"failed","action":{"type":"search","queries":["40cc mini bike age"]}},` +
	`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"A 40cc bike suits ages 8 and up.","annotations":[{"type":"url_citation","start_index":0,"end_index":12,"url":"https://example.com/a","title":"A"}]}]}],` +
	`"usage":{"input_tokens":12000,"input_tokens_details":{"cached_tokens":2000},"output_tokens":800,"output_tokens_details":{"reasoning_tokens":500},"total_tokens":12800}}`

func TestWebSearchValidation(t *testing.T) {
	h, f := fixture("openai", "http://127.0.0.1:1")
	f.models = append(f.models, domain.Model{ID: "mini", Name: "mini", UpstreamModel: "gpt-4.1-mini", Provider: "openai", Enabled: true, InputRate: 400000, OutputRate: 1600000, CostInputRate: 400000, CostOutputRate: 1600000, MaxOutputTokens: 128, Version: 1})
	search := func(extra, fields string) string {
		return `{"model":"model","input":"x"` + extra + `,"tools":[{"type":"web_search"` + fields + `}]}`
	}
	for _, c := range []struct{ path, body, want string }{
		{"/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"x"}],"tools":[{"type":"web_search"}]}`, "supported only by the Responses API"},
		{"/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"x"}],"web_search_options":{"search_context_size":"low"}}`, "web_search_options is not supported yet; use the Responses API with the web_search tool"},
		{"/openai/v1/responses", search("", `,"foo":1`), "unsupported web search tool field: foo"},
		{"/openai/v1/responses", `{"model":"model","input":"x","tools":[{"type":"web_search"},{"type":"web_search_preview"}]}`, "declare the web search tool once"},
		{"/openai/v1/responses", search(`,"max_tool_calls":21`, ""), "max_tool_calls must be between 1 and 20"},
		{"/openai/v1/responses", search(`,"max_tool_calls":0`, ""), "max_tool_calls must be between 1 and 20"},
		{"/openai/v1/responses", `{"model":"mini","input":"x","tools":[{"type":"web_search"}]}`, "the web search tool is not available with mini"},
		// A replayed call cannot carry a reference the gateway refuses elsewhere.
		{"/openai/v1/responses", `{"model":"model","input":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q","file_id":"file_foreign"}}]}`, "unsupported content reference"},
	} {
		w := serveRequest(h, newRequest(c.path, c.body))
		if w.Code != 400 || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%.90s: HTTP %d %s, want %q", c.body, w.Code, w.Body.String(), c.want)
		}
	}
	if len(f.reservations) != 0 {
		t.Fatalf("an invalid web search request was reserved: %+v", f.reservations)
	}
}

// The request reaches OpenAI as sent, with max_tool_calls pinned; the reply
// reaches the client unchanged; the bill adds $10 per 1,000 searches to the
// tokens, which include the pages the model read.
func TestWebSearchForwardsAndBillsEachSearch(t *testing.T) {
	up := newImageUpstream(t, "application/json", searchReply)
	h, f := fixture("openai", up.server.URL)
	w := serveRequest(h, newRequest("/openai/v1/responses", searchRequest))
	if w.Code != 200 || w.Body.String() != searchReply {
		t.Fatalf("HTTP %d %.300s", w.Code, w.Body.String())
	}
	sent := object(up.bodies[0])
	if string(sent["tools"]) != `[{"type":"web_search","user_location":{"type":"approximate","country":"US"}}]` || string(sent["include"]) != `["web_search_call.action.sources"]` || str(sent["tool_choice"]) != "required" || string(sent["store"]) != "false" || string(sent["max_tool_calls"]) != "10" || str(sent["model"]) != "upstream-model" {
		t.Fatalf("upstream body %s", up.bodies[0])
	}
	// The text model's worst case with ten calls, each reading a full
	// 128,000-token window, plus ten searches at $0.01.
	input := int64(2*len(searchRequest)+4096) + 10*128000
	sell := (input*1_250_000+128*2_000_000+999_999)/1_000_000 + 10*10_000
	cost := (input*625_000+128*1_000_000+999_999)/1_000_000 + 10*10_000
	if r := f.reservations[0]; r.Request.ReservedMicros != sell || r.CostReserve != cost {
		t.Fatalf("reserved %d and %d, want %d and %d", r.Request.ReservedMicros, r.CostReserve, sell, cost)
	}
	s := f.settlements[0]
	// 10,000 uncached, 2,000 cached and 800 output tokens, and two searches.
	if s.Status != "completed" || s.Usage.WebSearchCalls != 2 || s.Usage.WebSearchMicros != 20_000 || s.Usage.InputTokens != 10_000 || s.Usage.CachedTokens != 2_000 || s.ChargeMicros != 12_600+20_000 || s.CostMicros != 6_300+20_000 {
		t.Fatalf("settlement %+v", s)
	}
}

func TestWebSearchCallPrices(t *testing.T) {
	reply := `{"id":"resp_search","object":"response","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}}],"usage":{"input_tokens":0,"output_tokens":0}}`
	for _, c := range []struct {
		model, tool string
		// calls is the client's max_tool_calls; zero leaves it out.
		calls, perCall int64
	}{
		{"model", "web_search_2025_08_26", 0, 10_000},
		{"model", "web_search_preview", 0, 25_000},
		{"model", "web_search_preview_2025_03_11", 3, 25_000},
		{"reasoner", "web_search_preview", 20, 10_000},
	} {
		up := newImageUpstream(t, "application/json", reply)
		h, f := fixture("openai", up.server.URL)
		reasoner := f.models[0]
		reasoner.ID, reasoner.UpstreamModel = "reasoner", "gpt-5.5"
		f.models = append(f.models, reasoner)
		limit, calls := "", int64(10)
		if c.calls > 0 {
			limit, calls = `,"max_tool_calls":`+strconv.FormatInt(c.calls, 10), c.calls
		}
		body := `{"model":"` + c.model + `","input":"x","max_output_tokens":1` + limit + `,"tools":[{"type":"` + c.tool + `","search_context_size":"low"}]}`
		w := serveRequest(h, newRequest("/openai/v1/responses", body))
		if w.Code != 200 {
			t.Fatalf("%s: HTTP %d %s", c.tool, w.Code, w.Body.String())
		}
		input := int64(2*len(body)+4096) + calls*128000
		sell := (input*1_250_000+2_000_000+999_999)/1_000_000 + calls*c.perCall
		sent := string(object(up.bodies[0])["max_tool_calls"])
		if got := f.reservations[0].Request.ReservedMicros; got != sell || sent != strconv.FormatInt(calls, 10) {
			t.Errorf("%s on %s: reserved %d, want %d; sent max_tool_calls %s", c.tool, c.model, got, sell, sent)
		}
		if s := f.settlements[0]; s.Usage.WebSearchCalls != 1 || s.Usage.WebSearchMicros != c.perCall || s.ChargeMicros != c.perCall || s.CostMicros != c.perCall {
			t.Errorf("%s on %s: settlement %+v", c.tool, c.model, s)
		}
	}
}

func TestOpenAIReasoningModels(t *testing.T) {
	for upstream, want := range map[string]bool{"gpt-5.5": true, "gpt-6.1-sol": true, "gpt-5.4-mini": true, "o3": true, "o4-mini": true, "gpt-5-chat-latest": false, "gpt-4.1": false, "gpt-4o": false, "upstream-model": false} {
		if openAIReasoning(upstream) != want {
			t.Errorf("%s: reasoning %v", upstream, !want)
		}
	}
}

// A stream reports each call as it starts and ends and again in its final
// Response. Every call any of them shows is counted, and a call whose action
// never arrived is billed as a search.
func TestStreamedWebSearchCountsEveryReportedCall(t *testing.T) {
	event := func(kind, data string) string { return "event: " + kind + "\ndata: " + data + "\n\n" }
	stream := event("response.created", `{"type":"response.created","response":{"id":"resp_search","status":"in_progress"}}`) +
		event("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"in_progress"}}`) +
		event("response.web_search_call.searching", `{"type":"response.web_search_call.searching","output_index":0,"item_id":"ws_1"}`) +
		event("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q","sources":[{"type":"url","url":"https://example.com/a"}]}}}`) +
		event("response.output_item.added", `{"type":"response.output_item.added","output_index":1,"item":{"type":"web_search_call","id":"ws_2","status":"in_progress"}}`) +
		event("response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"open_page","url":"https://example.com/a"}}}`) +
		event("response.output_item.added", `{"type":"response.output_item.added","output_index":2,"item":{"type":"web_search_call","id":"ws_3","status":"in_progress"}}`) +
		event("response.output_text.delta", `{"type":"response.output_text.delta","output_index":3,"delta":"Answer"}`) +
		event("response.completed", `{"type":"response.completed","response":{"id":"resp_search","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}},{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Answer","annotations":[{"type":"url_citation","start_index":0,"end_index":6,"url":"https://example.com/a","title":"A"}]}]}],"usage":{"input_tokens":3000,"output_tokens":100}}}`)
	up := newImageUpstream(t, "text/event-stream", stream)
	h, f := fixture("openai", up.server.URL)
	w := serveRequest(h, newRequest("/openai/v1/responses", `{"model":"model","input":"x","stream":true,"tools":[{"type":"web_search"}]}`))
	if w.Code != 200 || w.Body.String() != stream {
		t.Fatalf("HTTP %d %.300s", w.Code, w.Body.String())
	}
	s := f.settlements[0]
	// ws_1 searched, ws_2 opened a page, ws_3 never said what it did.
	if s.Status != "completed" || s.Usage.WebSearchCalls != 2 || s.Usage.WebSearchMicros != 20_000 || s.ChargeMicros != (3000*1_000_000+100*2_000_000)/1_000_000+20_000 {
		t.Fatalf("settlement %+v", s)
	}
}

// An earlier web search answer can be sent back as context.
func TestWebSearchAnswerCanBeSentBack(t *testing.T) {
	up := newImageUpstream(t, "application/json", `{"id":"resp_next","object":"response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":1}}`)
	h, f := fixture("openai", up.server.URL)
	body := `{"model":"model","input":[` +
		`{"role":"user","content":"best 40cc mini bike for kids"},` +
		`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q","sources":[{"type":"url","url":"https://example.com/a"}]}},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"A 40cc bike.","annotations":[{"type":"url_citation","start_index":0,"end_index":4,"url":"https://example.com/a","title":"A"}]}]},` +
		`{"role":"user","content":"Which is safest?"}]}`
	w := serveRequest(h, newRequest("/openai/v1/responses", body))
	if w.Code != 200 || len(f.settlements) != 1 || f.settlements[0].Usage.WebSearchCalls != 0 {
		t.Fatalf("HTTP %d %s %+v", w.Code, w.Body.String(), f.settlements)
	}
}

// With the image generation tool, its bound of 1 to 4 calls covers searches.
func TestWebSearchWithImageToolKeepsTheImageBound(t *testing.T) {
	up := newImageUpstream(t, "application/json", responseWithImage("QUJD", `{"input_tokens":1,"output_tokens":1}`))
	h, f := imageFixture(up.server.URL)
	w := serveRequest(h, newRequest("/openai/v1/responses", `{"model":"model","input":"x","max_tool_calls":2,"tools":[{"type":"image_generation"},{"type":"web_search"}]}`))
	if w.Code != 200 || string(object(up.bodies[0])["max_tool_calls"]) != "2" {
		t.Fatalf("HTTP %d %s; sent %.300s", w.Code, w.Body.String(), up.bodies[0])
	}
	if len(f.reservations) != 1 || f.reservations[0].Request.ReservedMicros < 2*10_000+2*128000*1_250_000/1_000_000 {
		t.Fatalf("reservation %+v", f.reservations)
	}
}

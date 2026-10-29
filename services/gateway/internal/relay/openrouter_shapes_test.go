package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newRequest(path, body string) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer rp_test")
	return r
}
func serveRequest(h *Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// These replies follow the shapes in OpenRouter's API reference: its own
// fields (provider, cost, cost_details, is_byok, native_finish_reason),
// keep-alive comments, and message_delta events that repeat every count.
// A backup reply must reach the client unchanged and settle the same usage
// an official reply would.
func TestOpenRouterReplyShapesStayCompatible(t *testing.T) {
	chatJSON := `{"id":"gen-1730000000-abc","object":"chat.completion","created":1730000000,"model":"openai/gpt-5.4-mini","provider":"OpenAI","system_fingerprint":null,"service_tier":"default","choices":[{"index":0,"logprobs":null,"finish_reason":"stop","native_finish_reason":"stop","message":{"role":"assistant","content":"Hi","refusal":null,"reasoning":null}}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24,"cost":0.0000123,"is_byok":false,"prompt_tokens_details":{"cached_tokens":8,"cache_write_tokens":0,"audio_tokens":0,"video_tokens":0},"cost_details":{"upstream_inference_cost":null,"upstream_inference_prompt_cost":0.00001,"upstream_inference_completions_cost":0.0000023},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0}}}`
	chatStream := ": OPENROUTER PROCESSING\n\n" +
		"data: {\"id\":\"gen-1\",\"provider\":\"OpenAI\",\"model\":\"openai/gpt-5.4-mini\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"},\"finish_reason\":null,\"native_finish_reason\":null,\"logprobs\":null}]}\n\n" +
		": OPENROUTER PROCESSING\n\n" +
		"data: {\"id\":\"gen-1\",\"provider\":\"OpenAI\",\"model\":\"openai/gpt-5.4-mini\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":\"stop\",\"native_finish_reason\":\"stop\",\"logprobs\":null}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":4,\"total_tokens\":24,\"cost\":0.00001,\"is_byok\":false,\"prompt_tokens_details\":{\"cached_tokens\":8,\"cache_write_tokens\":0},\"completion_tokens_details\":{\"reasoning_tokens\":0}}}\n\n" +
		"data: [DONE]\n\n"
	messagesJSON := `{"id":"gen-1730000000-def","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4.5","provider":"Anthropic","content":[{"type":"text","text":"Hi","citations":null}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":20,"output_tokens":4,"cache_creation_input_tokens":3,"cache_read_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":0},"service_tier":"standard","inference_geo":"not_available","server_tool_use":null,"output_tokens_details":null,"cost":0.00012,"is_byok":false,"cost_details":{"upstream_inference_cost":null},"iterations":null,"speed":null}}`
	messagesStream := func(deltaUsage string) string {
		return ": OPENROUTER PROCESSING\n\n" +
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"gen-1\",\"type\":\"message\",\"role\":\"assistant\",\"container\":null,\"content\":[],\"model\":\"anthropic/claude-sonnet-4.5\",\"stop_reason\":null,\"stop_details\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":20,\"output_tokens\":1,\"output_tokens_details\":null,\"cache_creation_input_tokens\":3,\"cache_read_input_tokens\":5,\"cache_creation\":{\"ephemeral_5m_input_tokens\":3,\"ephemeral_1h_input_tokens\":0},\"inference_geo\":null,\"server_tool_use\":null,\"service_tier\":\"standard\"}}}\n\n" +
			"event: ping\ndata: {\"type\":\"ping\"}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"container\":null,\"stop_details\":null,\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":" + deltaUsage + "}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	cases := []struct {
		name, provider, path, request, contentType, reply string
		input, output, cached, write                      int64
	}{
		{"chat JSON", "openai", "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}]}`, "application/json", chatJSON, 12, 4, 8, 0},
		{"chat stream", "openai", "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}],"stream":true}`, "text/event-stream", chatStream, 12, 4, 8, 0},
		{"messages JSON", "anthropic", "/anthropic/v1/messages", `{"model":"model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`, "application/json", messagesJSON, 20, 4, 5, 3},
		{"messages stream", "anthropic", "/anthropic/v1/messages", `{"model":"model","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`, "text/event-stream",
			messagesStream(`{"input_tokens":20,"output_tokens":4,"output_tokens_details":null,"cache_creation_input_tokens":3,"cache_read_input_tokens":5,"server_tool_use":null}`), 20, 4, 5, 3},
		{"messages stream with zero placeholders", "anthropic", "/anthropic/v1/messages", `{"model":"model","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`, "text/event-stream",
			messagesStream(`{"input_tokens":0,"output_tokens":4,"output_tokens_details":null,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"server_tool_use":null}`), 20, 4, 5, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			official := newRecorder(t, 529, "application/json", `{}`)
			openrouter := newRecorder(t, 200, tc.contentType, tc.reply)
			h, f, _ := backupFixture(t, tc.provider, official.server.URL, map[string]*recorder{"openrouter": openrouter})
			w := serve(h, tc.path, tc.request)
			if w.Code != 200 || w.Body.String() != tc.reply {
				t.Fatalf("client reply changed: %d %q", w.Code, w.Body.String())
			}
			s := f.settlements
			if len(s) != 1 || s[0].Status != "completed" || !s[0].Usage.Known || s[0].Usage.InputTokens != tc.input || s[0].Usage.OutputTokens != tc.output || s[0].Usage.CachedTokens != tc.cached || s[0].Usage.CacheWriteTokens != tc.write {
				t.Fatalf("settlement %+v", s)
			}
		})
	}
}

// OpenRouter reports a provider failure after HTTP 200 as an SSE chunk with
// an error object and finish_reason "error". The client receives it as sent;
// the request is free and is not replayed, because output had started.
func TestOpenRouterMidStreamErrorIsFreeAndFinal(t *testing.T) {
	events := ": OPENROUTER PROCESSING\n\n" +
		"data: {\"id\":\"gen-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"openai/gpt-5.4-mini\",\"provider\":\"OpenAI\",\"error\":{\"code\":\"server_error\",\"message\":\"Provider disconnected\"},\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":\"error\"}]}\n\n"
	official := newRecorder(t, 503, "application/json", `{}`)
	openrouter := newRecorder(t, 200, "text/event-stream", events)
	zenmux := newRecorder(t, 200, "application/json", chatReply)
	h, f, _ := backupFixture(t, "openai", official.server.URL, map[string]*recorder{"openrouter": openrouter, "zenmux": zenmux})
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[],"stream":true}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Provider disconnected") || zenmux.count() != 0 {
		t.Fatalf("response %d %q zenmux=%d", w.Code, w.Body.String(), zenmux.count())
	}
	if s := f.settlements; len(s) != 1 || s[0].Status != "failed" || s[0].ChargeMicros != 0 {
		t.Fatalf("settlement %+v", s)
	}
}

// Requests to a backup keep every client field RelayPine accepts, including
// tools, thinking, caching controls and beta headers, and drop only the
// official service tier.
func TestBackupRequestKeepsClientFields(t *testing.T) {
	official := newRecorder(t, 529, "application/json", `{}`)
	openrouter := newRecorder(t, 200, "application/json", messagesJSON())
	h, _, _ := backupFixture(t, "anthropic", official.server.URL, map[string]*recorder{"openrouter": openrouter})
	body := `{"model":"model","max_tokens":64,"system":[{"type":"text","text":"Be brief.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"weather?"}],"tools":[{"name":"get_weather","description":"Weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"tool_choice":{"type":"auto"},"thinking":{"type":"disabled"},"temperature":1,"metadata":{"user_id":"customer-1"}}`
	r := newRequest("/anthropic/v1/messages", body)
	r.Header.Set("Anthropic-Version", "2023-06-01")
	r.Header.Set("Anthropic-Beta", "fine-grained-tool-streaming-2025-05-14")
	w := serveRequest(h, r)
	if w.Code != 200 {
		t.Fatalf("response %d %s", w.Code, w.Body)
	}
	call := openrouter.last()
	for _, field := range []string{"system", "messages", "tools", "tool_choice", "thinking", "temperature", "metadata", "max_tokens"} {
		if call.body[field] == nil {
			t.Errorf("backup request lost %s", field)
		}
	}
	if call.body["service_tier"] != nil || str(call.body["model"]) != "anthropic/claude-sonnet-4.5" || call.version != "2023-06-01" || call.beta != "fine-grained-tool-streaming-2025-05-14" {
		t.Fatalf("backup request %+v", call)
	}
}

func messagesJSON() string {
	return `{"id":"gen-2","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4.5","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","usage":{"input_tokens":30,"output_tokens":5}}`
}

// Inline images are forwarded unchanged; a backup must receive the same
// image data when the official route refuses the request.
func TestImageRequestFailsOverWithImageIntact(t *testing.T) {
	data := handlerImageData(t)
	official := newRecorder(t, 529, "application/json", `{}`)
	openrouter := newRecorder(t, 200, "application/json", messagesJSON())
	h, f, _ := backupFixture(t, "anthropic", official.server.URL, map[string]*recorder{"openrouter": openrouter})
	w := serve(h, "/anthropic/v1/messages", handlerImageBody(data, false))
	if w.Code != 200 || official.count() != 1 || openrouter.count() != 1 {
		t.Fatalf("response %d official=%d backup=%d %s", w.Code, official.count(), openrouter.count(), w.Body)
	}
	call := openrouter.last()
	if !strings.Contains(string(call.body["messages"]), data) || str(call.body["model"]) != "anthropic/claude-sonnet-4.5" {
		t.Fatal("backup request lost or changed the inline image")
	}
	if len(f.settlements) != 1 || f.settlements[0].Status != "completed" {
		t.Fatalf("settlement %+v", f.settlements)
	}
}

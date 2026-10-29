package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func pngDataURL(t *testing.T) (string, int) {
	t.Helper()
	data := base64.StdEncoding.EncodeToString(fixtureImage(t, "png", 40, 30))
	return "data:image/png;base64," + data, len(data)
}

// The per-image bound follows OpenAI's sizing rules for each model and detail
// level, plus one token for rounding. Undocumented models get the 30,000-patch
// cap at 1.2 tokens per patch.
func TestOpenAIImageTokenBounds(t *testing.T) {
	for _, c := range []struct {
		model, detail string
		want          int64
	}{
		{"gpt-6-astra", "auto", 36001}, {"gpt-6-astra-2026-05-01", "low", 309}, {"gpt-5.6-terra", "high", 3001}, {"gpt-5.6-sol", "original", 36001},
		{"gpt-5.5", "original", 12001}, {"gpt-5.5", "auto", 12001}, {"gpt-5.5-pro", "auto", 36001},
		{"gpt-5.4", "auto", 3001}, {"gpt-5.4-mini", "low", 4917}, {"gpt-5.4-nano", "original", 12001},
		{"gpt-5.2", "high", 4917}, {"gpt-4.1-mini", "low", 6637}, {"gpt-4o-mini", "high", 48170}, {"gpt-4o-mini", "low", 2834},
		{"gpt-4o", "low", 86}, {"gpt-4.1", "auto", 1446}, {"gpt-5", "high", 1191}, {"gpt-5.1", "low", 71}, {"o3", "high", 1276},
		{"gpt-5-mini", "low", 36001}, {"gpt-5-nano", "auto", 45001},
		{"gpt-6.1-terra", "low", 36001}, {"gpt-6-sol", "high", 36001}, {"gpt-5.6-cyber", "low", 36001}, {"", "auto", 36001},
	} {
		if got, ok := openAIImageTokenBound(c.model, c.detail); !ok || got != c.want {
			t.Errorf("%s %s: %d %v, want %d", c.model, c.detail, got, ok, c.want)
		}
	}
	if _, ok := openAIImageTokenBound("gpt-5.4", "medium"); ok {
		t.Fatal("unknown detail accepted")
	}
}

func TestOpenAIImageInputsInChatAndResponses(t *testing.T) {
	dataURL, base64Bytes := pngDataURL(t)
	chat := `[{"role":"system","content":"Describe images."},{"role":"user","content":[{"type":"text","text":"What is this?"},` +
		`{"type":"image_url","image_url":{"url":"` + dataURL + `"}},{"type":"image_url","image_url":{"url":"https://example.com/a.jpg","detail":"low"}}]}]`
	m := map[string]json.RawMessage{"messages": json.RawMessage(chat)}
	summary, err := validatePayloadWithImages(m, "/v1/chat/completions", "gpt-5.4")
	if err != nil || summary.OpenAIImages != 2 || summary.OpenAITokens != 3001+4917 || summary.Base64Bytes != int64(base64Bytes) {
		t.Fatalf("chat summary %+v %v", summary, err)
	}
	if string(m["messages"]) != chat {
		t.Fatal("validation changed the client's messages")
	}
	input := `[{"role":"user","content":[{"type":"input_text","text":"Compare"},{"type":"input_image","image_url":"` + dataURL + `","detail":"high"},{"type":"input_image","image_url":"https://example.com/b.png"}]},` +
		`{"type":"function_call","call_id":"c1","name":"screenshot","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":[{"type":"input_image","image_url":"` + dataURL + `","detail":"low"}]}]`
	summary, err = validatePayloadWithImages(map[string]json.RawMessage{"input": json.RawMessage(input)}, "/v1/responses", "gpt-6-astra")
	if err != nil || summary.OpenAIImages != 3 || summary.OpenAITokens != 3001+36001+309 || summary.Base64Bytes != 2*int64(base64Bytes) {
		t.Fatalf("responses summary %+v %v", summary, err)
	}
	jpeg := "data:image/png;base64," + base64.StdEncoding.EncodeToString(fixtureImage(t, "jpeg", 8, 8))
	chatImage := func(part string) string {
		return `[{"role":"user","content":[` + part + `]}]`
	}
	for _, c := range []struct{ path, field, value, want string }{
		{"/v1/chat/completions", "messages", chatImage(`{"type":"image_url","image_url":{"url":"http://example.com/a.png"}}`), "https URLs or base64 data URLs"},
		{"/v1/chat/completions", "messages", chatImage(`{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"medium"}}`), "image detail must be"},
		{"/v1/chat/completions", "messages", chatImage(`{"type":"image_url","image_url":{"url":"https://example.com/a.png","file_id":"file-x"}}`), "file_id references are not supported"},
		{"/v1/chat/completions", "messages", chatImage(`{"type":"image_url","image_url":{"url":"https://example.com/a.png"},"extra":1}`), "unsupported image field: extra"},
		{"/v1/chat/completions", "messages", chatImage(`{"type":"image_url","image_url":{"url":"` + jpeg + `"}}`), "does not match its image type"},
		{"/v1/chat/completions", "messages", `[{"role":"system","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]`, "only in user message content"},
		{"/v1/chat/completions", "messages", `[{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]`, "only in user message content"},
		{"/v1/chat/completions", "messages", chatImage(strings.TrimSuffix(strings.Repeat(`{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},`, 21), ",")), "20 image limit"},
		{"/v1/responses", "input", `[{"role":"user","content":[{"type":"input_image","file_id":"file-private"}]}]`, "file_id references are not supported"},
		{"/v1/responses", "input", `[{"role":"assistant","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]`, "only in user message content"},
		{"/v1/responses", "input", `[{"type":"message","role":"developer","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]`, "only in user message content"},
		{"/v1/responses", "input", `[{"type":"function_call","call_id":"c1","name":"f","arguments":"{}","image_url":"https://example.com/a.png"}]`, "unsupported content reference"},
	} {
		_, err := validatePayloadWithImages(map[string]json.RawMessage{c.field: json.RawMessage(c.value)}, c.path, "gpt-5.4")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %.100s: %v, want %q", c.path, c.value, err, c.want)
		}
	}
}

func TestOpenAIImageChatRoundTripReservesTheBoundAndSettlesUsage(t *testing.T) {
	up := newRecorder(t, 200, "application/json", `{"id":"chat_1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"A cat."}}],"usage":{"prompt_tokens":1500,"completion_tokens":6,"prompt_tokens_details":{"image_tokens":1400,"text_tokens":100}}}`)
	h, f := fixture("openai", up.server.URL)
	dataURL, base64Bytes := pngDataURL(t)
	messages := `[{"role":"user","content":[{"type":"text","text":"What is this?"},{"type":"image_url","image_url":{"url":"` + dataURL + `","detail":"high"}},{"type":"image_url","image_url":{"url":"https://example.com/cat.jpg"}}]}]`
	body := `{"model":"model","messages":` + messages + `}`
	w := serve(h, "/openai/v1/chat/completions", body)
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	if sent := up.calls[0].body["messages"]; !bytes.Equal(sent, []byte(messages)) {
		t.Fatalf("messages changed on the way upstream: %.200s", sent)
	}
	// The fixture's model is undocumented, so each image reserves 36,001 tokens.
	input := 2*int64(len(body)-base64Bytes) + 4096 + 2*36001
	want, wantCost, _ := estimate(input, 128, f.models[0])
	if r := f.reservations[0]; r.Request.ReservedMicros != want || r.CostReserve != wantCost {
		t.Fatalf("reserved %d/%d, want %d/%d", r.Request.ReservedMicros, r.CostReserve, want, wantCost)
	}
	if s := f.settlements[0]; s.Status != "completed" || s.Usage != (domain.Usage{InputTokens: 1500, OutputTokens: 6, Known: true}) {
		t.Fatalf("settlement %+v", s)
	}
}

func TestOpenAIImageResponsesRoundTripAndContextLimit(t *testing.T) {
	up := newRecorder(t, 200, "application/json", `{"id":"resp_img","status":"completed","output":[],"usage":{"input_tokens":900,"output_tokens":4}}`)
	h, f := fixture("openai", up.server.URL)
	input := `[{"role":"user","content":[{"type":"input_text","text":"Describe"},{"type":"input_image","image_url":"https://example.com/a.png","detail":"low"}]}]`
	w := serve(h, "/openai/v1/responses", `{"model":"model","input":`+input+`}`)
	if w.Code != 200 || !bytes.Equal(up.calls[0].body["input"], []byte(input)) {
		t.Fatalf("HTTP %d %s; upstream input %.200s", w.Code, w.Body.String(), up.calls[0].body["input"])
	}
	if s := f.settlements[0]; s.Status != "completed" || s.Usage.InputTokens != 900 || s.Usage.OutputTokens != 4 {
		t.Fatalf("settlement %+v", s)
	}
	// Three undocumented-model images reserve 108,003 tokens, more than a
	// 100,000-token standard price context allows.
	f.models[0].PricingReference = &domain.PriceReference{MaxInputTokens: 100000}
	three := `[{"role":"user","content":[` + strings.TrimSuffix(strings.Repeat(`{"type":"input_image","image_url":"https://example.com/a.png"},`, 3), ",") + `]}]`
	w = serve(h, "/openai/v1/responses", `{"model":"model","input":`+three+`}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "context limit") || len(f.reservations) != 1 {
		t.Fatalf("HTTP %d %s, reservations %d", w.Code, w.Body.String(), len(f.reservations))
	}
}

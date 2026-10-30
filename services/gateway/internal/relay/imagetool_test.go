package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestImageToolValidation(t *testing.T) {
	h, f := imageFixture("http://127.0.0.1:1")
	tool := func(fields string) string {
		return `{"model":"model","input":"draw a cat","tools":[{"type":"image_generation"` + fields + `}]}`
	}
	for _, c := range []struct{ path, body, want string }{
		{"/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"x"}],"tools":[{"type":"image_generation"}]}`, "supported only by the Responses API"},
		{"/openai/v1/responses", tool(`,"model":"gpt-image-1.5"`), "image model gpt-image-1.5 is not available to this key"},
		{"/openai/v1/responses", tool(`,"foo":1`), "unsupported image generation tool field: foo"},
		{"/openai/v1/responses", tool(`,"input_image_mask":{"image_url":"data:image/png;base64,AAAA"}`), "masks are not supported"},
		{"/openai/v1/responses", tool(`,"input_fidelity":"high"`), "input_fidelity is not supported"},
		{"/openai/v1/responses", tool(`,"quality":"xhigh"`), "quality must be one of low, medium, high, auto"},
		{"/openai/v1/responses", tool(`,"size":"4096x1024"`), "at most 3840"},
		{"/openai/v1/responses", tool(`,"model":"image25","background":"transparent","output_format":"jpeg"`), "transparent backgrounds need png or webp"},
		{"/openai/v1/responses", tool(`,"background":"transparent"`), "gpt-image-2 does not support transparent backgrounds"},
		{"/openai/v1/responses", `{"model":"model","input":"x","max_tool_calls":5,"tools":[{"type":"image_generation"}]}`, "max_tool_calls must be between 1 and 4"},
		{"/openai/v1/responses", `{"model":"model","input":"x","multi_agent":{"enabled":true},"tools":[{"type":"image_generation"}]}`, "multi-agent requests cannot use the image generation tool"},
		{"/openai/v1/responses", `{"model":"model","input":"x","tools":[{"type":"image_generation"},{"type":"image_generation"}]}`, "declare the image generation tool once"},
		{"/openai/v1/responses", `{"model":"model","input":"x","tools":[{"type":"web_search"}]}`, "built-in tools are not supported"},
	} {
		w := serveRequest(h, newRequest(c.path, c.body))
		if w.Code != 400 || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%.90s: HTTP %d %s, want %q", c.body, w.Code, w.Body.String(), c.want)
		}
	}
	if len(f.reservations) != 0 {
		t.Fatalf("an invalid image tool request was reserved: %+v", f.reservations)
	}
}

// responseWithImage is a Responses reply with one generated image.
func responseWithImage(image, toolUsage string) string {
	usage := `,"usage":{"input_tokens":50,"output_tokens":20,"total_tokens":70}`
	if toolUsage != "" {
		usage += `,"tool_usage":{"image_gen":` + toolUsage + `}`
	}
	return `{"id":"resp_image","object":"response","status":"completed","model":"upstream-model","output":[` +
		`{"type":"image_generation_call","id":"ig_1","status":"completed","action":"generate","size":"1024x1024","quality":"low","revised_prompt":"A cat","result":"` + image + `"},` +
		`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Here is your cat."}]}]` + usage + `}`
}

func TestImageToolForwardsPinnedModelAndSettlesBothModels(t *testing.T) {
	image := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 2<<20))
	reply := responseWithImage(image, `{"input_tokens":30,"input_tokens_details":{"text_tokens":30,"image_tokens":0},"output_tokens":196,"total_tokens":226}`)
	up := newImageUpstream(t, "application/json", reply)
	h, f := imageFixture(up.server.URL)
	body := `{"model":"model","input":"draw a cat","tools":[{"type":"image_generation","quality":"low","size":"1024x1024"}]}`
	w := serveRequest(h, newRequest("/openai/v1/responses", body))
	if w.Code != 200 || w.Body.String() != reply {
		t.Fatalf("HTTP %d, %d bytes relayed", w.Code, w.Body.Len())
	}
	var sent struct {
		Tools []struct {
			Model string `json:"model"`
		} `json:"tools"`
		MaxToolCalls int `json:"max_tool_calls"`
	}
	if json.Unmarshal(up.bodies[0], &sent) != nil || len(sent.Tools) != 1 || sent.Tools[0].Model != "gpt-image-2" || sent.MaxToolCalls != 1 {
		t.Fatalf("upstream body %.300s", up.bodies[0])
	}
	// The text model's worst case plus one image call's: 32,768 text tokens,
	// four context images and 245 output tokens (196 with a 25% margin).
	input := int64(2*len(body) + 4096)
	text := (input*1_250_000 + 128*2_000_000 + 999_999) / 1_000_000
	tool := int64(32768*5_000_000+4*imageInputTokenBound*8_000_000+245*30_000_000+999_999) / 1_000_000
	if got := f.reservations[0].Request.ReservedMicros; got != text+tool {
		t.Fatalf("reserved %d, want %d", got, text+tool)
	}
	s := f.settlements[0]
	want := domain.Usage{InputTokens: 50, OutputTokens: 20, ImageTextTokens: 30, ImageOutputTokens: 196, Known: true}
	// Text model: 50 and 20 tokens at $1 and $2. Image model: 30 text tokens
	// at $5 and 196 image tokens at $30.
	if s.Status != "completed" || s.Usage != want || s.ChargeMicros != 90+6030 || s.CostMicros != 45+6030 {
		t.Fatalf("settlement %+v", s)
	}
	stored := f.payloads[w.Header().Get("X-Request-Id")].Response.Body
	if strings.Contains(stored, image[:64]) || !strings.Contains(stored, "[image omitted:") || !strings.Contains(stored, "Here is your cat.") {
		t.Fatalf("inspection copy %.300s", stored)
	}
}

func TestImageToolBillsAtLeastTheCalculatorAndTheWorstCaseWithoutAReport(t *testing.T) {
	for _, c := range []struct {
		name, toolUsage     string
		text, image, output int64
	}{
		// A report below OpenAI's calculator for the generated image is raised.
		{"undercounted", `{"input_tokens":30,"output_tokens":100}`, 0, 30, 196},
		// No report: the worst case for the image's size and quality, the
		// revised prompt plus the instruction allowance as text input.
		{"missing", "", int64(len("A cat")) + imageTextAllowance, 0, 245},
	} {
		t.Run(c.name, func(t *testing.T) {
			up := newImageUpstream(t, "application/json", responseWithImage("QUJD", c.toolUsage))
			h, f := imageFixture(up.server.URL)
			serveRequest(h, newRequest("/openai/v1/responses", `{"model":"model","input":"draw a cat","tools":[{"type":"image_generation","quality":"low","size":"1024x1024"}]}`))
			u := f.settlements[0].Usage
			if u.ImageTextTokens != c.text || u.ImageInputTokens != c.image || u.ImageOutputTokens != c.output {
				t.Fatalf("image tool usage %+v", u)
			}
		})
	}
}

func TestImageToolUsesTheNamedModelAndCallBound(t *testing.T) {
	up := newImageUpstream(t, "application/json", responseWithImage("QUJD", `{"input_tokens":1,"output_tokens":1}`))
	h, f := imageFixture(up.server.URL)
	body := `{"model":"model","input":"x","max_tool_calls":3,"tools":[{"type":"image_generation","model":"image25","quality":"high","size":"1024x1536"}]}`
	serveRequest(h, newRequest("/openai/v1/responses", body))
	if !strings.Contains(string(up.bodies[0]), `"model":"gpt-image-2.5-sunburst-2026-09-08"`) || !strings.Contains(string(up.bodies[0]), `"max_tool_calls":3`) {
		t.Fatalf("upstream body %.300s", up.bodies[0])
	}
	// GPT Image 2.5 high at 1024x1536 is 1,372 tokens, doubled, three calls.
	input := int64(2*len(body) + 4096)
	text := (input*1_250_000 + 128*2_000_000 + 999_999) / 1_000_000
	tool := int64(3*32768*5_000_000+3*4*imageInputTokenBound*8_000_000+3*2744*30_000_000+999_999) / 1_000_000
	if got := f.reservations[0].Request.ReservedMicros; got != text+tool {
		t.Fatalf("reserved %d, want %d", got, text+tool)
	}
}

func TestImageToolStreamCountsPartialImages(t *testing.T) {
	partial := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 2<<20))
	final := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 2<<20))
	completed := responseWithImage(final, `{"input_tokens":30,"input_tokens_details":{"text_tokens":30,"image_tokens":0},"output_tokens":196,"total_tokens":226}`)
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_image\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.image_generation_call.partial_image\ndata: {\"type\":\"response.image_generation_call.partial_image\",\"item_id\":\"ig_1\",\"partial_image_index\":0,\"partial_image_b64\":\"" + partial + "\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + completed + "}\n\n"
	up := newImageUpstream(t, "text/event-stream", stream)
	h, f := imageFixture(up.server.URL)
	w := serveRequest(h, newRequest("/openai/v1/responses", `{"model":"model","input":"draw a cat","stream":true,"tools":[{"type":"image_generation","quality":"low","size":"1024x1024","partial_images":1}]}`))
	if w.Code != 200 || w.Body.String() != stream {
		t.Fatalf("HTTP %d, %d bytes relayed, want %d", w.Code, w.Body.Len(), len(stream))
	}
	s := f.settlements[0]
	// 196 reported, raised to 196 + 100 for the partial image it delivered.
	if s.Status != "completed" || s.Usage.ImageOutputTokens != 296 || s.Usage.ImageTextTokens != 30 || s.Usage.InputTokens != 50 {
		t.Fatalf("settlement %+v", s)
	}
	stored := f.payloads[w.Header().Get("X-Request-Id")].Response.Body
	if strings.Contains(stored, partial[:64]) || strings.Contains(stored, final[:64]) || strings.Count(stored, "[image omitted:") != 2 {
		t.Fatalf("stream inspection copy %.400s", stored)
	}
}

func TestCompactImageJSONDropsOnlyImageData(t *testing.T) {
	long := strings.Repeat("A", 2048)
	text := strings.Repeat("words ", 400)
	in := `{"result":"` + long + `","short":{"b64_json":"QUJD"},"nested":[{"partial_image_b64":"` + long + `","note":"a \"quoted\" : value"}],"text":"` + text + `","b64_json":"` + long + `"}`
	out := compactImageJSON([]byte(in))
	if !json.Valid(out) || strings.Contains(string(out), long) || strings.Count(string(out), "[image omitted: 2048 bytes]") != 3 || !strings.Contains(string(out), text) || !strings.Contains(string(out), `"b64_json":"QUJD"`) || !strings.Contains(string(out), `a \"quoted\" : value`) {
		t.Fatalf("compacted %s", out)
	}
}

// An edit may read every image the request carries, beyond the four allowed
// for earlier responses.
func TestImageToolHoldCoversTheRequestImages(t *testing.T) {
	up := newImageUpstream(t, "application/json", responseWithImage("QUJD", `{"input_tokens":1,"output_tokens":1}`))
	h, f := imageFixture(up.server.URL)
	img := `{"type":"input_image","image_url":"https://example.com/a.png","detail":"high"}`
	body := `{"model":"model","input":[{"role":"user","content":[{"type":"input_text","text":"edit these"},` + img + `,` + img + `]}],"tools":[{"type":"image_generation","quality":"low","size":"1024x1024"}]}`
	w := serveRequest(h, newRequest("/openai/v1/responses", body))
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	bound, _ := openAIImageTokenBound("upstream-model", "high")
	input := int64(2*len(body)+4096) + 2*bound
	text := (input*1_250_000 + 128*2_000_000 + 999_999) / 1_000_000
	tool := int64(32768*5_000_000+(4+2)*imageInputTokenBound*8_000_000+245*30_000_000+999_999) / 1_000_000
	if got := f.reservations[0].Request.ReservedMicros; got != text+tool {
		t.Fatalf("reserved %d, want %d", got, text+tool)
	}
}

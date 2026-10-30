package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

// GPT Image 2 at its official reference price, plus a GPT Image 2.5 model.
func imageFixture(upstream string) (*Handler, *fakeRepo) {
	h, f := fixture("openai", upstream)
	priced := func(id, upstream string) domain.Model {
		return domain.Model{ID: id, Name: id, UpstreamModel: upstream, Provider: "openai", Enabled: true, Kind: domain.ModelKindImage, MaxOutputTokens: 1, Version: 1,
			InputRate: 5_000_000, CachedRate: 1_250_000, ImageInputRate: 8_000_000, ImageOutputRate: 30_000_000,
			CostInputRate: 5_000_000, CostCachedRate: 1_250_000, CostImageInputRate: 8_000_000, CostImageOutputRate: 30_000_000}
	}
	f.models = append(f.models, priced("image", "gpt-image-2"), priced("image25", "gpt-image-2.5-sunburst-2026-09-08"))
	return h, f
}

// imageUpstream records each call, including multipart bodies, and answers
// with a fixed reply.
type imageUpstream struct {
	mu                  sync.Mutex
	paths, contentTypes []string
	bodies              [][]byte
	server              *httptest.Server
}

func newImageUpstream(t *testing.T, contentType, reply string) *imageUpstream {
	u := &imageUpstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.paths, u.contentTypes, u.bodies = append(u.paths, r.URL.Path), append(u.contentTypes, r.Header.Get("Content-Type")), append(u.bodies, raw)
		u.mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(u.server.Close)
	return u
}

// The calculator reproduces OpenAI's GPT Image 2 per-image prices at $30 per
// million output tokens: low 1024x1024 196 ($0.006), medium 1,756 ($0.053),
// high 7,024 ($0.211), and 1024x1536 158 / 1,372 / 5,488 ($0.005 / 0.041 / 0.165).
func TestImageOutputTokensMatchOpenAICalculator(t *testing.T) {
	for _, c := range []struct {
		cells, width, height int
		want                 int64
	}{
		{16, 1024, 1024, 196}, {48, 1024, 1024, 1756}, {96, 1024, 1024, 7024},
		{16, 1024, 1536, 158}, {48, 1024, 1536, 1372}, {96, 1024, 1536, 5488}, {96, 1536, 1024, 5488},
		{16, 3840, 2160, 371}, {24, 1024, 1024, 439}, {64, 1024, 1024, 3122}, {64, 3840, 2160, 5930}, {96, 3840, 2160, 13342},
	} {
		if got := imageOutputTokens(c.cells, c.width, c.height); got != c.want {
			t.Errorf("%d cells %dx%d: %d tokens, want %d", c.cells, c.width, c.height, got, c.want)
		}
	}
	for cells, want := range map[int]int64{16: 659, 24: 1483, 48: 5930, 64: 10542, 96: 23719} {
		if got := maxImageOutputTokens(cells); got != want {
			t.Errorf("auto size at %d cells: %d tokens, want %d", cells, got, want)
		}
	}
}

func TestImageRequestValidation(t *testing.T) {
	h, f := imageFixture("http://127.0.0.1:1")
	png := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...))
	jpeg := base64.StdEncoding.EncodeToString(append([]byte{0xff, 0xd8, 0xff, 0xe0}, make([]byte, 32)...))
	gen, edit := "/openai/v1/images/generations", "/openai/v1/images/edits"
	for _, c := range []struct{ path, body, want string }{
		{gen, `{"model":"image"}`, "prompt is required"},
		{gen, `{"model":"image","prompt":"x","response_format":"url"}`, "always return base64"},
		{gen, `{"model":"image","prompt":"x","style":"vivid"}`, "unsupported image parameter: style"},
		{gen, `{"model":"image","prompt":"x","n":11}`, "n must be between 1 and 10"},
		{gen, `{"model":"image","prompt":"x","n":1.5}`, "n must be between 1 and 10"},
		{gen, `{"model":"image","prompt":"x","stream":true,"n":2}`, "set n to 1"},
		{gen, `{"model":"image","prompt":"x","partial_images":2}`, "partial_images requires stream"},
		{gen, `{"model":"image","prompt":"x","stream":true,"partial_images":4}`, "partial_images must be between 0 and 3"},
		{gen, `{"model":"image","prompt":"x","quality":"xhigh"}`, "quality must be one of low, medium, high, auto"},
		{gen, `{"model":"image25","prompt":"x","quality":"hd"}`, "quality must be one of low, medium, high, xhigh, max, auto"},
		{gen, `{"model":"image","prompt":"x","size":"1000x1000"}`, "multiples of 16"},
		{gen, `{"model":"image","prompt":"x","size":"4096x1024"}`, "at most 3840"},
		{gen, `{"model":"image","prompt":"x","size":"512x512"}`, "between 655,360"},
		{gen, `{"model":"image","prompt":"x","size":"3072x1008"}`, "between 1:3 and 3:1"},
		{gen, `{"model":"image","prompt":"x","size":"1024x1024 "}`, "WIDTHxHEIGHT"},
		{gen, `{"model":"image25","prompt":"x","background":"transparent","output_format":"jpeg"}`, "transparent backgrounds need png or webp"},
		{gen, `{"model":"image","prompt":"x","background":"transparent"}`, "gpt-image-2 does not support transparent backgrounds"},
		{edit, `{"model":"image","prompt":"x","background":"transparent","images":[{"image_url":"data:image/png;base64,` + png + `"}]}`, "gpt-image-2 does not support transparent backgrounds"},
		{gen, `{"model":"image","prompt":"x","images":[]}`, "only by image edits"},
		{gen, `{"model":"model","prompt":"x"}`, "does not generate images"},
		{gen, `{"model":"image","prompt":"` + strings.Repeat("あ", 32001) + `"}`, "exceeds 32,000 characters"},
		{edit, `{"model":"image","prompt":"x","images":[{"image_url":"https://example.com/a.png"}]}`, "remote image URLs are not supported"},
		{edit, `{"model":"image","prompt":"x","images":[{"file_id":"file-abc"}]}`, "file_id references are not supported"},
		{edit, `{"model":"image","prompt":"x","images":[{"image_url":"data:image/png;base64,` + jpeg + `"}]}`, "does not match its image type"},
		{edit, `{"model":"image","prompt":"x","images":[{"image_url":"data:image/png;base64,` + png + `"}],"mask":{"image_url":"data:image/jpeg;base64,` + jpeg + `"}}`, "the mask must be a PNG image"},
		{edit, `{"model":"image","prompt":"x","input_fidelity":"high","images":[{"image_url":"data:image/png;base64,` + png + `"}]}`, "input_fidelity is not supported"},
		{edit, `{"model":"image","prompt":"x","images":[]}`, "between 1 and 16 images"},
		{"/openai/v1/chat/completions", `{"model":"image","messages":[{"role":"user","content":"x"}]}`, "served by the Images API"},
	} {
		w := serveRequest(h, newRequest(c.path, c.body))
		if w.Code != 400 || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s %.80s: HTTP %d %s, want 400 %q", c.path, c.body, w.Code, w.Body.String(), c.want)
		}
	}
	if len(f.reservations) != 0 || len(f.settlements) != 0 {
		t.Fatalf("an invalid image request was reserved: %+v", f.reservations)
	}
}

func TestImageReservationIsTheDocumentedWorstCase(t *testing.T) {
	reply := `{"created":1,"data":[{"b64_json":"QUJD"}],"usage":{"input_tokens":10,"output_tokens":20}}`
	for _, c := range []struct {
		name, body          string
		textInput, imageOut int64
	}{
		// 7,024 tokens per high 1024x1024 image, 25% margin, two images.
		{"gpt-image-2 high", `{"model":"image","prompt":"a lighthouse","size":"1024x1024","quality":"high","n":2}`, 12 + imageTextAllowance, 2 * 8780},
		// Automatic size and quality on 2.5: 23,719 tokens doubled.
		{"gpt-image-2.5 auto", `{"model":"image25","prompt":"a lighthouse"}`, 12 + imageTextAllowance, 47438},
		// One streamed image with three partial images.
		{"stream partials", `{"model":"image","prompt":"a lighthouse","size":"1024x1536","quality":"low","stream":true,"partial_images":3}`, 12 + imageTextAllowance, 198 + 300},
	} {
		t.Run(c.name, func(t *testing.T) {
			up := newImageUpstream(t, "application/json", reply)
			h, f := imageFixture(up.server.URL)
			serveRequest(h, newRequest("/openai/v1/images/generations", c.body))
			if len(f.reservations) != 1 {
				t.Fatalf("reservations %+v", f.reservations)
			}
			wantSell := (c.textInput*5_000_000 + c.imageOut*30_000_000 + 999_999) / 1_000_000
			if got := f.reservations[0].Request.ReservedMicros; got != wantSell || f.reservations[0].CostReserve != wantSell {
				t.Fatalf("reserved %d (cost %d), want %d", got, f.reservations[0].CostReserve, wantSell)
			}
		})
	}
}

func TestImageGenerationRelaysAndSettlesUsage(t *testing.T) {
	image := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, 3<<20))
	reply := `{"created":1730000000,"background":"opaque","data":[{"b64_json":"` + image + `"}],"output_format":"png","quality":"high","size":"1024x1024","usage":{"input_tokens":30,"input_tokens_details":{"text_tokens":20,"image_tokens":10},"output_tokens":7024,"total_tokens":7054}}`
	up := newImageUpstream(t, "application/json", reply)
	h, f := imageFixture(up.server.URL)
	w := serveRequest(h, newRequest("/openai/v1/images/generations", `{"model":"image","prompt":"a lighthouse","size":"1024x1024","quality":"high"}`))
	if w.Code != 200 || w.Body.String() != reply {
		t.Fatalf("HTTP %d, %d bytes relayed, want the %d byte reply unchanged", w.Code, w.Body.Len(), len(reply))
	}
	sent := object(up.bodies[0])
	if up.paths[0] != "/v1/images/generations" || up.contentTypes[0] != "application/json" || str(sent["model"]) != "gpt-image-2" || str(sent["quality"]) != "high" {
		t.Fatalf("upstream got %s %s %s", up.paths[0], up.contentTypes[0], up.bodies[0])
	}
	s := f.settlements[0]
	want := domain.Usage{InputTokens: 20, ImageInputTokens: 10, ImageOutputTokens: 7024, Known: true}
	if s.Status != "completed" || s.Usage != want {
		t.Fatalf("settlement %+v", s)
	}
	// 20 text input at $5, 10 image input at $8 and 7,024 image output at $30.
	if s.ChargeMicros != 210_900 || s.CostMicros != 210_900 {
		t.Fatalf("charge %d cost %d, want 210900", s.ChargeMicros, s.CostMicros)
	}
	payload := f.payloads[w.Header().Get("X-Request-Id")]
	stored := payload.Response.Body
	if strings.Contains(stored, image[:64]) || !strings.Contains(stored, "[image omitted]") || !strings.Contains(stored, `"output_tokens":7024`) || payload.Response.Bytes != len(reply) {
		t.Fatalf("inspection copy kept image data or lost usage: %d bytes %.300s", payload.Response.Bytes, stored)
	}
}

func TestImageStreamRelaysPartialsAndSettlesCompletedUsage(t *testing.T) {
	// Each image event is larger than the 1 MiB limit for text frames.
	partial := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 2<<20))
	final := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 2<<20))
	stream := "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"" + partial + "\",\"partial_image_index\":0}\n\n" +
		"event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"" + final + "\",\"usage\":{\"input_tokens\":12,\"input_tokens_details\":{\"text_tokens\":12,\"image_tokens\":0},\"output_tokens\":296,\"total_tokens\":308}}\n\n"
	up := newImageUpstream(t, "text/event-stream", stream)
	h, f := imageFixture(up.server.URL)
	w := serveRequest(h, newRequest("/openai/v1/images/generations", `{"model":"image","prompt":"a lighthouse","size":"1024x1024","quality":"low","stream":true,"partial_images":1}`))
	if w.Code != 200 || w.Body.String() != stream {
		t.Fatalf("HTTP %d, %d bytes relayed, want the %d byte stream unchanged", w.Code, w.Body.Len(), len(stream))
	}
	s := f.settlements[0]
	// Stream usage has no output split, so all output is image output.
	if s.Status != "completed" || s.Usage != (domain.Usage{InputTokens: 12, ImageOutputTokens: 296, Known: true}) || s.ChargeMicros != 8_940 {
		t.Fatalf("settlement %+v", s)
	}
	stored := f.payloads[w.Header().Get("X-Request-Id")].Response.Body
	if strings.Contains(stored, partial[:64]) || strings.Contains(stored, final[:64]) || strings.Count(stored, "[image omitted") != 2 || !strings.Contains(stored, `"output_tokens":296`) {
		t.Fatalf("stream inspection copy: %.400s", stored)
	}
}

func TestImageStreamWithoutCompletionIsFreeAndKeepsTheReservation(t *testing.T) {
	stream := "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"QUJD\",\"partial_image_index\":0}\n\n"
	up := newImageUpstream(t, "text/event-stream", stream)
	h, f := imageFixture(up.server.URL)
	serveRequest(h, newRequest("/openai/v1/images/generations", `{"model":"image","prompt":"x","stream":true,"partial_images":1}`))
	if s := f.settlements[0]; s.Status != "failed" || s.Usage.Known || s.ChargeMicros != 0 {
		t.Fatalf("settlement %+v", s)
	}
}

func TestImageReplyWithoutUsageKeepsTheReservation(t *testing.T) {
	up := newImageUpstream(t, "application/json", `{"created":1,"data":[{"b64_json":"QUJD"}]}`)
	h, f := imageFixture(up.server.URL)
	w := serveRequest(h, newRequest("/openai/v1/images/generations", `{"model":"image","prompt":"x"}`))
	if s := f.settlements[0]; w.Code != 200 || s.Status != "completed" || s.Usage.Known {
		t.Fatalf("HTTP %d settlement %+v", w.Code, s)
	}
}

func TestImageEditMultipartIsRebuiltWithNeutralFiles(t *testing.T) {
	reply := `{"created":1,"data":[{"b64_json":"QUJD"}],"usage":{"input_tokens":400,"input_tokens_details":{"text_tokens":10,"image_tokens":390},"output_tokens":196,"total_tokens":596}}`
	up := newImageUpstream(t, "application/json", reply)
	h, f := imageFixture(up.server.URL)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 4096)...)
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{8}, 4096)...)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for _, field := range [][2]string{{"model", "image"}, {"prompt", "add a red hat"}, {"size", "1024x1024"}, {"quality", "low"}, {"n", "1"}} {
		_ = form.WriteField(field[0], field[1])
	}
	for _, file := range []struct {
		field, name string
		data        []byte
	}{{"image[]", "../../etc/secret.png", png}, {"image[]", "photo.jpg", jpeg}, {"mask", "mask.png", png}} {
		part, _ := form.CreateFormFile(file.field, file.name)
		_, _ = part.Write(file.data)
	}
	_ = form.Close()
	size := body.Len()
	r := httptest.NewRequest("POST", "/openai/v1/images/edits", &body)
	r.Header.Set("Authorization", "Bearer rp_test")
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := serveRequest(h, r)
	if w.Code != 200 || w.Body.String() != reply {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	_, params, err := mime.ParseMediaType(up.contentTypes[0])
	if up.paths[0] != "/v1/images/edits" || err != nil {
		t.Fatalf("upstream got %s %s", up.paths[0], up.contentTypes[0])
	}
	reader := multipart.NewReader(bytes.NewReader(up.bodies[0]), params["boundary"])
	fields, files := map[string]string{}, []string{}
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(part)
		if part.FileName() == "" {
			fields[part.FormName()] = string(data)
			continue
		}
		files = append(files, part.FormName()+" "+part.FileName()+" "+part.Header.Get("Content-Type"))
		if !bytes.Equal(data, png) && !bytes.Equal(data, jpeg) {
			t.Fatal("an image changed on the way upstream")
		}
	}
	if fields["model"] != "gpt-image-2" || fields["prompt"] != "add a red hat" || fields["n"] != "1" || fields["quality"] != "low" {
		t.Fatalf("upstream fields %v", fields)
	}
	if strings.Join(files, "; ") != "image[] image-1.png image/png; image[] image-2.jpeg image/jpeg; mask image-3.png image/png" {
		t.Fatalf("upstream files %v", files)
	}
	// Three input images (two images and the mask) at the input bound.
	if f.reservations[0].Request.ReservedMicros != ((13+imageTextAllowance)*5_000_000+3*imageInputTokenBound*8_000_000+245*30_000_000+999_999)/1_000_000 {
		t.Fatalf("reservation %d", f.reservations[0].Request.ReservedMicros)
	}
	stored := f.payloads[w.Header().Get("X-Request-Id")].Request
	if strings.Contains(stored.Body, "\x89PNG") || !strings.Contains(stored.Body, `"model":"image"`) || !strings.Contains(stored.Body, "image/jpeg, 4100 bytes (omitted)") || stored.Bytes != size {
		t.Fatalf("request inspection copy: %d bytes %s", stored.Bytes, stored.Body)
	}
	if s := f.settlements[0]; s.Usage != (domain.Usage{InputTokens: 10, ImageInputTokens: 390, ImageOutputTokens: 196, Known: true}) {
		t.Fatalf("settlement %+v", s)
	}
}

func TestImageEditJSONKeepsDataURLsAndOmitsThemFromInspection(t *testing.T) {
	up := newImageUpstream(t, "application/json", `{"created":1,"data":[{"b64_json":"QUJD"}],"usage":{"input_tokens":5,"output_tokens":6}}`)
	h, f := imageFixture(up.server.URL)
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 2048)...))
	w := serveRequest(h, newRequest("/openai/v1/images/edits", `{"model":"image","prompt":"x","images":[{"image_url":"`+png+`"}],"mask":{"image_url":"`+png+`"}}`))
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	var sent struct {
		Model  string `json:"model"`
		Images []struct {
			URL string `json:"image_url"`
		} `json:"images"`
	}
	if json.Unmarshal(up.bodies[0], &sent) != nil || sent.Model != "gpt-image-2" || len(sent.Images) != 1 || sent.Images[0].URL != png {
		t.Fatalf("upstream body %.200s", up.bodies[0])
	}
	stored := f.payloads[w.Header().Get("X-Request-Id")].Request.Body
	if strings.Contains(stored, "base64,") || !strings.Contains(stored, "[image data omitted:") || !strings.Contains(stored, "[mask data omitted:") {
		t.Fatalf("request inspection copy %.300s", stored)
	}
}

func TestReplyScannerFollowsChunkedReplies(t *testing.T) {
	reply := ` {"created":1,"data":[{"b64_json":"QUJD"},{"b64_json":"REVG"}],"note":"a,}]\"{","nested":{"a":[1,{"b":"}"}]},"usage":{"input_tokens":5,"output_tokens":6},"flag":true,"n":null} `
	for _, size := range []int{1, 2, 7, len(reply)} {
		s := newReplyScanner()
		for i := 0; i < len(reply); i += size {
			_, _ = s.Write([]byte(reply[i:min(i+size, len(reply))]))
		}
		u, ok := parseImageUsage(s.fields["usage"])
		if !s.complete() || !ok || u.ImageInputTokens != 5 || s.images != 2 || string(s.fields["note"]) != `"a,}]\"{"` || string(s.fields["flag"]) != "true" || string(s.fields["n"]) != "null" || string(s.fields["nested"]) != `{"a":[1,{"b":"}"}]}` {
			t.Fatalf("chunks of %d: complete=%v images=%d fields=%v", size, s.complete(), s.images, s.fields)
		}
	}
	for _, bad := range []string{`{"a":1`, `{"a":1}x`, `[1]`, `{"a":1,}`, `{"a" 1}`, ``} {
		s := newReplyScanner()
		_, _ = s.Write([]byte(bad))
		if s.complete() {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseImageUsageBillsUnsplitTokensAtImageRates(t *testing.T) {
	for raw, want := range map[string]domain.Usage{
		`{"input_tokens":30,"input_tokens_details":{"text_tokens":20,"image_tokens":10},"output_tokens":100,"output_tokens_details":{"text_tokens":4,"image_tokens":96}}`: {InputTokens: 20, ImageInputTokens: 10, OutputTokens: 4, ImageOutputTokens: 96, Known: true},
		`{"input_tokens":30,"output_tokens":100}`:                                                             {ImageInputTokens: 30, ImageOutputTokens: 100, Known: true},
		`{"input_tokens":30,"input_tokens_details":{"text_tokens":25,"image_tokens":10},"output_tokens":100}`: {ImageInputTokens: 30, ImageOutputTokens: 100, Known: true},
	} {
		if got, ok := parseImageUsage(json.RawMessage(raw)); !ok || got != want {
			t.Errorf("%s: %+v", raw, got)
		}
	}
	for _, raw := range []string{`{"input_tokens":30}`, `{"input_tokens":-1,"output_tokens":2}`, `null`} {
		if _, ok := parseImageUsage(json.RawMessage(raw)); ok {
			t.Errorf("accepted %s", raw)
		}
	}
}

// GPT Image models have no text output price but are still published, and only
// while an official credential can serve them: backups never carry images.
func TestModelListIncludesImageModels(t *testing.T) {
	h, f := imageFixture("https://api.openai.com/v1")
	f.models[0].UpstreamModel = "gpt-5.4-mini"
	list := func() string {
		r := httptest.NewRequest("GET", "/openai/v1/models", nil)
		r.Header.Set("Authorization", "Bearer rp_test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		ids := []string{}
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
			t.Fatalf("model list %d %s", w.Code, w.Body)
		}
		for _, m := range body.Data {
			ids = append(ids, m.ID)
		}
		return strings.Join(ids, ",")
	}
	if got := list(); got != "model,image,image25" {
		t.Fatalf("official models %s", got)
	}
	f.endpoints[0].Enabled = false
	f.endpoints = append(f.endpoints, domain.Endpoint{ID: domain.BackupEndpointID("openrouter"), Name: "openrouter", Role: domain.RoleBackup, Vendor: "openrouter", Enabled: true})
	if got := list(); got != "model" {
		t.Fatalf("backup-only models %s", got)
	}
}

// GPT Image 2.5 keeps transparent backgrounds; only GPT Image 2 refuses them.
func TestTransparentBackgroundsNeedGPTImage25(t *testing.T) {
	up := newImageUpstream(t, "application/json", `{"created":1,"data":[{"b64_json":"iVBORw0KGgo="}],"usage":{"input_tokens":5,"input_tokens_details":{"text_tokens":5,"image_tokens":0},"output_tokens":196}}`)
	h, f := imageFixture(up.server.URL)
	w := serveRequest(h, newRequest("/openai/v1/images/generations", `{"model":"image25","prompt":"a logo","background":"transparent","output_format":"webp","quality":"low"}`))
	if w.Code != 200 || len(f.settlements) != 1 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
}

package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func savedPayload(t *testing.T, f *fakeRepo, w *httptest.ResponseRecorder) domain.RequestPayload {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	id := w.Header().Get("X-Request-Id")
	p, ok := f.payloads[id]
	if !ok || len(f.payloads) != 1 {
		t.Fatalf("payload for %q missing among %d", id, len(f.payloads))
	}
	return p
}

func TestPayloadKeepsOriginalRequestAndDeliveredResponse(t *testing.T) {
	const reply = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Hi"}],"usage":{"input_tokens":5,"output_tokens":2}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()
	h, f := fixture("anthropic", server.URL)
	const body = `{"model":"model","messages":[{"role":"user","content":"hello"}]}`
	r := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(body))
	r.Header.Set("X-Api-Key", "rp_test")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	r.Header.Set("User-Agent", "sdk-test/1.0")
	r.Header.Set("Cookie", "session=private")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != reply {
		t.Fatalf("response %d %s", w.Code, w.Body)
	}
	p := savedPayload(t, f, w)
	// The model alias and normalized output limit belong to the upstream body.
	if p.Request.Method != "POST" || p.Request.Path != "/anthropic/v1/messages" || p.Request.Body != body || p.Request.Bytes != len(body) || p.Request.Truncated {
		t.Fatalf("request %+v", p.Request)
	}
	if len(p.Request.Headers) != 2 || p.Request.Headers["anthropic-version"] != "2023-06-01" || p.Request.Headers["user-agent"] != "sdk-test/1.0" {
		t.Fatalf("captured headers %v", p.Request.Headers)
	}
	if p.Response.Status != 200 || p.Response.ContentType != "application/json" || p.Response.Body != reply || p.Response.Bytes != len(reply) || p.Response.Truncated || p.Upstream != nil {
		t.Fatalf("response %+v upstream %+v", p.Response, p.Upstream)
	}
}

func TestPayloadKeepsWithheldUpstreamReplyForOperators(t *testing.T) {
	const providerError = `{"error":{"type":"invalid_request_error","message":"Organization org-123 cannot use this model"}}`
	for _, tc := range []struct {
		name   string
		status int
	}{{"error status", 400}, {"error object with success status", 200}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, providerError)
			}))
			defer server.Close()
			h, f := fixture("openai", server.URL)
			w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}]}`)
			if w.Code < 400 || strings.Contains(w.Body.String(), "org-123") {
				t.Fatalf("client received %d %s", w.Code, w.Body)
			}
			p := savedPayload(t, f, w)
			if p.Response.Status != w.Code || p.Response.Body != w.Body.String() {
				t.Fatalf("delivered response not recorded: %+v", p.Response)
			}
			if p.Upstream == nil || p.Upstream.Status != tc.status || p.Upstream.Body != providerError || p.Upstream.Bytes != len(providerError) || p.Upstream.Truncated {
				t.Fatalf("upstream reply %+v", p.Upstream)
			}
			if _, leaked := p.Request.Headers["authorization"]; leaked {
				t.Fatal("client credential captured")
			}
		})
	}
}

func TestPayloadKeepsDeliveredStream(t *testing.T) {
	const events = "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, events)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if w.Code != 200 || w.Body.String() != events || !w.Flushed {
		t.Fatalf("stream changed: %d flushed=%t %q", w.Code, w.Flushed, w.Body)
	}
	p := savedPayload(t, f, w)
	if p.Response.ContentType != "text/event-stream" || p.Response.Body != events || p.Upstream != nil {
		t.Fatalf("stream payload %+v", p.Response)
	}
}

func TestPayloadResponseCaptureIsBounded(t *testing.T) {
	reply := `{"id":"chat_big","choices":[{"message":{"content":"` + strings.Repeat("a", maxCapturedResponseBytes) + `"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}]}`)
	if w.Code != 200 || w.Body.String() != reply {
		t.Fatal("client did not receive the complete response")
	}
	p := savedPayload(t, f, w)
	if !p.Response.Truncated || len(p.Response.Body) != maxCapturedResponseBytes || p.Response.Bytes != len(reply) || !strings.HasPrefix(reply, p.Response.Body) {
		t.Fatalf("bounded capture: truncated=%t kept=%d bytes=%d", p.Response.Truncated, len(p.Response.Body), p.Response.Bytes)
	}
}

func TestPayloadCaptureCanBeDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"chat_1","usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	h.capture = false
	if w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}]}`); w.Code != 200 {
		t.Fatalf("response %d %s", w.Code, w.Body)
	}
	if len(f.payloads) != 0 || len(f.settlements) != 1 {
		t.Fatalf("payloads=%d settlements=%d", len(f.payloads), len(f.settlements))
	}
}

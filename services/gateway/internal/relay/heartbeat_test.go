package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const slowChat = "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n"

// An upstream that is slow to start its stream, like an Images API stream
// without partial images, sends no headers for a long time. The client gets
// SSE comments meanwhile, and a stream is not limited by the reply timeout.
func TestSlowStreamKeepsTheClientConnected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, slowChat)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	h.beatEvery, h.replyTimeout = 50*time.Millisecond, 100*time.Millisecond
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	body := w.Body.String()
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || !strings.HasPrefix(body, ": keepalive\n\n") || strings.ReplaceAll(body, ": keepalive\n\n", "") != slowChat {
		t.Fatalf("HTTP %d %q", w.Code, body)
	}
	if s := f.settlements[0]; s.Status != "completed" || !s.Usage.Known || s.Usage.OutputTokens != 7 {
		t.Fatalf("settlement %+v", s)
	}
	if stored := f.payloads[w.Header().Get("X-Request-Id")].Response; stored.Status != 200 || !strings.HasPrefix(stored.Body, ": keepalive") {
		t.Fatalf("inspection copy %+v", stored)
	}
}

// Once the heartbeat has sent the status, an error reply arrives as the
// provider's SSE error event.
func TestErrorAfterHeartbeatIsAnSSEEvent(t *testing.T) {
	for _, tc := range []struct{ provider, path, prefix string }{
		{"openai", "/openai/v1/chat/completions", "data: "},
		{"anthropic", "/anthropic/v1/messages", "event: error\ndata: "},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				time.Sleep(300 * time.Millisecond)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":{"message":"The prompt was rejected.","type":"invalid_request_error"}}`)
			}))
			defer server.Close()
			h, f := fixture(tc.provider, server.URL)
			h.beatEvery = 50 * time.Millisecond
			w := serve(h, tc.path, `{"model":"model","messages":[{"role":"user","content":"hello"}],"stream":true}`)
			body := w.Body.String()
			event := tc.prefix + `{"type":"error","error":{"code":"upstream_error","message":"The upstream rejected the request: The prompt was rejected.","type":"upstream_error"}}` + "\n\n"
			if w.Code != 200 || !strings.HasPrefix(body, ": keepalive\n\n") || !strings.HasSuffix(body, event) {
				t.Fatalf("HTTP %d %q", w.Code, body)
			}
			if s := f.settlements[0]; s.Status != "failed" || s.ErrorCode != "upstream_400" || s.ChargeMicros != 0 {
				t.Fatalf("settlement %+v", s)
			}
		})
	}
}

// A quick stream keeps the upstream's status and has no comments.
func TestQuickStreamHasNoHeartbeat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"The prompt was rejected."}}`)
	}))
	defer server.Close()
	h, _ := fixture("openai", server.URL)
	h.beatEvery = time.Second
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	if w.Code != 400 || strings.Contains(w.Body.String(), "keepalive") || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("HTTP %d %q", w.Code, w.Body)
	}
}

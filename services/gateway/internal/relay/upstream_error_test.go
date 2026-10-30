package relay

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUpstreamErrorsKeepTheProvidersExplanation(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{`{"error":{"message":"Invalid value: 'transparent'. Supported values are: 'opaque' and 'auto'.","type":"invalid_request_error","param":"background","code":"invalid_value"}}`,
			"The upstream rejected the request: Invalid value: 'transparent'. Supported values are: 'opaque' and 'auto'. (parameter: background)"},
		{`{"type":"error","error":{"type":"invalid_request_error","message":"messages: text content blocks must be non-empty"}}`,
			"The upstream rejected the request: messages: text content blocks must be non-empty"},
		{`{"error":{"message":"Project proj_AbC123xyz in organization org-XyZ987abc cannot use this model with key sk-proj-ab12****cd34.","param":null}}`,
			"The upstream rejected the request: Project [redacted] in organization [redacted] cannot use this model with key [redacted]."},
		{`{"error":{"message":"Organization org-123 cannot use this model"}}`,
			"The upstream rejected the request: Organization [redacted] cannot use this model"},
		{`{"error":{"message":"A user-supplied value is invalid.","param":"input[0].content"}}`,
			"The upstream rejected the request: A user-supplied value is invalid. (parameter: input[0].content)"},
		{`{"error":{"message":"bad","param":"two words"}}`, "The upstream rejected the request: bad"},
		{`not json`, upstreamRefused},
		{`{"error":{"message":"  "}}`, upstreamRefused},
		{`{"detail":"no error object"}`, upstreamRefused},
	} {
		if got := upstreamError([]byte(c.raw)); got != c.want {
			t.Errorf("%.60s:\n got %q\nwant %q", c.raw, got, c.want)
		}
	}
	long := upstreamError([]byte(`{"error":{"message":"` + strings.Repeat("é", 600) + `"}}`))
	if !strings.HasSuffix(long, strings.Repeat("é", 500)+"…") || strings.Contains(long, strings.Repeat("é", 501)) {
		t.Fatalf("message not bounded at 500 characters: %d bytes", len(long))
	}
}

// A request the upstream refuses as invalid keeps its status and explanation,
// and is not retried on another route.
func TestUpstreamClientErrorsReachTheClient(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"Unsupported value for organization org-AbC123456789: 'temperature' does not support 0.3 with this model.","type":"invalid_request_error","param":"temperature","code":"unsupported_value"}}`)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	w := serve(h, "/openai/v1/chat/completions", `{"model":"model","messages":[{"role":"user","content":"hi"}],"temperature":0.3}`)
	body := w.Body.String()
	if w.Code != 400 || calls != 1 || !strings.Contains(body, `does not support 0.3 with this model.`) || !strings.Contains(body, "(parameter: temperature)") || strings.Contains(body, "AbC123456789") {
		t.Fatalf("HTTP %d after %d calls: %s", w.Code, calls, body)
	}
	if len(f.settlements) != 1 || f.settlements[0].ChargeMicros != 0 || f.settlements[0].ErrorCode != "upstream_400" {
		t.Fatalf("settlement %+v", f.settlements)
	}
}

// An upstream that fails or is overloaded tells the client to retry, and its
// own wording does not reach the client.
func TestUpstreamOverloadAsksTheClientToRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded"}}`)
	}))
	defer server.Close()
	h, f := fixture("openai", server.URL)
	w := serve(h, "/openai/v1/responses", `{"model":"model","input":"hi"}`)
	body := w.Body.String()
	if w.Code != 503 || !strings.Contains(body, "The upstream is temporarily unavailable. Retry the request.") || strings.Contains(body, "Our servers") {
		t.Fatalf("HTTP %d: %s", w.Code, body)
	}
	if len(f.settlements) != 1 || f.settlements[0].ErrorCode != "upstream_503" || f.settlements[0].ChargeMicros != 0 {
		t.Fatalf("settlement %+v", f.settlements)
	}
}

// An upstream that sends no reply in time gets 504 upstream_timeout. A reply
// that is not streamed arrives only when it is complete, so the message points
// long requests to streaming. The upstream may still finish, so its cost stays
// pending and the customer is not charged.
func TestSlowUpstreamReplyTimesOut(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				time.Sleep(300 * time.Millisecond)
				_, _ = io.WriteString(w, `{"usage":{"input_tokens":2,"output_tokens":1}}`)
			}))
			defer server.Close()
			h, f := fixture("openai", server.URL)
			h.replyTimeout = 50 * time.Millisecond
			w := serve(h, "/openai/v1/responses", fmt.Sprintf(`{"model":"model","input":"hi","stream":%t}`, stream))
			body := w.Body.String()
			if w.Code != 504 || !strings.Contains(body, `"upstream_timeout"`) || !strings.Contains(body, "The upstream did not reply within") || strings.Contains(body, "stream: true") == stream {
				t.Fatalf("HTTP %d: %s", w.Code, body)
			}
			if s := f.settlements; len(s) != 1 || s[0].Status != "failed" || s[0].ErrorCode != "upstream_timeout" || s[0].Usage.Known || s[0].ChargeMicros != 0 {
				t.Fatalf("settlement %+v", s)
			}
		})
	}
}

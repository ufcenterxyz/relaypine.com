package relay

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"relaypine.com/gateway/internal/domain"
)

// Request inspection keeps a bounded copy of each relayed exchange. The copy
// never delays or changes what is written to the client.
const (
	maxCapturedResponseBytes = 4 << 20
	// Error replies are read only up to the drain limit already used for them.
	maxCapturedUpstreamBytes = 64 << 10
	maxCapturedHeaderBytes   = 1 << 10
)

// Diagnostic request headers only. Authorization, X-Api-Key and cookies are
// never captured.
var capturedHeaders = []string{"Content-Type", "User-Agent", "Anthropic-Version", "Anthropic-Beta"}

// responseCapture forwards every write unchanged and keeps the first
// maxCapturedResponseBytes of the body the client received.
type responseCapture struct {
	http.ResponseWriter
	status    int
	body      []byte
	size      int
	truncated bool
}

func (c *responseCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *responseCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	n, err := c.ResponseWriter.Write(b)
	c.size += n
	if room := maxCapturedResponseBytes - len(c.body); room > 0 {
		c.body = append(c.body, b[:min(n, room)]...)
	}
	c.truncated = c.size > len(c.body)
	return n, err
}

// Unwrap lets http.ResponseController reach Flush and write deadlines.
func (c *responseCapture) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *responseCapture) captured() domain.CapturedResponse {
	return domain.CapturedResponse{Status: c.status, ContentType: c.Header().Get("Content-Type"), Body: string(c.body), Bytes: c.size, Truncated: c.truncated}
}

// The original client body, before model aliasing and limit normalization,
// so customers never see the upstream model ID through their own request.
func capturedRequest(r *http.Request, body []byte) domain.CapturedRequest {
	headers := map[string]string{}
	for _, name := range capturedHeaders {
		if value := strings.Join(r.Header.Values(name), ", "); value != "" {
			headers[strings.ToLower(name)] = value[:min(len(value), maxCapturedHeaderBytes)]
		}
	}
	return domain.CapturedRequest{Method: r.Method, Path: r.URL.Path, Headers: headers, Body: string(body), Bytes: len(body)}
}

// upstreamReply records a provider reply that the client did not receive
// unchanged, keeping at most limit bytes of its body.
func upstreamReply(response *http.Response, body []byte, limit int) *domain.CapturedResponse {
	size := len(body)
	if response.ContentLength > int64(size) {
		size = int(min(response.ContentLength, 1<<40))
	}
	kept := body[:min(len(body), limit)]
	return &domain.CapturedResponse{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: string(kept), Bytes: size, Truncated: size > len(kept)}
}

func (h *Handler) savePayload(r *http.Request, p plan, capture *responseCapture, upstream *domain.CapturedResponse) {
	payload := domain.RequestPayload{Request: capturedRequest(r, p.original), Response: capture.captured(), Upstream: upstream}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.repo.SavePayload(ctx, p.requestID, payload); err != nil {
		slog.Warn("Request payload was not stored", "request_id", p.requestID)
	}
}

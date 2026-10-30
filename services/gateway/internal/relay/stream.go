package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"relaypine.com/gateway/internal/domain"
)

const maxFrameBytes = 1 << 20

// Anthropic stream usage fields that only grow during a message.
var cumulativeTokens = map[string]bool{"input_tokens": true, "output_tokens": true, "cache_read_input_tokens": true, "cache_creation_input_tokens": true}

type streamTracker struct {
	usage domain.Usage
	// The image tool's tokens from the final Response, and the partial
	// images the stream delivered.
	toolText, toolImage, toolOutput int64
	partialImages                   int
	anthropic                       map[string]json.RawMessage
	completed, failed               bool
	resourceID                      string
	finalFailedUsage                bool
}

// inspectImage follows an Images API stream: partial images, then one
// completed event that carries the usage.
func (t *streamTracker) inspectImage(data []byte) error {
	if string(data) == "[DONE]" {
		return nil
	}
	obj := object(data)
	if obj == nil {
		return errors.New("invalid SSE JSON")
	}
	switch str(obj["type"]) {
	case "image_generation.completed", "image_edit.completed":
		// A completed image without a readable usage keeps the reservation
		// for reconciliation instead of settling at zero.
		t.usage, _ = parseImageUsage(obj["usage"])
		t.completed = true
	case "error":
		t.failed = true
	}
	if raw, ok := obj["error"]; ok && string(raw) != "null" {
		t.failed = true
	}
	return nil
}

func (t *streamTracker) inspect(data []byte, p plan) (string, error) {
	if p.image != nil {
		return "", t.inspectImage(data)
	}
	if string(data) == "[DONE]" {
		if p.provider != "openai" || strings.HasSuffix(p.path, "/responses") {
			return "", errors.New("unexpected terminal event")
		}
		t.completed = true
		return "", nil
	}
	obj := object(data)
	if obj == nil {
		return "", errors.New("invalid SSE JSON")
	}
	kind := str(obj["type"])
	if kind == "error" || kind == "response.failed" {
		t.failed = true
	}
	if raw, ok := obj["error"]; ok && string(raw) != "null" {
		t.failed = true
	}
	resource := ""
	if p.provider == "anthropic" {
		if t.anthropic == nil {
			t.anthropic = map[string]json.RawMessage{}
		}
		usageRaw := obj["usage"]
		if kind == "message_start" {
			usageRaw = object(obj["message"])["usage"]
		}
		u := object(usageRaw)
		if len(usageRaw) > 0 && string(usageRaw) != "null" && u == nil {
			// A malformed explicit update invalidates earlier partial totals.
			t.anthropic = map[string]json.RawMessage{}
		}
		for k, v := range u {
			// Later events carry cumulative totals. Compatible providers repeat
			// every count in message_delta, so a zero placeholder there must not
			// erase the input already reported by message_start.
			if cumulativeTokens[k] {
				previous, known := integer(t.anthropic[k])
				next, valid := integer(v)
				if known && valid && next < previous {
					continue
				}
			}
			t.anthropic[k] = v
		}
		raw, _ := json.Marshal(t.anthropic)
		parsed, ok := parseUsage(raw, p.provider)
		t.usage = domain.Usage{}
		if ok {
			t.usage = parsed
		}
		if kind == "message_stop" {
			t.completed = true
		}
	} else {
		if raw, exists := obj["usage"]; exists && string(raw) != "null" {
			t.usage = domain.Usage{}
			if parsed, ok := parseUsage(raw, p.provider); ok {
				t.usage = parsed
			}
		}
		// Responses terminal events must carry their own complete totals.
		// A missing/null response object cannot inherit a prior progress event.
		if kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" {
			t.usage = domain.Usage{}
			t.finalFailedUsage = false
		}
		if response := object(obj["response"]); response != nil {
			resource = str(response["id"])
			raw, exists := response["usage"]
			if exists && string(raw) != "null" || kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" {
				t.usage = domain.Usage{}
				t.finalFailedUsage = false
				if parsed, ok := parseUsage(raw, p.provider); ok {
					t.usage = parsed
					if kind == "response.failed" {
						t.finalFailedUsage = true
					}
				}
			}
		}
		if kind == "response.image_generation_call.partial_image" {
			t.partialImages++
		}
		if p.imageTool != nil && (kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed") {
			if response := object(obj["response"]); response != nil {
				t.toolText, t.toolImage, t.toolOutput = p.imageTool.usage(response, t.partialImages)
			}
		}
		if kind == "response.completed" || kind == "response.incomplete" {
			t.completed = true
		}
	}
	if resource != "" {
		if t.resourceID != "" && t.resourceID != resource {
			return "", errors.New("response ID changed during stream")
		}
		if t.resourceID == "" {
			t.resourceID = resource
			return resource, nil
		}
	}
	return "", nil
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request, response *http.Response, p plan, c candidate, s *domain.Settlement) {
	frameLimit := maxFrameBytes
	// Request inspection keeps image events without their base64 images.
	capture, _ := w.(*responseCapture)
	if p.image != nil || p.imageTool != nil {
		frameLimit = maxImageFrameBytes
		if capture != nil {
			capture.summarize(nil)
		}
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), frameLimit)
	tracker := streamTracker{}
	frame := []byte{}
	committed := false
	clientFailed := false
	bindingFailed := false
	commit := func() {
		if committed {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(response.StatusCode)
		committed = true
	}
	flush := func() error { return http.NewResponseController(w).Flush() }
	process := func() error {
		if len(frame) == 0 {
			return nil
		}
		parts := [][]byte{}
		for _, line := range bytes.Split(frame, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				data := bytes.TrimPrefix(line, []byte("data:"))
				data = bytes.TrimPrefix(data, []byte(" "))
				parts = append(parts, data)
			}
		}
		if len(parts) > 0 {
			data := bytes.Join(parts, []byte("\n"))
			if p.imageTool != nil {
				// Events with generated images are parsed and kept without them.
				data = compactImageJSON(data)
			}
			resource, err := tracker.inspect(data, p)
			if err != nil {
				return err
			}
			if capture != nil && p.image != nil {
				capture.addSummary(append(append([]byte("data: "), summarizeImageEvent(data)...), '\n', '\n'))
			} else if capture != nil && p.imageTool != nil {
				capture.addSummary(append(append([]byte("data: "), data...), '\n', '\n'))
			}
			if resource != "" {
				if err = h.saveResource(r.Context(), p, c, resource); err != nil {
					bindingFailed = true
					return errors.New("response ownership could not be persisted")
				}
			}
		}
		commit()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := w.Write(append(frame, '\n')); err != nil {
			clientFailed = true
			return err
		}
		if err := flush(); err != nil {
			clientFailed = true
			return err
		}
		frame = frame[:0]
		return nil
	}
	var streamErr error
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(frame)+len(line)+1 > frameLimit {
			streamErr = errors.New("SSE frame exceeds limit")
			break
		}
		if len(line) == 0 {
			streamErr = process()
			if streamErr != nil || tracker.completed || tracker.failed {
				break
			}
		} else {
			frame = append(frame, line...)
			frame = append(frame, '\n')
		}
	}
	if streamErr == nil {
		streamErr = scanner.Err()
	}
	if streamErr == nil && len(frame) > 0 && !tracker.completed && !tracker.failed {
		streamErr = process()
	}
	withTool := func() {
		if p.imageTool != nil && s.Usage.Known {
			s.Usage.ImageTextTokens, s.Usage.ImageInputTokens, s.Usage.ImageOutputTokens = tracker.toolText, tracker.toolImage, tracker.toolOutput
		}
	}
	if tracker.completed && !tracker.failed && streamErr == nil {
		s.Status = "completed"
		s.Usage = tracker.usage
		withTool()
		if strings.HasSuffix(p.path, "/responses") && tracker.resourceID == "" {
			s.Status = "failed"
			s.ErrorCode = "resource_binding_missing"
		}
	} else {
		s.Status = "failed"
		s.ErrorCode = "upstream_stream_incomplete"
		if tracker.failed {
			// The upstream reported the failure in the stream, and the client
			// received its error event.
			s.ErrorCode = "upstream_stream_error"
		}
		if !tracker.failed && !bindingFailed && (r.Context().Err() != nil || clientFailed) {
			s.Status = "canceled"
			s.ErrorCode = "client_disconnected"
		}
		// A partial usage object is not a verified total. Retain the upstream
		// reservation for reconciliation. A verified terminal event remains usable
		// even when writing it to a disconnected client fails; upstream/platform
		// failures stay free to the customer.
		s.Usage = domain.Usage{}
		if tracker.completed || tracker.finalFailedUsage {
			s.Usage = tracker.usage
			withTool()
		}
		if tracker.completed && strings.HasSuffix(p.path, "/responses") && tracker.resourceID == "" {
			s.Status = "failed"
			s.ErrorCode = "resource_binding_missing"
		}
		if bindingFailed {
			s.Status = "failed"
			s.ErrorCode = "resource_binding_failed"
		}
		if !committed {
			fail(w, 502, "upstream_error", "Upstream stream could not be read")
		} else if !clientFailed && r.Context().Err() == nil && !tracker.failed {
			streamError(w, p.provider)
			_ = flush()
		}
	}
	h.price(s, p)
}

func streamError(w http.ResponseWriter, provider string) {
	obj := map[string]any{"type": "error", "error": map[string]string{"type": "upstream_error", "message": "The upstream stream ended before completion"}}
	body, _ := json.Marshal(obj)
	if provider == "anthropic" {
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", body)
	} else {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
	}
}

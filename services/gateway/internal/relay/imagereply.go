package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"relaypine.com/gateway/internal/domain"
)

const (
	// A non-streamed image reply is relayed as it arrives, never buffered
	// whole: several large images are tens of megabytes of base64.
	maxImageResponseBytes = 256 << 20
	// One streamed partial or final image is a single SSE frame.
	maxImageFrameBytes = 64 << 20
	// Top-level reply fields other than the images are small.
	maxImageFieldBytes = 64 << 10
)

// parseImageUsage reads Images API usage. Input and output totals are required.
// Without a consistent text/image split, all input is billed as image input and
// all output as image output, which are the higher rates.
func parseImageUsage(raw json.RawMessage) (domain.Usage, bool) {
	m := object(raw)
	input, ok := integer(m["input_tokens"])
	if !ok {
		return domain.Usage{}, false
	}
	output, ok := integer(m["output_tokens"])
	if !ok {
		return domain.Usage{}, false
	}
	u := domain.Usage{ImageInputTokens: input, ImageOutputTokens: output, Known: true}
	if d := object(m["input_tokens_details"]); d != nil {
		text, okText := integer(d["text_tokens"])
		image, okImage := integer(d["image_tokens"])
		if okText && okImage && text+image == input {
			u.InputTokens, u.ImageInputTokens = text, image
		}
	}
	if d := object(m["output_tokens_details"]); d != nil {
		text, okText := integer(d["text_tokens"])
		image, okImage := integer(d["image_tokens"])
		if okText && okImage && text+image == output {
			u.OutputTokens, u.ImageOutputTokens = text, image
		}
	}
	return u, true
}

// replyScanner follows the top level of one JSON object as it streams past.
// It keeps the raw value of every top-level field except "data", whose images
// it only counts, so memory stays bounded whatever the images weigh.
type replyScanner struct {
	state              int
	depth              int
	inString, escaped  bool
	afterComma         bool
	key                []byte
	kind               byte
	value              []byte
	keep               bool
	images, imageBytes int
	fields             map[string]json.RawMessage
	kept               int
	err                error
}

const (
	scanStart = iota
	scanKeyOrEnd
	scanKey
	scanColon
	scanValue
	scanInValue
	scanCommaOrEnd
	scanDone
)

var errReplyFormat = errors.New("invalid JSON reply")

func newReplyScanner() *replyScanner {
	return &replyScanner{fields: map[string]json.RawMessage{}}
}

func space(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func (s *replyScanner) Write(chunk []byte) (int, error) {
	for _, b := range chunk {
		if s.err != nil {
			break
		}
		s.step(b)
	}
	return len(chunk), nil
}

func (s *replyScanner) complete() bool { return s.err == nil && s.state == scanDone }

func (s *replyScanner) fail() { s.err = errReplyFormat }

func (s *replyScanner) add(b byte) {
	if !s.keep {
		return
	}
	if s.kept++; s.kept > maxImageFieldBytes {
		s.err = errors.New("reply field exceeds limit")
		return
	}
	s.value = append(s.value, b)
}

func (s *replyScanner) end() {
	if s.keep {
		s.fields[string(s.key)] = append(json.RawMessage(nil), s.value...)
	}
	s.state, s.keep, s.value = scanCommaOrEnd, false, s.value[:0]
}

func (s *replyScanner) step(b byte) {
	switch s.state {
	case scanStart:
		if b == '{' {
			s.state, s.depth = scanKeyOrEnd, 1
		} else if !space(b) {
			s.fail()
		}
	case scanKeyOrEnd:
		switch {
		case b == '"':
			s.state, s.key, s.afterComma = scanKey, s.key[:0], false
		case b == '}' && !s.afterComma:
			s.state, s.depth = scanDone, 0
		case !space(b):
			s.fail()
		}
	case scanKey:
		switch {
		case s.escaped:
			s.escaped = false
		case b == '\\':
			s.escaped = true
		case b == '"':
			s.state = scanColon
			return
		}
		if len(s.key) < 64 {
			s.key = append(s.key, b)
		}
	case scanColon:
		if b == ':' {
			s.state = scanValue
		} else if !space(b) {
			s.fail()
		}
	case scanValue:
		if space(b) {
			return
		}
		s.keep, s.kind, s.state = string(s.key) != "data", b, scanInValue
		switch b {
		case '"':
			s.inString = true
		case '{', '[':
			s.depth++
		default:
			s.kind = 'l'
		}
		s.add(b)
	case scanInValue:
		s.inValue(b)
	case scanCommaOrEnd:
		switch {
		case b == ',':
			s.state, s.afterComma = scanKeyOrEnd, true
		case b == '}':
			s.state, s.depth = scanDone, 0
		case !space(b):
			s.fail()
		}
	case scanDone:
		if !space(b) {
			s.fail()
		}
	}
}

func (s *replyScanner) inValue(b byte) {
	if s.kind == 'l' {
		if b == ',' || b == '}' || space(b) {
			s.end()
			s.step(b)
			return
		}
		s.add(b)
		return
	}
	s.add(b)
	if s.inString {
		if string(s.key) == "data" {
			s.imageBytes++
		}
		switch {
		case s.escaped:
			s.escaped = false
		case b == '\\':
			s.escaped = true
		case b == '"':
			s.inString = false
			if s.kind == '"' {
				s.end()
			}
		}
		return
	}
	switch b {
	case '"':
		s.inString = true
	case '{', '[':
		s.depth++
		if b == '{' && s.depth == 3 && string(s.key) == "data" {
			s.images++
		}
	case '}', ']':
		s.depth--
		if s.depth == 1 {
			s.end()
		}
	}
}

// summary is the inspection copy of a reply: every field as sent, with each
// generated image replaced by a note.
func (s *replyScanner) summary() []byte {
	copied := map[string]any{}
	for k, v := range s.fields {
		copied[k] = v
	}
	if s.images > 0 {
		data := make([]map[string]string, s.images)
		for i := range data {
			data[i] = map[string]string{"b64_json": "[image omitted]"}
		}
		copied["data"] = data
		copied["images_omitted_bytes"] = s.imageBytes
	}
	raw, _ := json.Marshal(copied)
	return raw
}

// summarizeImageEvent is the inspection copy of one image stream event, with
// its base64 image replaced by a note.
func summarizeImageEvent(data []byte) []byte {
	event := object(data)
	if event == nil {
		return nil
	}
	if image, ok := event["b64_json"]; ok {
		event["b64_json"], _ = json.Marshal(fmt.Sprintf("[image omitted: %d bytes]", len(image)))
	}
	raw, _ := json.Marshal(event)
	return raw
}

// relayImage passes a non-streamed Images API reply to the client as it
// arrives and settles on the usage it reports.
func (h *Handler) relayImage(w http.ResponseWriter, r *http.Request, response *http.Response, p plan, s *domain.Settlement) {
	if mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type")); mediaType != "application/json" {
		s.Status, s.ErrorCode = "failed", "invalid_response"
		fail(w, 502, "upstream_error", "Expected a JSON response")
		return
	}
	capture := captureOf(w)
	if capture != nil {
		capture.summarize(nil)
	}
	scan := newReplyScanner()
	buf := make([]byte, 64<<10)
	var total int64
	var readErr error
	committed, clientFailed := false, false
	for {
		n, err := response.Body.Read(buf)
		if n > 0 {
			if total += int64(n); total > maxImageResponseBytes {
				readErr = errors.New("image reply exceeds limit")
				break
			}
			_, _ = scan.Write(buf[:n])
			if !committed && scan.err != nil {
				readErr = scan.err
				break
			}
			if !clientFailed {
				if !committed {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(response.StatusCode)
					committed = true
				}
				_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
				if _, err := w.Write(buf[:n]); err != nil {
					// The images are generated and billed upstream whether or not
					// the client reads them; keep reading to settle the usage.
					clientFailed = true
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			readErr = err
			break
		}
	}
	if capture != nil && scan.err == nil {
		capture.summarize(scan.summary())
	}
	switch {
	case readErr != nil || !scan.complete():
		s.Status, s.ErrorCode = "failed", "invalid_response"
		if clientFailed || r.Context().Err() != nil {
			s.Status, s.ErrorCode = "canceled", "client_disconnected"
		}
		// An unreadable reply is not a verified total; the upstream
		// reservation stays for reconciliation.
		s.Usage = domain.Usage{}
		if !committed {
			fail(w, 502, "upstream_error", "Unable to read bounded upstream response")
		}
	case len(scan.fields["error"]) > 0 && string(scan.fields["error"]) != "null":
		s.Status, s.ErrorCode = "failed", "upstream_error"
		s.Usage, _ = parseImageUsage(scan.fields["usage"])
	default:
		s.Status = "completed"
		if clientFailed {
			s.Status, s.ErrorCode = "canceled", "client_disconnected"
		}
		s.Usage, _ = parseImageUsage(scan.fields["usage"])
	}
	h.price(s, p)
}

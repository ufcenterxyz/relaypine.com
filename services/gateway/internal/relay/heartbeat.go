package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// heartbeatEvery is how long a streamed reply may stay silent before the
// gateway writes an SSE comment. Cloudflare, in front of the service, closes a
// connection whose origin sends nothing for 100 seconds.
const heartbeatEvery = 15 * time.Second

// heartbeat keeps a streamed reply's connection open while its upstream is
// silent. An upstream can stay silent for longer than Cloudflare allows: an
// Images API stream without partial images sends nothing, not even its
// headers, until the image is done. After heartbeatEvery of silence the
// heartbeat starts the SSE reply itself if the handler has not, and writes a
// comment line, which SSE clients ignore. The HTTP status is then sent, so an
// error the handler writes afterwards is sent as an SSE error event.
type heartbeat struct {
	http.ResponseWriter
	provider string
	every    time.Duration
	// header is the handler's header map. The client's map is written only
	// while mu is held, because the heartbeat goroutine writes it too.
	header http.Header

	mu   sync.Mutex
	last time.Time
	// started: the heartbeat sent the status. replied: the handler did.
	started, replied bool
	// stream: the reply is an event stream, so comments may be added.
	stream bool
	// failing: the handler's error reply is sent as an SSE error event.
	failing bool
	broken  bool
	stop    chan struct{}
	done    chan struct{}
}

func startHeartbeat(w http.ResponseWriter, provider string, every time.Duration) *heartbeat {
	hb := &heartbeat{ResponseWriter: w, provider: provider, every: every, header: w.Header().Clone(), last: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	go hb.run()
	return hb
}

func (hb *heartbeat) run() {
	defer close(hb.done)
	timer := time.NewTimer(hb.every)
	defer timer.Stop()
	for {
		select {
		case <-hb.stop:
			return
		case <-timer.C:
			timer.Reset(hb.beat())
		}
	}
}

// close stops the heartbeat. Nothing is written to the client after it
// returns.
func (hb *heartbeat) close() {
	close(hb.stop)
	<-hb.done
}

// beat writes a comment once the reply has been silent for hb.every, and
// returns how long to wait before looking again.
func (hb *heartbeat) beat() time.Duration {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	if wait := hb.every - time.Since(hb.last); wait > 0 {
		return wait
	}
	if !hb.started && !hb.replied {
		h := hb.ResponseWriter.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("X-Accel-Buffering", "no")
		hb.ResponseWriter.WriteHeader(http.StatusOK)
		hb.started, hb.stream = true, true
	}
	if hb.stream && !hb.failing && !hb.broken {
		hb.broken = hb.send([]byte(": keepalive\n\n")) != nil
	}
	return hb.every
}

// send writes b to the client and flushes it, giving up after 30 seconds.
func (hb *heartbeat) send(b []byte) error {
	rc := http.NewResponseController(hb.ResponseWriter)
	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := hb.ResponseWriter.Write(b); err != nil {
		return err
	}
	hb.last = time.Now()
	return rc.Flush()
}

func (hb *heartbeat) Header() http.Header { return hb.header }

func (hb *heartbeat) WriteHeader(status int) {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	hb.writeHeader(status)
}

func (hb *heartbeat) writeHeader(status int) {
	if hb.replied {
		return
	}
	hb.replied = true
	if hb.started {
		hb.failing = status >= 400
		return
	}
	h := hb.ResponseWriter.Header()
	for k, v := range hb.header {
		h[k] = v
	}
	hb.stream = status == http.StatusOK && strings.HasPrefix(h.Get("Content-Type"), "text/event-stream")
	hb.ResponseWriter.WriteHeader(status)
}

func (hb *heartbeat) Write(b []byte) (int, error) {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	if !hb.replied {
		hb.writeHeader(http.StatusOK)
	}
	if hb.failing {
		if err := hb.send(sseError(b, hb.provider)); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	n, err := hb.ResponseWriter.Write(b)
	hb.last = time.Now()
	return n, err
}

// FlushError lets http.ResponseController flush through the heartbeat's lock.
func (hb *heartbeat) FlushError() error {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return http.NewResponseController(hb.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach write deadlines.
func (hb *heartbeat) Unwrap() http.ResponseWriter { return hb.ResponseWriter }

// sseError sends the handler's JSON error reply as the provider's SSE error
// event: OpenAI clients raise on a data event with an error object, and
// Anthropic clients on an error event.
func sseError(body []byte, provider string) []byte {
	var reply struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &reply) != nil || len(reply.Error) == 0 || string(reply.Error) == "null" {
		reply.Error, _ = json.Marshal(map[string]string{"type": "upstream_error", "code": "upstream_error", "message": "The request failed"})
	}
	event, _ := json.Marshal(struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}{"error", reply.Error})
	prefix := "data: "
	if provider == "anthropic" {
		prefix = "event: error\ndata: "
	}
	return append(append([]byte(prefix), event...), '\n', '\n')
}

// captureOf finds the request capture among w and the writers it wraps.
func captureOf(w http.ResponseWriter) *responseCapture {
	for w != nil {
		if c, ok := w.(*responseCapture); ok {
			return c
		}
		inner, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = inner.Unwrap()
	}
	return nil
}

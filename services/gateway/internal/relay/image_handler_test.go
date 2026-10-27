package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

// A generated, non-sensitive image whose compressed size is large enough to
// distinguish visual-token admission from the old base64-as-text estimate.
func handlerImageData(t *testing.T) string {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < len(im.Pix); i += 4 {
		n := rng.Uint32()
		im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = byte(n), byte(n>>8), byte(n>>16), 255
	}
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(out.Bytes())
}

func handlerImageBody(data string, stream bool) string {
	return fmt.Sprintf(`{"model":"model","max_tokens":64,"stream":%t,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}},{"type":"text","text":"Describe the colors."}]}]}`, stream, data)
}

func TestImageRequestContextAndCapacityUseVisualBound(t *testing.T) {
	data := handlerImageData(t)
	body := handlerImageBody(data, false)
	h, repo := fixture("anthropic", "https://api.anthropic.com")
	repo.models[0].PricingReference = &domain.PriceReference{MaxInputTokens: 16384}
	p := plan{provider: "anthropic", path: "/v1/messages", key: repo.key}
	if err := h.prepare(httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(body)), &p, repo.models); err != nil {
		t.Fatal(err)
	}
	if p.input <= 4096 || p.input >= 16384 || int64(len(body))*2+4096 <= 16384 {
		t.Fatalf("visual admission did not separate base64 bytes: bound %d body %d", p.input, len(body))
	}
	// The same bound, not a separate low estimate, must enforce the context cap.
	repo.models[0].PricingReference.MaxInputTokens = p.input - 1
	p = plan{provider: "anthropic", path: "/v1/messages", key: repo.key}
	if err := h.prepare(httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(body)), &p, repo.models); err == nil {
		t.Fatal("visual input exceeded the configured context bound")
	}
}

type imageRecordingLimiter struct {
	fakeLimiter
	tokens int64
}

func (l *imageRecordingLimiter) acquire(ctx context.Context, id string, buckets []limits, tokens int64) error {
	l.tokens = tokens
	return l.fakeLimiter.acquire(ctx, id, buckets, tokens)
}

// Use the existing fake limiter with an extra observation of the bound supplied
// by Handler, so the context/TPM/money paths cannot silently diverge.
func TestImageForwardingAndActualUsageSettlement(t *testing.T) {
	data := handlerImageData(t)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				var request struct {
					Messages []struct {
						Content []struct {
							Type   string `json:"type"`
							Source struct {
								Type      string `json:"type"`
								MediaType string `json:"media_type"`
								Data      string `json:"data"`
							} `json:"source"`
						} `json:"content"`
					} `json:"messages"`
				}
				if json.Unmarshal(body, &request) != nil || len(request.Messages) != 1 || len(request.Messages[0].Content) != 2 || request.Messages[0].Content[0].Source.Data != data || request.Messages[0].Content[0].Type != "image" || request.Messages[0].Content[0].Source.Type != "base64" || request.Messages[0].Content[0].Source.MediaType != "image/png" {
					t.Error("image payload was changed or removed")
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2100,\"output_tokens\":1}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				} else {
					_, _ = io.WriteString(w, `{"id":"msg_image","usage":{"input_tokens":2100,"output_tokens":7}}`)
				}
			}))
			defer upstream.Close()
			h, repo := fixture("anthropic", upstream.URL)
			limiter := &imageRecordingLimiter{}
			h.limiter = limiter
			body := handlerImageBody(data, stream)
			w := serve(h, "/anthropic/v1/messages", body)
			if w.Code != 200 || calls.Load() != 1 || len(repo.reservations) != 1 || len(repo.settlements) != 1 {
				t.Fatalf("image dispatch status=%d calls=%d", w.Code, calls.Load())
			}
			s := repo.settlements[0]
			wantUsage := domain.Usage{InputTokens: 2100, OutputTokens: 7, Known: true}
			wantCharge, wantCost, err := charges(wantUsage, repo.models[0])
			if err != nil || s.Usage != wantUsage || s.ChargeMicros != wantCharge || s.CostMicros != wantCost || s.Status != "completed" {
				t.Fatalf("actual usage settlement mismatch: %+v", s)
			}
			if repo.reservations[0].Request.ReservedMicros <= wantCharge || repo.reservations[0].Request.ReservedMicros > 20000 {
				t.Fatal("image was not conservatively reserved using its visual bound")
			}
			wantBound := 2*int64(len(body)-len(data)) + 4096 + 2*10*10 + 256
			wantReserve, wantCostReserve, err := estimate(wantBound, 64, repo.models[0])
			if err != nil || limiter.tokens != wantBound+64 || repo.reservations[0].Request.ReservedMicros != wantReserve || repo.reservations[0].CostReserve != wantCostReserve {
				t.Fatal("TPM and financial reservation did not share the same image input bound")
			}
		})
	}
}

func TestImageCountingRemainsFreeAndBudgetFailureSendsNothing(t *testing.T) {
	data := handlerImageData(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Error("unexpected generation call")
		}
		_, _ = io.WriteString(w, `{"input_tokens":2100}`)
	}))
	defer upstream.Close()
	h, repo := fixture("anthropic", upstream.URL)
	w := serve(h, "/anthropic/v1/messages/count_tokens", handlerImageBody(data, false))
	if w.Code != 200 || calls.Load() != 1 || len(repo.reservations) != 1 || len(repo.settlements) != 1 || repo.reservations[0].Request.ReservedMicros != 0 || repo.reservations[0].CostReserve != 0 || repo.settlements[0].ChargeMicros != 0 || repo.settlements[0].CostMicros != 0 {
		t.Fatalf("image counting was not free: status %d", w.Code)
	}
	h, repo = fixture("anthropic", upstream.URL)
	repo.reserveError = store.ErrInsufficientFunds
	w = serve(h, "/anthropic/v1/messages", handlerImageBody(data, false))
	if w.Code != 402 || calls.Load() != 1 || len(repo.reservations) != 0 || len(repo.settlements) != 0 {
		t.Fatal("insufficient funds sent an image upstream")
	}
}

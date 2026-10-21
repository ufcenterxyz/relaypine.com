package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInvalidTerminalUsageNeverSettlesEarlierPartialTotals(t *testing.T) {
	for _, tc := range []struct{ provider, path, events string }{
		{"anthropic", "/anthropic/v1/messages", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5,\"cache_creation\":{\"ephemeral_1h_input_tokens\":10}}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"openai", "/openai/v1/chat/completions", "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":11}}}\n\ndata: [DONE]\n\n"},
		{"openai", "/openai/v1/responses", "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_test\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"input_tokens_details\":{\"cache_write_tokens\":5,\"cache_write_tokens_details\":{\"ttl_30m_tokens\":0,\"ttl_12h_tokens\":5}}}}}\n\n"},
		{"openai", "/openai/v1/responses", "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_test\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\ndata: {\"type\":\"response.completed\"}\n\n"},
		{"openai", "/openai/v1/responses", "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_test\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\ndata: {\"type\":\"response.completed\",\"response\":null}\n\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.events)
			}))
			defer server.Close()
			h, f := fixture(tc.provider, server.URL)
			w := serve(h, tc.path, `{"model":"model","messages":[],"stream":true}`)
			if w.Code != 200 || len(f.settlements) != 1 {
				t.Fatalf("unexpected outcome %d %s", w.Code, w.Body)
			}
			settlement := f.settlements[0]
			if settlement.Usage.Known || settlement.ChargeMicros != 0 || settlement.CostMicros != 0 {
				t.Fatalf("stale partial usage charged %+v", settlement)
			}
		})
	}
}

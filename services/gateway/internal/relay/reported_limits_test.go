package relay

import (
	"net/http"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestReportedLimitsReadProviderHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Ratelimit-Limit-Requests", "10000")
	h.Set("X-Ratelimit-Limit-Tokens", " 4000000 ")
	h.Set("X-Ratelimit-Remaining-Tokens", "3999000")
	if l, ok := reportedLimit("openai", h); !ok || l != (domain.ReportedLimit{Requests: 10000, Tokens: 4000000}) {
		t.Fatalf("OpenAI limits %+v %v", l, ok)
	}
	a := http.Header{}
	a.Set("Anthropic-Ratelimit-Requests-Limit", "4000")
	a.Set("Anthropic-Ratelimit-Input-Tokens-Limit", "2000000")
	a.Set("Anthropic-Ratelimit-Output-Tokens-Limit", "400000")
	a.Set("Anthropic-Ratelimit-Tokens-Limit", "2400000")
	if l, ok := reportedLimit("anthropic", a); !ok || l != (domain.ReportedLimit{Requests: 4000, Tokens: 2400000, InputTokens: 2000000, OutputTokens: 400000}) {
		t.Fatalf("Anthropic limits %+v %v", l, ok)
	}
	// OpenAI headers on an Anthropic route, and malformed values, are ignored.
	bad := http.Header{}
	bad.Set("X-Ratelimit-Limit-Requests", "10000")
	bad.Set("Anthropic-Ratelimit-Requests-Limit", "-1")
	bad.Set("Anthropic-Ratelimit-Tokens-Limit", "1e6")
	if l, ok := reportedLimit("anthropic", bad); ok {
		t.Fatalf("accepted %+v", l)
	}
	if _, ok := reportedLimit("openai", http.Header{}); ok {
		t.Fatal("no headers reported a limit")
	}
	// A handler without Redis, as in unit tests, ignores reports.
	var recorder *limitRecorder
	recorder.note("credential", "model", domain.ReportedLimit{Requests: 1})
}

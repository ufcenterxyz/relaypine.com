package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

// flexModel is gpt-6.1-terra at OpenAI's published standard and Flex prices,
// with its official reference.
func flexModel() domain.Model {
	return domain.Model{
		ID: "model", Name: "GPT-6.1 Terra", Provider: "openai", UpstreamModel: "gpt-6.1-terra", Enabled: true, BillingMode: domain.BillingMetered, MaxOutputTokens: 128, Version: 1,
		InputRate: 1_000_000, OutputRate: 5_000_000, CachedRate: 50_000, CacheWriteRate: 1_250_000,
		CostInputRate: 1_000_000, CostOutputRate: 5_000_000, CostCachedRate: 50_000, CostCacheWriteRate: 1_250_000,
		LongContext: &domain.LongContext{AboveInputTokens: 272_000, InputRate: 2_000_000, OutputRate: 7_500_000, CachedRate: 100_000, CacheWriteRate: 2_500_000,
			CostInputRate: 2_000_000, CostOutputRate: 7_500_000, CostCachedRate: 100_000, CostCacheWriteRate: 2_500_000},
		Flex: &domain.FlexPrice{InputRate: 500_000, OutputRate: 2_500_000, CachedRate: 25_000, CacheWriteRate: 625_000,
			CostInputRate: 500_000, CostOutputRate: 2_500_000, CostCachedRate: 25_000, CostCacheWriteRate: 625_000,
			LongContext: &domain.LongContext{AboveInputTokens: 272_000, InputRate: 1_000_000, OutputRate: 3_750_000, CachedRate: 50_000, CacheWriteRate: 1_250_000,
				CostInputRate: 1_000_000, CostOutputRate: 3_750_000, CostCachedRate: 50_000, CostCacheWriteRate: 1_250_000}},
		PricingReference: &domain.PriceReference{Provider: "openai", Model: "gpt-6.1-terra", SourceURL: "https://developers.openai.com/api/docs/pricing", Scope: "standard_text", MaxInputTokens: 272_000},
	}
}

// flexUpstream is a fake OpenAI that records each request and answers with
// reply.
type flexUpstream struct {
	mu     sync.Mutex
	bodies []map[string]json.RawMessage
	auths  []string
	server *httptest.Server
}

func newFlexUpstream(t *testing.T, reply func(http.ResponseWriter)) *flexUpstream {
	u := &flexUpstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies, u.auths = append(u.bodies, object(raw)), append(u.auths, r.Header.Get("Authorization"))
		u.mu.Unlock()
		reply(w)
	}))
	t.Cleanup(u.server.Close)
	return u
}
func (u *flexUpstream) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}
func (u *flexUpstream) body(i int) map[string]json.RawMessage {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.bodies[i]
}

// answer replies with a status, headers and body.
func answer(status int, contentType, body string, headers ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// flexFixture serves flexModel from an official OpenAI endpoint that the test
// points at upstream; vendors are backups pointed at their own servers.
func flexFixture(upstream string, vendors map[string]*flexUpstream) (*Handler, *fakeRepo, *fakeLimiter) {
	h, f := fixture("openai", "https://api.openai.com")
	f.models[0] = flexModel()
	for vendor := range vendors {
		f.endpoints = append(f.endpoints, domain.Endpoint{ID: domain.BackupEndpointID(vendor), Name: vendor, Role: domain.RoleBackup, Vendor: vendor, Enabled: true})
		f.credentials = append(f.credentials, domain.Credential{ID: domain.BackupCredentialID(vendor), EndpointID: domain.BackupEndpointID(vendor), Secret: vendor + "-secret", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5})
	}
	h.target = func(e domain.Endpoint, protocol string) (string, string) {
		base, auth := e.Target(protocol)
		if e.IsBackup() {
			preset, _ := url.Parse(base)
			return vendors[e.Vendor].server.URL + preset.Path, auth
		}
		return upstream, auth
	}
	limiter := &fakeLimiter{}
	h.limiter = limiter
	return h, f, limiter
}

// The usage every successful reply below reports: 1,000 input tokens, 400 of
// them cached, and 80 output tokens. At Flex rates that is 600 x $0.50 +
// 400 x $0.025 + 80 x $2.50 per million, 510 micro-USD; at the standard
// rates twice as much.
const (
	flexCharge     = 510
	standardCharge = 1020
)

// replies of the four protocols that report usage and, unless tier is empty,
// a service tier.
func flexReply(protocol, tier string) (string, string) {
	tierField := ""
	if tier != "" {
		tierField = fmt.Sprintf(`"service_tier":%q,`, tier)
	}
	responsesUsage := `"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":400},"output_tokens":80,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":1080}`
	chatUsage := `"usage":{"prompt_tokens":1000,"completion_tokens":80,"prompt_tokens_details":{"cached_tokens":400}}`
	switch protocol {
	case "responses":
		return "application/json", `{"id":"resp_flex","object":"response","status":"completed",` + tierField + `"output":[],` + responsesUsage + `}`
	case "responses-stream":
		return "text/event-stream", "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_flex\",\"status\":\"in_progress\",\"service_tier\":\"auto\"}}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_flex\",\"status\":\"completed\"," + tierField + responsesUsage + "}}\n\n"
	case "chat":
		return "application/json", `{"id":"chat_flex","object":"chat.completion",` + tierField + `"choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],` + chatUsage + `}`
	default:
		return "text/event-stream", "data: {\"id\":\"chat_flex\",\"object\":\"chat.completion.chunk\"," + tierField + "\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"}}]}\n\n" +
			"data: {\"id\":\"chat_flex\",\"object\":\"chat.completion.chunk\"," + tierField + "\"choices\":[]," + chatUsage + "}\n\ndata: [DONE]\n\n"
	}
}

// flexRequest is a request of a protocol with the given extra fields.
func flexRequest(protocol, extra string) (string, string) {
	stream := strings.HasSuffix(protocol, "-stream")
	if strings.HasPrefix(protocol, "responses") {
		return "/openai/v1/responses", fmt.Sprintf(`{"model":"model","input":"hello","max_output_tokens":100,"stream":%t%s}`, stream, extra)
	}
	return "/openai/v1/chat/completions", fmt.Sprintf(`{"model":"model","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":100,"stream":%t%s}`, stream, extra)
}

// A Flex request reaches OpenAI with service_tier "flex", holds its Flex
// price and settles at the Flex rates when OpenAI reports the Flex tier; the
// request record carries the tier.
func TestFlexReservesAndSettlesAtFlexRates(t *testing.T) {
	for _, protocol := range []string{"responses", "responses-stream", "chat", "chat-stream"} {
		t.Run(protocol, func(t *testing.T) {
			contentType, reply := flexReply(protocol, "flex")
			up := newFlexUpstream(t, answer(200, contentType, reply))
			h, f, limiter := flexFixture(up.server.URL, nil)
			path, body := flexRequest(protocol, `,"service_tier":"flex"`)
			w := serve(h, path, body)
			if w.Code != 200 || w.Body.String() != reply {
				t.Fatalf("HTTP %d %s", w.Code, w.Body)
			}
			if tier := str(up.body(0)["service_tier"]); tier != "flex" {
				t.Fatalf("upstream service_tier %q", tier)
			}
			// Every input token at the highest input-side Flex rate, the 30-minute
			// cache write's $0.625, and 100 output tokens at $2.50 per million.
			input := int64(2*len(body) + 4096)
			flexHold := (input*625_000 + 100*2_500_000 + 999_999) / 1_000_000
			standardHold, _, _ := estimate(input, 100, flexModel())
			r := f.reservations[0]
			if r.Request.ServiceTier != domain.ServiceTierFlex || r.Request.ReservedMicros != flexHold || r.CostReserve != flexHold || flexHold >= standardHold || r.Model.Flex == nil {
				t.Fatalf("reservation %+v hold %d cost %d, want %d (standard %d)", r.Request, r.Request.ReservedMicros, r.CostReserve, flexHold, standardHold)
			}
			s := f.settlements[0]
			if s.Status != "completed" || !s.Usage.Known || s.Usage.ServiceTier != domain.ServiceTierFlex || s.ChargeMicros != flexCharge || s.CostMicros != flexCharge || s.ErrorCode != "" {
				t.Fatalf("settlement %+v", s)
			}
			if limiter.leases[0] != requestTimeout+leaseMargin && !strings.HasSuffix(protocol, "-stream") || limiter.leases[0] != flexStreamTimeout+leaseMargin && strings.HasSuffix(protocol, "-stream") {
				t.Fatalf("lease %s", limiter.leases[0])
			}
		})
	}
}

// Each request is billed at the tier OpenAI reports it ran at: the standard
// rates when it ran at the default tier, and nothing until reconciled when
// the reply names no tier the gateway prices.
func TestFlexSettlesAtTheReportedTier(t *testing.T) {
	for _, protocol := range []string{"responses", "responses-stream", "chat", "chat-stream"} {
		for _, tc := range []struct {
			tier   string
			known  bool
			charge int64
			code   string
		}{
			{"default", true, standardCharge, ""},
			{"", false, 0, "service_tier_unknown"},
			{"auto", false, 0, "service_tier_unknown"},
			{"priority", false, 0, "service_tier_unknown"},
		} {
			t.Run(protocol+"/"+tc.tier, func(t *testing.T) {
				contentType, reply := flexReply(protocol, tc.tier)
				up := newFlexUpstream(t, answer(200, contentType, reply))
				h, f, _ := flexFixture(up.server.URL, nil)
				path, body := flexRequest(protocol, `,"service_tier":"flex"`)
				if w := serve(h, path, body); w.Code != 200 || w.Body.String() != reply {
					t.Fatalf("HTTP %d %s", w.Code, w.Body)
				}
				s := f.settlements[0]
				if s.Usage.Known != tc.known || s.ChargeMicros != tc.charge || s.CostMicros != tc.charge || s.ErrorCode != tc.code {
					t.Fatalf("settlement %+v", s)
				}
				if tc.known && s.Usage.ServiceTier != domain.ServiceTierDefault {
					t.Fatalf("recorded tier %q", s.Usage.ServiceTier)
				}
			})
		}
	}
}

// A request without service_tier "flex" is unchanged: it is sent at the
// default tier, holds and settles at the standard rates whatever tier the
// reply names, and records no tier.
func TestStandardRequestsKeepTheDefaultTier(t *testing.T) {
	for _, protocol := range []string{"responses", "chat-stream"} {
		for _, extra := range []string{"", `,"service_tier":"auto"`, `,"service_tier":"default"`} {
			contentType, reply := flexReply(protocol, "flex")
			up := newFlexUpstream(t, answer(200, contentType, reply))
			h, f, limiter := flexFixture(up.server.URL, nil)
			path, body := flexRequest(protocol, extra)
			if w := serve(h, path, body); w.Code != 200 {
				t.Fatalf("%s %s: HTTP %d %s", protocol, extra, w.Code, w.Body)
			}
			standardHold, _, _ := estimate(int64(2*len(body)+4096), 100, flexModel())
			s := f.settlements[0]
			if tier := str(up.body(0)["service_tier"]); tier != "default" || f.reservations[0].Request.ServiceTier != "" || f.reservations[0].Request.ReservedMicros != standardHold ||
				s.ChargeMicros != standardCharge || s.Usage.ServiceTier != "" || limiter.leases[0] != requestTimeout+leaseMargin {
				t.Fatalf("%s %s: tier %q reservation %+v settlement %+v", protocol, extra, tier, f.reservations[0].Request, s)
			}
			if raw, _ := json.Marshal(s.Usage); strings.Contains(string(raw), "service_tier") {
				t.Fatalf("standard usage records a tier: %s", raw)
			}
		}
	}
}

// OpenAI's 429 for unavailable Flex capacity reaches the client as 429
// flex_unavailable with OpenAI's Retry-After. It is not charged, it pauses
// neither the key nor the model, no other route is tried, and the request
// returns its concurrency slot and token estimate.
func TestFlexCapacityRefusalIsFreeAndPausesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		body, retryAfter string
		// want lists the acceptable Retry-After values; an HTTP date leaves
		// a second of slack either way.
		want []string
	}{
		"documented": {`{"error":{"message":"Flex processing is temporarily unavailable. Please try again later or use standard processing.","type":"resource_unavailable","param":null,"code":"flex_unavailable"}}`, "120", []string{"120"}},
		"no wait":    {`{"error":{"message":"Resource unavailable","type":"resource_unavailable","code":null}}`, "", []string{""}},
		"date":       {`{"error":{"message":"Flex capacity is short","type":"invalid_request_error","code":null}}`, time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat), []string{"89", "90", "91"}},
		"unreadable": {`Too Many Requests`, "soon", []string{""}},
	} {
		t.Run(name, func(t *testing.T) {
			up := newFlexUpstream(t, answer(429, "application/json", tc.body, "Retry-After", tc.retryAfter))
			h, f, limiter := flexFixture(up.server.URL, nil)
			// A second official key is never tried for a capacity refusal.
			f.credentials = append(f.credentials, domain.Credential{ID: "second", EndpointID: "endpoint", Secret: "second-secret", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5})
			path, body := flexRequest("responses", `,"service_tier":"flex"`)
			w := serve(h, path, body)
			var reply struct {
				Error struct{ Type, Code, Message, Param string } `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != 429 || reply.Error.Code != "flex_unavailable" || reply.Error.Type != "resource_unavailable" || reply.Error.Param != "service_tier" || !strings.Contains(reply.Error.Message, "not charged") {
				t.Fatalf("HTTP %d %s", w.Code, w.Body)
			}
			if got := w.Header().Get("Retry-After"); !slices.Contains(tc.want, got) {
				t.Fatalf("Retry-After %q, want %q", got, tc.want)
			}
			if up.calls() != 1 || len(f.reroutes) != 0 {
				t.Fatalf("calls %d reroutes %+v", up.calls(), f.reroutes)
			}
			s := f.settlements[0]
			if s.Status != "failed" || s.ErrorCode != "flex_unavailable" || s.Usage != (domain.Usage{Known: true}) || s.ChargeMicros != 0 || s.CostMicros != 0 {
				t.Fatalf("settlement %+v", s)
			}
			if len(limiter.cooled) != 0 {
				t.Fatalf("a Flex capacity refusal paused %v", limiter.cooled)
			}
			if limiter.acquired != 1 || limiter.released != 1 {
				t.Fatalf("concurrency acquired %d released %d", limiter.acquired, limiter.released)
			}
			for scope, tokens := range limiter.netTokens() {
				if tokens != 0 {
					t.Fatalf("%s kept %d tokens of a refused request", scope, tokens)
				}
			}
		})
	}
}

// A 429 that names a rate limit or an account refusal without mentioning Flex
// is one, also for a Flex request: the model or key is paused and the request
// moves to the next official key, still at the Flex tier.
func TestFlexRequestsKeepRealRateLimits(t *testing.T) {
	for name, tc := range map[string]struct {
		body  string
		scope string
	}{
		"rate limit": {`{"error":{"message":"Rate limit reached for gpt-6.1-terra on tokens per min (TPM)","type":"tokens","code":"rate_limit_exceeded"}}`, modelScope("credential:credential", "gpt-6.1-terra")},
		"quota":      {`{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`, "credential:credential"},
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := flexReply("responses", "flex")
			second := newFlexUpstream(t, answer(200, "application/json", ok))
			first := newFlexUpstream(t, answer(429, "application/json", tc.body, "Retry-After", "20"))
			h, f, limiter := flexFixture(first.server.URL, nil)
			f.endpoints = append(f.endpoints, domain.Endpoint{ID: "endpoint-2", Provider: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true})
			f.credentials = append(f.credentials, domain.Credential{ID: "second", EndpointID: "endpoint-2", Secret: "second-secret", Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 5, Priority: -1})
			h.target = func(e domain.Endpoint, protocol string) (string, string) {
				_, auth := e.Target(protocol)
				if e.ID == "endpoint-2" {
					return second.server.URL, auth
				}
				return first.server.URL, auth
			}
			path, body := flexRequest("responses", `,"service_tier":"flex"`)
			if w := serve(h, path, body); w.Code != 200 {
				t.Fatalf("HTTP %d %s", w.Code, w.Body)
			}
			if _, paused := limiter.cooled[tc.scope]; !paused {
				t.Fatalf("not paused: %v", limiter.cooled)
			}
			if second.calls() != 1 || str(second.body(0)["service_tier"]) != "flex" || f.settlements[0].ChargeMicros != flexCharge {
				t.Fatalf("second route %d calls, settlement %+v", second.calls(), f.settlements[0])
			}
		})
	}
}

// A streamed request may be refused inside the stream. When the refusal is
// its first event, the client gets the same 429; after the stream started,
// the refusal reaches the client as the upstream sent it. Either way nothing
// is charged.
func TestFlexStreamRefusal(t *testing.T) {
	refusal := `{"type":"error","error":{"type":"resource_unavailable","code":"flex_unavailable","message":"Flex processing is temporarily unavailable.","param":null},"sequence_number":0}`
	created := `{"type":"response.created","response":{"id":"resp_wait","status":"queued","service_tier":"flex"}}`
	failed := `{"type":"response.failed","response":{"id":"resp_wait","status":"failed","service_tier":"flex","error":{"code":"flex_unavailable","message":"Flex processing is temporarily unavailable."}}}`
	for _, tc := range []struct {
		name, protocol, events string
		status                 int
	}{
		{"first event", "responses-stream", "event: error\ndata: " + refusal + "\n\n", 429},
		{"top-level code", "responses-stream", "event: error\ndata: {\"type\":\"error\",\"code\":\"flex_unavailable\",\"message\":\"Flex processing is temporarily unavailable.\",\"param\":null}\n\n", 429},
		{"chat chunk", "chat-stream", "data: {\"error\":{\"type\":\"resource_unavailable\",\"code\":\"flex_unavailable\",\"message\":\"Flex processing is temporarily unavailable.\"}}\n\n", 429},
		{"after created", "responses-stream", "event: response.created\ndata: " + created + "\n\nevent: response.failed\ndata: " + failed + "\n\n", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := newFlexUpstream(t, answer(200, "text/event-stream", tc.events, "Retry-After", "30"))
			h, f, limiter := flexFixture(up.server.URL, nil)
			path, body := flexRequest(tc.protocol, `,"service_tier":"flex"`)
			w := serve(h, path, body)
			if w.Code != tc.status {
				t.Fatalf("HTTP %d %s", w.Code, w.Body)
			}
			if tc.status == 429 && (!strings.Contains(w.Body.String(), `"code":"flex_unavailable"`) || w.Header().Get("Retry-After") != "30") {
				t.Fatalf("refusal %s %v", w.Body, w.Header())
			}
			if tc.status == 200 && w.Body.String() != tc.events {
				t.Fatalf("stream changed: %s", w.Body)
			}
			s := f.settlements[0]
			if s.Status != "failed" || s.ErrorCode != "flex_unavailable" || s.Usage != (domain.Usage{Known: true}) || s.ChargeMicros != 0 || s.CostMicros != 0 {
				t.Fatalf("settlement %+v", s)
			}
			if len(limiter.cooled) != 0 || limiter.acquired != limiter.released {
				t.Fatalf("paused %v, acquired %d released %d", limiter.cooled, limiter.acquired, limiter.released)
			}
		})
	}
	// A refusal after output was produced leaves the upstream cost pending.
	events := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_late\",\"service_tier\":\"flex\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\ndata: " + refusal + "\n\n"
	up := newFlexUpstream(t, answer(200, "text/event-stream", events))
	h, f, _ := flexFixture(up.server.URL, nil)
	path, body := flexRequest("responses-stream", `,"service_tier":"flex"`)
	if w := serve(h, path, body); w.Code != 200 || w.Body.String() != events {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
	if s := f.settlements[0]; s.Status != "failed" || s.ErrorCode != "flex_unavailable" || s.Usage.Known || s.ChargeMicros != 0 {
		t.Fatalf("late refusal settlement %+v", s)
	}
}

// A JSON reply whose error refuses Flex is answered like the 429.
func TestFlexRefusalInAJSONReply(t *testing.T) {
	reply := `{"id":"resp_x","object":"response","status":"failed","service_tier":"flex","error":{"code":"flex_unavailable","message":"Flex processing is temporarily unavailable."},"output":[]}`
	up := newFlexUpstream(t, answer(200, "application/json", reply))
	h, f, limiter := flexFixture(up.server.URL, nil)
	path, body := flexRequest("responses", `,"service_tier":"flex"`)
	w := serve(h, path, body)
	if w.Code != 429 || !strings.Contains(w.Body.String(), `"code":"flex_unavailable"`) {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
	if s := f.settlements[0]; s.Status != "failed" || s.ErrorCode != "flex_unavailable" || s.Usage != (domain.Usage{Known: true}) || s.ChargeMicros != 0 || len(limiter.cooled) != 0 {
		t.Fatalf("settlement %+v paused %v", s, limiter.cooled)
	}
}

// A Flex request goes only to official OpenAI routes: never to a backup, even
// when the official route fails, and it is refused with 400 before anything
// is reserved when a backup is the model's only route.
func TestFlexNeverUsesNonOfficialRoutes(t *testing.T) {
	_, ok := flexReply("chat", "")
	backup := newFlexUpstream(t, answer(200, "application/json", ok))
	official := newFlexUpstream(t, answer(503, "application/json", `{"error":{"message":"overloaded","type":"server_error","code":"server_is_overloaded"}}`))
	h, f, _ := flexFixture(official.server.URL, map[string]*flexUpstream{"openrouter": backup})
	path, body := flexRequest("chat", `,"service_tier":"flex"`)
	if w := serve(h, path, body); w.Code != 503 || backup.calls() != 0 || official.calls() != 1 {
		t.Fatalf("HTTP %d %s; backup %d official %d", w.Code, w.Body, backup.calls(), official.calls())
	}
	// The same request without Flex fails over to the backup.
	path, body = flexRequest("chat", "")
	if w := serve(h, path, body); w.Code != 200 || backup.calls() != 1 || backup.body(0)["service_tier"] != nil {
		t.Fatalf("standard failover HTTP %d %s; backup %d", w.Code, w.Body, backup.calls())
	}
	// With only the backup left, Flex is refused before any reservation.
	f.credentials = f.credentials[1:]
	reservations := len(f.reservations)
	path, body = flexRequest("chat", `,"service_tier":"flex"`)
	w := serve(h, path, body)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "official OpenAI routes") || len(f.reservations) != reservations || backup.calls() != 1 {
		t.Fatalf("HTTP %d %s; reservations %d backup %d", w.Code, w.Body, len(f.reservations)-reservations, backup.calls())
	}
}

// Requests the gateway cannot price at Flex rates are refused with 400 before
// anything is reserved or sent.
func TestFlexRefusedBeforeAnythingIsSent(t *testing.T) {
	up := newFlexUpstream(t, answer(200, "application/json", `{}`))
	for name, tc := range map[string]struct {
		change      func(*domain.Model)
		path, body  string
		wantMessage string
	}{
		"no Flex price":  {func(m *domain.Model) { m.Flex = nil }, "/openai/v1/responses", `{"model":"model","input":"hi","service_tier":"flex"}`, "OpenAI publishes no Flex price for it"},
		"manual price":   {func(m *domain.Model) { m.PricingReference = nil }, "/openai/v1/responses", `{"model":"model","input":"hi","service_tier":"flex"}`, "OpenAI publishes no Flex price for it"},
		"other tier":     {nil, "/openai/v1/responses", `{"model":"model","input":"hi","service_tier":"priority"}`, `and "flex" are supported`},
		"null tier":      {nil, "/openai/v1/chat/completions", `{"model":"model","messages":[],"service_tier":null}`, `and "flex" are supported`},
		"long prompt":    {func(m *domain.Model) { m.Flex.LongContext = nil }, "/openai/v1/responses", `{"model":"model","input":"hi","service_tier":"flex","tools":[{"type":"web_search"}]}`, "up to 272000 tokens"},
		"image endpoint": {nil, "/openai/v1/images/generations", `{"model":"model","prompt":"a cat","service_tier":"flex"}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			h, f, _ := flexFixture(up.server.URL, nil)
			if tc.change != nil {
				tc.change(&f.models[0])
			}
			w := serve(h, tc.path, tc.body)
			var reply struct {
				Error struct{ Message string } `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &reply) != nil || w.Code != 400 || !strings.Contains(reply.Error.Message, tc.wantMessage) {
				t.Fatalf("HTTP %d %s", w.Code, w.Body)
			}
			if len(f.reservations) != 0 || up.calls() != 0 {
				t.Fatalf("reserved %d, sent %d", len(f.reservations), up.calls())
			}
		})
	}
	// Messages keep their own standard tier.
	h, f := fixture("anthropic", up.server.URL)
	if w := serve(h, "/anthropic/v1/messages", `{"model":"model","max_tokens":10,"messages":[],"service_tier":"flex"}`); w.Code != 400 || len(f.reservations) != 0 {
		t.Fatalf("Messages accepted Flex: HTTP %d %s", w.Code, w.Body)
	}
	// OpenAI publishes no Flex price for image generation.
	p := plan{model: flexModel(), imageTool: &imageToolPlan{}}
	if err := p.checkFlex(); err == nil || !strings.Contains(err.Error(), "image generation") {
		t.Fatalf("image tool with Flex: %v", err)
	}
	// Web search keeps its per-call price and stays within the Flex price.
	p = plan{model: flexModel(), input: 5000, webSearch: &webSearchPlan{calls: 10, callMicros: webSearchCallMicros, inputBound: 1_280_000}}
	if err := p.checkFlex(); err != nil {
		t.Fatal(err)
	}
}

// Web search on a Flex request: the searches keep OpenAI's per-call price,
// and the tokens, search content included, are billed at the Flex rates.
func TestFlexWebSearchKeepsTheCallPrice(t *testing.T) {
	reply := strings.Replace(searchReply, `"status":"completed",`, `"status":"completed","service_tier":"flex",`, 1)
	up := newFlexUpstream(t, answer(200, "application/json", reply))
	h, f, _ := flexFixture(up.server.URL, nil)
	request := strings.Replace(searchRequest, `"store":false`, `"store":false,"service_tier":"flex"`, 1)
	if w := serve(h, "/openai/v1/responses", request); w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
	// 10,000 uncached and 2,000 cached input tokens and 800 output tokens at
	// Flex rates: 5,000 + 50 + 2,000 micro-USD, plus two searches at $0.01.
	s := f.settlements[0]
	if s.Usage.WebSearchCalls != 2 || s.Usage.WebSearchMicros != 20_000 || s.ChargeMicros != 27_050 || s.CostMicros != 27_050 || s.Usage.ServiceTier != "flex" {
		t.Fatalf("settlement %+v", s)
	}
	// The hold covers every allowed search and its context window: above
	// 272K input tokens, at the Flex long context rates.
	input := int64(2*len(request)+4096) + 10*webSearchContextTokens
	hold := (input*1_250_000 + 128*3_750_000 + 999_999) / 1_000_000
	if r := f.reservations[0]; r.Request.ReservedMicros != hold+10*webSearchCallMicros || r.CostReserve != hold+10*webSearchCallMicros {
		t.Fatalf("hold %d and %d, want %d", r.Request.ReservedMicros, r.CostReserve, hold+10*webSearchCallMicros)
	}
}

// A streamed Flex request may run up to 15 minutes and holds its concurrency
// slot as long; every other request keeps the 5-minute limit.
func TestFlexStreamDeadline(t *testing.T) {
	for _, tc := range []struct {
		protocol, extra string
		limit           time.Duration
	}{
		{"responses-stream", `,"service_tier":"flex"`, flexStreamTimeout},
		{"chat-stream", `,"service_tier":"flex"`, flexStreamTimeout},
		{"responses", `,"service_tier":"flex"`, requestTimeout},
		{"responses-stream", "", requestTimeout},
	} {
		contentType, reply := flexReply(tc.protocol, "flex")
		up := newFlexUpstream(t, answer(200, contentType, reply))
		h, f, limiter := flexFixture(up.server.URL, nil)
		started := time.Now()
		path, body := flexRequest(tc.protocol, tc.extra)
		if w := serve(h, path, body); w.Code != 200 {
			t.Fatalf("%s%s: HTTP %d %s", tc.protocol, tc.extra, w.Code, w.Body)
		}
		if left := f.deadline.Sub(started); left > tc.limit+time.Second || left < tc.limit-time.Minute {
			t.Fatalf("%s%s: deadline in %s, want %s", tc.protocol, tc.extra, left, tc.limit)
		}
		if limiter.leases[0] != tc.limit+leaseMargin {
			t.Fatalf("%s%s: lease %s", tc.protocol, tc.extra, limiter.leases[0])
		}
	}
	// The client's own disconnect still ends a long Flex stream.
	up := newFlexUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
	})
	h, f, _ := flexFixture(up.server.URL, nil)
	path, body := flexRequest("responses-stream", `,"service_tier":"flex"`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer rp_test")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if s := f.settlements[0]; s.Status != "canceled" || s.ErrorCode != "client_disconnected" {
		t.Fatalf("settlement %+v", s)
	}
}

// Which upstream errors refuse a Flex request for capacity: OpenAI's code,
// type or a message naming Flex, and for a 429 anything that is not plainly a
// rate limit or a billing refusal. Words such as "flexible" name nothing.
func TestFlexRefusalClassification(t *testing.T) {
	for body, want := range map[string]bool{
		`{"error":{"code":"flex_unavailable"}}`:                                                     true,
		`{"error":{"type":"resource_unavailable","code":null}}`:                                     true,
		`{"error":{"message":"Flex processing is temporarily unavailable."}}`:                       true,
		`{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for flex requests"}}`: true,
		`{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached on tokens per min"}}`: false,
		`{"error":{"code":"rate_limit_exceeded","message":"Use a more flexible schedule"}}`:         false,
		`{"error":{"type":"rate_limit_error","code":"slow_down"}}`:                                  false,
		`{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`:                       false,
		`{"error":{"code":"credit_balance_exhausted"}}`:                                             false,
		`Too Many Requests`: true,
	} {
		if flexCapacityRefusal([]byte(body)) != want {
			t.Errorf("429 %s: refusal %v", body, !want)
		}
	}
	for event, want := range map[string]bool{
		`{"type":"error","code":"flex_unavailable","message":"x"}`:                                  true,
		`{"type":"error","error":{"type":"resource_unavailable","code":"flex_unavailable"}}`:        true,
		`{"type":"error","error":{"type":"server_error","message":"a flexible failure"}}`:           false,
		`{"type":"response.failed","response":{"error":{"code":"flex_unavailable","message":"x"}}}`: true,
		`{"type":"response.failed","response":{"error":{"code":"server_error","message":"x"}}}`:     false,
		`{"type":"response.output_text.delta","delta":"flex flex_unavailable"}`:                     false,
		`{"error":{"type":"resource_unavailable","message":"x"}}`:                                   true,
		`{"id":"c","choices":[{"delta":{"content":"flex"}}],"service_tier":"flex","error":null}`:    false,
	} {
		if flexStreamRefusal(object([]byte(event))) != want {
			t.Errorf("event %s: refusal %v", event, !want)
		}
	}
}

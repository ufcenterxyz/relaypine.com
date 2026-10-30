// Package relay forwards a deliberately bounded text, function-tool and inline-image subset of
// the native OpenAI and Anthropic protocols. A request moves to another upstream
// only after a definitive rejection that produced no output; a send whose
// outcome is unknown is never replayed and charged twice.
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/pricing"
	"relaypine.com/gateway/internal/store"
)

const maxRequestBytes = 2 << 20
const maxResponseBytes = 16 << 20

type Repository interface {
	ListEndpoints(context.Context) ([]domain.Endpoint, error)
	ListCredentials(context.Context) ([]domain.Credential, error)
	ListProxies(context.Context) ([]domain.Proxy, error)
	ListModels(context.Context) ([]domain.Model, error)
	AuthenticateKey(context.Context, string) (domain.AccessKey, error)
	Routing(context.Context) (domain.RouteConfig, error)
	Reserve(context.Context, domain.Reservation) error
	Reroute(context.Context, string, domain.Reroute) error
	Settle(context.Context, string, domain.Settlement) error
	SaveResource(context.Context, domain.Resource) error
	Resource(context.Context, string, string, string) (domain.Resource, error)
	SavePayload(context.Context, string, domain.RequestPayload) error
}
type Handler struct {
	repo         Repository
	limiter      routeLimiter
	allowPrivate bool
	capture      bool
	// limits keeps the rate limits official upstreams report for each key.
	limits *limitRecorder
	// target overrides endpoint addressing; tests point vendor presets at
	// local servers. Nil uses domain.Endpoint.Target.
	target func(domain.Endpoint, string) (string, string)
}

// capture records each request's body and the response for request inspection.
func New(repo Repository, client *redis.Client, allowPrivate, capture bool) *Handler {
	return &Handler{repo: repo, limiter: &redisLimiter{client}, allowPrivate: allowPrivate, capture: capture, limits: &limitRecorder{client: client, last: map[string]domain.ReportedLimit{}}}
}

type candidate struct {
	credential domain.Credential
	endpoint   domain.Endpoint
	proxyURL   string
	// buckets are this route's own limits: the credential and its quota group.
	// Tenant and access-key limits are shared by every attempt of a request.
	buckets []limits
	score   float64
	balance int64
	// backup routes are tried only after every primary route.
	backup bool
	// model is the ID sent upstream; base and authMode address the protocol.
	model, base, authMode string
}
type plan struct {
	key                       domain.AccessKey
	model                     domain.Model
	provider, path, requestID string
	// original is the client's body; body is the normalized upstream body.
	original, body []byte
	// fields is body as an object, so a backup can substitute its model ID.
	fields        map[string]json.RawMessage
	anthropicBeta string
	input, output int64
	// tpm is the token estimate charged to rate limits: a realistic figure,
	// where input and output above are strict bounds for the money hold.
	tpm int64
	// limitAdded marks a chat max_completion_tokens the gateway filled in.
	limitAdded              bool
	stream, count, resource bool
	binding                 *domain.Resource
	// image is set for Images API calls. input then includes imageInput, the
	// bound on image input tokens, which is priced separately.
	image      *imageRequest
	imageInput int64
	// imageTool is set when a Responses request offers image_generation.
	imageTool *imageToolPlan
	// inputImages counts the OpenAI image inputs of the request.
	inputImages int
	// contentType is the upstream body type; empty means JSON.
	contentType string
	// originalBytes is the client's body size when original is a summary.
	originalBytes int
}

// attempt is the outcome of one upstream call.
type attempt struct {
	settlement domain.Settlement
	// retry marks a definitive rejection before any output: nothing was written
	// to the client, so another route may still serve the request.
	retry bool
	// endpointWide marks a failure of the upstream service rather than one key.
	endpointWide bool
	// status, code, message and retryAfter describe the rejection to report
	// when no other route accepts the request.
	status                    int
	code, message, retryAfter string
	// upstream is a provider reply that the client did not receive unchanged.
	upstream *domain.CapturedResponse
}

// maxUpstreamAttempts bounds one request: the first route plus at most two
// reroutes after definitive rejections, such as an outage or a revoked key.
const maxUpstreamAttempts = 3

// failureCooldown keeps a key that failed at the service level out of routing
// briefly, so later requests go straight to another route.
const failureCooldown = 30 * time.Second

func requestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "req_" + hex.EncodeToString(b[:])
}
func fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": code, "code": code, "message": message}})
}
func allowed(list []string, id string) bool {
	if len(list) == 0 {
		return true
	}
	for _, v := range list {
		if v == id || v == "*" {
			return true
		}
	}
	return false
}
func validID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	p := plan{requestID: requestID()}
	w.Header().Set("X-Request-Id", p.requestID)
	switch {
	case strings.HasPrefix(r.URL.Path, "/openai/"):
		p.provider = "openai"
		p.path = strings.TrimPrefix(r.URL.Path, "/openai")
	case strings.HasPrefix(r.URL.Path, "/anthropic/"):
		p.provider = "anthropic"
		p.path = strings.TrimPrefix(r.URL.Path, "/anthropic")
	default:
		fail(w, 404, "not_found", "Unknown API route")
		return
	}
	secret := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		secret = strings.TrimPrefix(auth, "Bearer ")
	}
	if p.provider == "anthropic" && secret == "" {
		secret = r.Header.Get("X-Api-Key")
	}
	if secret == "" {
		fail(w, 401, "authentication_error", "An API key is required")
		return
	}
	key, err := h.repo.AuthenticateKey(ctx, secret)
	if err != nil || !key.Enabled {
		fail(w, 401, "authentication_error", "Invalid API key")
		return
	}
	p.key = key
	models, err := h.repo.ListModels(ctx)
	if err != nil {
		fail(w, 503, "configuration_unavailable", "Model registry unavailable")
		return
	}
	if p.path == "/v1/models" && r.Method == http.MethodGet {
		h.models(w, p, models)
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, 400, "unsupported_parameter", "Query parameters are not supported")
		return
	}
	isCreate := p.provider == "openai" && (p.path == "/v1/chat/completions" || p.path == "/v1/responses") || p.provider == "anthropic" && (p.path == "/v1/messages" || p.path == "/v1/messages/count_tokens")
	isImage := p.provider == "openai" && (p.path == imageGenerationsPath || p.path == imageEditsPath)
	if isImage && r.Method == http.MethodPost {
		if err = h.prepareImage(r, &p, models); err != nil {
			fail(w, 400, "invalid_request_error", err.Error())
			return
		}
	} else if isCreate && r.Method == http.MethodPost {
		p.count = strings.HasSuffix(p.path, "/count_tokens")
		if err = h.prepare(r, &p, models); err != nil {
			fail(w, 400, "invalid_request_error", err.Error())
			return
		}
	} else if p.provider == "openai" && strings.HasPrefix(p.path, "/v1/responses/") && (r.Method == http.MethodGet || r.Method == http.MethodDelete) {
		id := strings.TrimPrefix(p.path, "/v1/responses/")
		if !validID(id) {
			fail(w, 404, "not_found", "Resource not found")
			return
		}
		binding, e := h.repo.Resource(ctx, key.TenantID, key.ProjectID, id)
		if e != nil {
			fail(w, 404, "not_found", "Resource not found")
			return
		}
		p.binding = &binding
		p.resource = true
		for _, m := range models {
			if m.ID == binding.Model && m.Enabled && m.Provider == p.provider && allowed(key.Models, m.ID) {
				p.model = m
				break
			}
		}
		if p.model.ID == "" {
			fail(w, 403, "model_not_available", "Resource model is not available to this key")
			return
		}
	} else {
		fail(w, 404, "unsupported_operation", "This API operation is not supported")
		return
	}
	config, err := h.repo.Routing(ctx)
	if err != nil {
		fail(w, 503, "configuration_unavailable", "Routing configuration unavailable")
		return
	}
	candidates, err := h.candidates(ctx, p, config)
	if err != nil {
		if errors.Is(err, store.ErrUntrackedCostBudget) {
			fail(w, 402, "untracked_cost_budget", "Usage-only models cannot enforce an upstream monetary budget. Use a credential without a local monetary cap, or configure this model for metered billing.")
			return
		}
		fail(w, 503, "upstream_unavailable", "No usable upstream configuration")
		return
	}
	if len(candidates) == 0 {
		fail(w, 503, "upstream_unavailable", "No eligible upstream for this model")
		return
	}
	reserved, costReserve := int64(0), int64(0)
	if p.image != nil {
		reserved, costReserve, err = estimateImage(p.input-p.imageInput, p.imageInput, p.output, p.model)
		if err != nil {
			fail(w, 400, "invalid_price", "Unable to reserve this request")
			return
		}
	} else if !p.count && !p.resource {
		reserved, costReserve, err = estimate(p.input, p.output, p.model)
		if err != nil {
			fail(w, 400, "invalid_price", "Unable to reserve this request")
			return
		}
	}
	if p.imageTool != nil {
		// Every allowed image call at its worst case, at the image model's rates.
		text, image, output := p.imageTool.bounds()
		sell, cost, err := estimateImage(text, image, output, p.imageTool.model)
		if err != nil {
			fail(w, 400, "invalid_price", "Unable to reserve this request")
			return
		}
		reserved, costReserve = reserved+sell, costReserve+cost
	}
	h.route(w, r, p, sharedLimits(p, config), candidates, reserved, costReserve)
}

// sharedLimits are the tenant and access-key limits. Every attempt of one
// request holds them once; each route adds its own credential limits.
// tokenHold is where an estimate was charged: a set of buckets in one window.
type tokenHold struct {
	buckets []limits
	window  int64
}

// adjustTokens moves an estimate charged to a hold by delta.
func (h *Handler) adjustTokens(hold tokenHold, delta int64) {
	if len(hold.buckets) == 0 || delta == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.limiter.adjust(ctx, hold.buckets, hold.window, delta)
}

// usedTokens are the tokens a request consumed, in the units of its estimate:
// cache reads and writes count, and an image tool's tokens belong to the image
// model rather than to this request's estimate.
// usedTokens is what a request counts against the per-minute token limits,
// the way its provider counts them. OpenAI counts every input token, cached
// or not, and the output. Anthropic limits input (ITPM) and output (OTPM)
// apart: uncached input and cache writes count toward ITPM, cache reads do
// not except on Claude Haiku 3.5, and output has its own limit. An Anthropic
// key's TPM is therefore its ITPM, and output is left to Anthropic.
// https://platform.claude.com/docs/en/api/rate-limits
func usedTokens(p plan, u domain.Usage) int64 {
	if p.provider == "anthropic" {
		n := u.InputTokens + u.CacheWriteTokens
		if countsCacheReads(p.model.UpstreamModel) {
			n += u.CachedTokens
		}
		return n
	}
	n := u.InputTokens + u.CachedTokens + u.CacheWriteTokens + u.OutputTokens
	if p.imageTool == nil {
		n += u.ImageInputTokens + u.ImageOutputTokens + u.ImageTextTokens
	}
	return n
}

func sharedLimits(p plan, config domain.RouteConfig) []limits {
	return []limits{{"tenant:" + p.key.TenantID, config.TenantRPM, 0, config.TenantConcurrency}, {"access:" + p.key.ID, p.key.RPM, 0, 0}}
}

// route reserves the first admissible candidate and forwards the request. When
// an upstream definitively rejects an attempt before any output (the request
// never left, or HTTP 401/402/403/429/5xx), the request moves to the next
// candidate. Candidates list every primary route before any backup, so a backup
// serves a request only after no primary route could.
func (h *Handler) route(w http.ResponseWriter, r *http.Request, p plan, shared []limits, candidates []candidate, reserved, costReserve int64) {
	ctx := r.Context()
	capture := &responseCapture{ResponseWriter: w}
	if h.capture {
		w = capture
	}
	var current *candidate
	var held []limits
	var last attempt
	// The estimate is charged to the shared buckets once and to each route
	// that admits the request; settlement turns it into the tokens used.
	estimate := p.tpm
	var sharedTokens, routeTokens tokenHold
	var capacityErr error
	var untrackedBudget bool
	failedEndpoints := map[string]bool{}
	attempts := 0
	started := time.Now()
	release := func(buckets []limits) error {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.limiter.release(cleanup, p.requestID, buckets)
	}
	// Deferred calls run in reverse: settle, store the inspection copy, and only
	// then return the held capacity.
	defer func() {
		if current != nil {
			_ = release(held)
			// An unknown total keeps the estimate, which is an upper bound.
			if u := last.settlement.Usage; u.Known {
				delta := usedTokens(p, u) - estimate
				h.adjustTokens(sharedTokens, delta)
				h.adjustTokens(routeTokens, delta)
			}
		}
	}()
	defer func() {
		if current == nil {
			return
		}
		last.settlement.LatencyMS = time.Since(started).Milliseconds()
		settleCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = h.repo.Settle(settleCtx, p.requestID, last.settlement)
		cancel()
		// Settlement is written first; a body that cannot be stored never
		// affects accounting.
		if h.capture {
			h.savePayload(r, p, capture, last.upstream)
		}
	}()
	for i := range candidates {
		if attempts >= maxUpstreamAttempts || ctx.Err() != nil {
			break
		}
		c := &candidates[i]
		if failedEndpoints[c.endpoint.ID] {
			continue
		}
		// Cost is a hard eligibility condition, even when load is the ranking
		// policy. Avoid consuming rate capacity for a predictably unfunded key.
		if c.credential.BudgetMicros > 0 && c.balance < costReserve {
			continue
		}
		buckets := c.buckets
		if current == nil {
			buckets = append(append([]limits{}, shared...), c.buckets...)
		} else if len(held) > len(shared) {
			// The refused route no longer runs this request. Free its capacity
			// before taking another route's, which may share its quota group.
			_ = release(held[len(shared):])
			held = shared
		}
		charged, err := h.limiter.acquire(ctx, p.requestID, buckets, estimate)
		if errors.Is(err, errCapacity) {
			capacityErr = err
			continue
		}
		if err != nil {
			if current == nil {
				fail(w, 503, "limiter_unavailable", "Rate limiter unavailable")
				return
			}
			break
		}
		if current == nil {
			reservation := domain.Reservation{Request: domain.Request{ID: p.requestID, TenantID: p.key.TenantID, ProjectID: p.key.ProjectID, AccessKeyID: p.key.ID, CredentialID: c.credential.ID, Provider: p.provider, Model: p.model.ID, Status: "reserved", ReservedMicros: reserved, CreatedAt: time.Now()}, Model: p.model, CredentialID: c.credential.ID, CostReserve: costReserve}
			err = h.repo.Reserve(ctx, reservation)
		} else {
			err = h.repo.Reroute(ctx, p.requestID, domain.Reroute{FromCredentialID: current.credential.ID, ToCredentialID: c.credential.ID, CostReserve: costReserve, ErrorCode: last.settlement.ErrorCode})
		}
		if err != nil {
			// No upstream call has been made on this route, and a rejected money
			// transaction holds nothing. Release it, and the tokens it was charged,
			// before trying another budget.
			h.adjustTokens(tokenHold{buckets, charged}, -estimate)
			if releaseErr := release(buckets); releaseErr != nil && current == nil {
				fail(w, 503, "limiter_unavailable", "Unable to release route capacity")
				return
			}
			if errors.Is(err, store.ErrUntrackedCostBudget) {
				untrackedBudget = true
				continue
			}
			if errors.Is(err, store.ErrBudgetExceeded) {
				continue
			}
			if current != nil {
				// The earlier rejection still describes this request.
				break
			}
			// Customer balance and storage failures cannot be solved by changing
			// an upstream key. Only an atomic upstream-budget race retries routing.
			fail(w, 402, "budget_unavailable", "Customer balance or upstream budget is insufficient, or its reservation is unavailable")
			return
		}
		if current == nil {
			started = time.Now()
			sharedTokens = tokenHold{shared, charged}
		}
		routeTokens = tokenHold{c.buckets, charged}
		current = c
		held = append(append([]limits{}, shared...), c.buckets...)
		attempts++
		last = h.forward(w, r, p, *c)
		if !last.retry {
			return
		}
		// The refused route ran no inference: return its whole estimate.
		h.adjustTokens(routeTokens, -estimate)
		routeTokens = tokenHold{}
		if last.endpointWide {
			failedEndpoints[c.endpoint.ID] = true
		}
	}
	if current == nil {
		if capacityErr != nil {
			w.Header().Set("Retry-After", "1")
			fail(w, 429, "rate_limit_exceeded", "All eligible upstream capacity is in use")
		} else if untrackedBudget {
			fail(w, 402, "untracked_cost_budget", "Usage-only models cannot enforce an upstream monetary budget. Use a credential without a local monetary cap, or configure this model for metered billing.")
		} else {
			fail(w, 402, "budget_unavailable", "No eligible upstream has sufficient remaining budget")
		}
		return
	}
	// No other route accepted the request; report the last rejection.
	if last.retryAfter != "" {
		w.Header().Set("Retry-After", last.retryAfter)
	}
	fail(w, last.status, last.code, last.message)
}

func (h *Handler) models(w http.ResponseWriter, p plan, models []domain.Model) {
	endpoints, err := h.repo.ListEndpoints(context.Background())
	if err != nil {
		fail(w, 503, "configuration_unavailable", "Model registry unavailable")
		return
	}
	items := []map[string]any{}
	for _, m := range models {
		accounting := validAccounting(m)
		if m.IsImage() {
			// GPT Image models publish no text output price, so they have their
			// own accounting check.
			accounting = validImageAccounting(m)
		}
		if !m.Enabled || !accounting || m.Provider != p.provider || !allowed(p.key.Models, m.ID) {
			continue
		}
		usable := false
		for _, e := range endpoints {
			if !e.Enabled {
				continue
			}
			if e.IsBackup() {
				// Image models are served only through official credentials.
				if !m.IsImage() {
					_, usable = BackupModel(e.Vendor, p.provider, m.UpstreamModel)
				}
			} else {
				usable = e.Provider == p.provider && (m.PricingReference == nil || pricing.OfficialEndpoint(e.Provider, e.BaseURL)) && (allowed(e.Models, m.ID) || allowed(e.Models, m.UpstreamModel))
			}
			if usable {
				break
			}
		}
		if usable {
			items = append(items, map[string]any{"id": m.ID, "object": "model", "owned_by": "relaypine", "display_name": m.Name, "type": "model"})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": items, "has_more": false})
}

func (h *Handler) prepare(r *http.Request, p *plan, models []domain.Model) error {
	if p.provider == "anthropic" {
		beta, err := anthropicBetaHeader(r.Header)
		if err != nil {
			return err
		}
		p.anthropicBeta = beta
	}
	if enc := r.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return errors.New("compressed request bodies are not supported")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		return errors.New("unable to read request")
	}
	if len(body) > maxRequestBytes {
		return errors.New("request body exceeds 2 MiB")
	}
	p.original = body
	m := object(body)
	if m == nil {
		return errors.New("a JSON object is required")
	}
	id := str(m["model"])
	for _, model := range models {
		if model.ID == id && model.Enabled && model.Provider == p.provider && allowed(p.key.Models, id) {
			p.model = model
			break
		}
	}
	if p.model.ID == "" {
		return errors.New("model is unavailable or not allowed for this key")
	}
	if p.model.IsImage() {
		return errors.New("image models are served by the Images API: /v1/images/generations and /v1/images/edits")
	}
	if !validAccounting(p.model) || p.model.MaxOutputTokens <= 0 {
		return errors.New("model has no complete price and output limit configuration")
	}
	images, err := validatePayloadWithImages(m, p.path, p.model.UpstreamModel)
	if err != nil {
		return err
	}
	p.inputImages = images.OpenAIImages
	p.input, err = estimatedInputTokens(len(body), images)
	if err == nil {
		p.tpm = admissionTokens(len(body), images, p.input)
	}
	if err != nil {
		return err
	}
	if raw, exists := m["previous_response_id"]; exists && string(raw) != "null" {
		rid := str(raw)
		if !validID(rid) {
			return errors.New("invalid previous_response_id")
		}
		binding, err := h.repo.Resource(r.Context(), p.key.TenantID, p.key.ProjectID, rid)
		if err != nil {
			return errors.New("previous response is not available to this project")
		}
		if binding.Model != p.model.ID {
			return errors.New("continuation must use the original model")
		}
		if binding.InputTokens <= 0 || binding.InputTokens > 1<<38 {
			return errors.New("previous response input bound is unavailable")
		}
		p.binding = &binding
		p.input += binding.InputTokens
		p.tpm += binding.InputTokens
	}
	// A verified standard price covers prompts up to MaxInputTokens. Longer
	// prompts are priced by the model's long context rates, or need none
	// when the provider has no higher price for them. A reference saved
	// before long context prices were recorded still refuses them.
	if ref := p.model.PricingReference; ref != nil && !ref.AllPromptLengths && p.model.LongContext == nil && (ref.MaxInputTokens <= 0 || p.input > ref.MaxInputTokens) {
		return errors.New("request exceeds the standard reference price context limit; shorten the input or configure a separately priced model")
	}
	if !p.count {
		field := "max_tokens"
		if strings.HasSuffix(p.path, "/responses") {
			field = "max_output_tokens"
		}
		if strings.HasSuffix(p.path, "/chat/completions") {
			// OpenAI deprecated max_tokens and its reasoning models refuse it, so a
			// request that names neither limit is given max_completion_tokens.
			field = "max_completion_tokens"
			if _, ok := m["max_tokens"]; ok {
				field = "max_tokens"
				if _, both := m["max_completion_tokens"]; both {
					return errors.New("specify only one maximum output parameter")
				}
			}
		}
		p.output = int64(p.model.MaxOutputTokens)
		if raw, ok := m[field]; ok {
			n, valid := integer(raw)
			if !valid || n <= 0 || n > p.output {
				return errors.New("maximum output tokens must be within the model limit")
			}
			p.output = n
		} else if field == "max_completion_tokens" {
			p.limitAdded = true
		}
		m[field] = json.RawMessage(strconv.FormatInt(p.output, 10))
	}
	if raw, ok := m["stream"]; ok {
		if json.Unmarshal(raw, &p.stream) != nil {
			return errors.New("stream must be a boolean")
		}
	}
	if p.stream && p.count {
		return errors.New("count_tokens does not support streaming")
	}
	if p.stream && strings.HasSuffix(p.path, "/chat/completions") {
		options := object(m["stream_options"])
		if options == nil {
			options = map[string]json.RawMessage{}
		}
		options["include_usage"] = json.RawMessage("true")
		m["stream_options"], _ = json.Marshal(options)
	}
	if err = h.prepareImageTool(p, m, models); err != nil {
		return err
	}
	// OpenAI admits a request with its output limit; Anthropic counts output
	// only as it is generated, against a separate limit.
	if p.provider != "anthropic" {
		p.tpm += p.output
	}
	m["model"], _ = json.Marshal(p.model.UpstreamModel)
	p.fields = m
	p.body, err = json.Marshal(m)
	return err
}

// upstreamBody is the body for one route. A backup receives its own catalog ID
// and no service tier: the forced standard tier only describes the official
// price, and an aggregator applies its own processing tiers.
func (p plan) upstreamBody(c candidate) ([]byte, error) {
	if !c.backup || p.image != nil {
		return p.body, nil
	}
	fields := make(map[string]json.RawMessage, len(p.fields))
	for k, v := range p.fields {
		fields[k] = v
	}
	fields["model"], _ = json.Marshal(c.model)
	delete(fields, "service_tier")
	if p.limitAdded {
		// Backups have always received the gateway's own limit as max_tokens.
		fields["max_tokens"] = fields["max_completion_tokens"]
		delete(fields, "max_completion_tokens")
	}
	return json.Marshal(fields)
}

func (h *Handler) candidates(ctx context.Context, p plan, config domain.RouteConfig) ([]candidate, error) {
	endpoints, err := h.repo.ListEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	credentials, err := h.repo.ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	proxies, err := h.repo.ListProxies(ctx)
	if err != nil {
		return nil, err
	}
	result := []candidate{}
	untrackedBudget := false
	for _, c := range credentials {
		if !c.Enabled || c.Secret == "" {
			continue
		}
		if p.binding != nil && c.ID != p.binding.CredentialID {
			continue
		}
		var endpoint domain.Endpoint
		for _, e := range endpoints {
			if e.ID == c.EndpointID {
				endpoint = e
				break
			}
		}
		if endpoint.ID == "" || !endpoint.Enabled || p.binding != nil && p.binding.EndpointID != endpoint.ID {
			continue
		}
		model := p.model.UpstreamModel
		if endpoint.IsBackup() {
			// A backup has no stored model allowlist: it carries the official
			// model under the vendor's catalog name, and only for stateless calls.
			// Official reference prices still apply to the customer.
			id, ok := BackupModel(endpoint.Vendor, p.provider, p.model.UpstreamModel)
			if !ok || p.binding != nil || p.resource || !backupPath(p.provider, p.path) {
				continue
			}
			model = id
		} else if endpoint.Provider != p.provider || p.model.PricingReference != nil && !pricing.OfficialEndpoint(endpoint.Provider, endpoint.BaseURL) || !allowed(endpoint.Models, p.model.ID) && !allowed(endpoint.Models, p.model.UpstreamModel) {
			continue
		}
		base, authMode := endpoint.Target(p.provider)
		if h.target != nil {
			base, authMode = h.target(endpoint, p.provider)
		}
		if _, err = upstreamURL(base, p.path, h.allowPrivate); err != nil {
			continue
		}
		proxyURL := ""
		if c.ProxyID != "" {
			for _, proxy := range proxies {
				if proxy.ID == c.ProxyID && proxy.Enabled {
					proxyURL = proxy.URL
					break
				}
			}
			if proxyURL == "" {
				continue
			}
		}
		if p.model.EffectiveBillingMode() == domain.BillingUsageOnly && c.BudgetMicros > 0 {
			untrackedBudget = true
			continue
		}
		if c.BudgetMicros > 0 && (c.SpentMicros >= c.BudgetMicros || c.HeldMicros >= c.BudgetMicros-c.SpentMicros) {
			continue
		}
		buckets := []limits{{"credential:" + c.ID, c.RPM, c.TPM, c.MaxConcurrency}}
		if c.QuotaGroup != "" {
			buckets = append(buckets, groupLimits(c, credentials))
		}
		load, err := h.limiter.load(ctx, append(sharedLimits(p, config), buckets...))
		if err != nil {
			return nil, err
		}
		balance := int64(math.MaxInt64)
		if c.BudgetMicros > 0 {
			balance = c.BudgetMicros - c.SpentMicros - c.HeldMicros
		}
		result = append(result, candidate{credential: c, endpoint: endpoint, proxyURL: proxyURL, buckets: buckets, score: load, balance: balance, backup: endpoint.IsBackup(), model: model, base: base, authMode: authMode})
	}
	if len(result) == 0 && untrackedBudget {
		return nil, store.ErrUntrackedCostBudget
	}
	// Balanced strategy accounts for normalized load and the remaining budget
	// fraction; priority is a stable tie-breaker, never an override of capacity.
	// Backups always follow every primary route, in the operator's order.
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.backup != b.backup {
			return !a.backup
		}
		if a.backup {
			if a.credential.Priority != b.credential.Priority {
				return a.credential.Priority > b.credential.Priority
			}
			return a.credential.ID < b.credential.ID
		}
		switch config.Strategy {
		case "highest_balance":
			if a.score >= 1 || b.score >= 1 {
				if (a.score >= 1) != (b.score >= 1) {
					return a.score < 1
				}
			}
			if a.balance != b.balance {
				return a.balance > b.balance
			}
		case "balanced":
			sa, sb := a.score, b.score
			if a.credential.BudgetMicros > 0 {
				sa += 1 - float64(a.balance)/float64(a.credential.BudgetMicros)
			}
			if b.credential.BudgetMicros > 0 {
				sb += 1 - float64(b.balance)/float64(b.credential.BudgetMicros)
			}
			if sa != sb {
				return sa < sb
			}
		default:
			if a.score != b.score {
				return a.score < b.score
			}
		}
		if a.credential.Priority != b.credential.Priority {
			return a.credential.Priority > b.credential.Priority
		}
		return a.credential.ID < b.credential.ID
	})
	return result, nil
}

// forward makes one upstream attempt and writes the client response, except
// for a definitive rejection that arrived before any output. That attempt is
// returned unwritten with retry set, so the caller can try another route or
// report it. The caller settles the request once, after its last attempt.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, p plan, c candidate) (result attempt) {
	settlement := &result.settlement
	settlement.Status = "unknown"
	reject := func(status int, message string) attempt {
		result.retry = true
		result.status, result.code, result.message = status, "upstream_error", message
		return result
	}
	tr, err := transport(c.proxyURL, h.allowPrivate)
	if err != nil {
		*settlement = domain.Settlement{Status: "not_sent", Usage: domain.Usage{Known: true}, ErrorCode: "proxy_configuration"}
		return reject(502, "Upstream proxy configuration unavailable")
	}
	defer tr.CloseIdleConnections()
	target, err := upstreamURL(c.base, p.path, h.allowPrivate)
	if err != nil {
		*settlement = domain.Settlement{Status: "not_sent", Usage: domain.Usage{Known: true}, ErrorCode: "upstream_configuration"}
		return reject(502, "Upstream configuration unavailable")
	}
	body, err := p.upstreamBody(c)
	var upstream *http.Request
	var sent atomic.Bool
	if err == nil {
		ctx := httptrace.WithClientTrace(r.Context(), &httptrace.ClientTrace{WroteHeaders: func() { sent.Store(true) }, WroteRequest: func(httptrace.WroteRequestInfo) { sent.Store(true) }})
		upstream, err = http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
	}
	if err != nil {
		*settlement = domain.Settlement{Status: "not_sent", Usage: domain.Usage{Known: true}, ErrorCode: "invalid_upstream"}
		fail(w, 502, "upstream_unavailable", "Invalid upstream request")
		return
	}
	contentType := p.contentType
	if contentType == "" {
		contentType = "application/json"
	}
	upstream.Header.Set("Content-Type", contentType)
	upstream.Header.Set("Accept", "application/json")
	if p.stream {
		upstream.Header.Set("Accept", "text/event-stream")
	}
	// An allowlist prevents cookie, connection-token and internal-header leakage.
	if p.provider == "anthropic" {
		version := r.Header.Get("Anthropic-Version")
		if version == "" {
			version = "2023-06-01"
		}
		upstream.Header.Set("Anthropic-Version", version)
		// Workspace scope belongs to the selected upstream credential. A client
		// must not be able to move that credential's spend to another workspace.
		if c.credential.WorkspaceID != "" {
			upstream.Header.Set("Anthropic-Workspace-Id", c.credential.WorkspaceID)
		}
		if p.anthropicBeta != "" {
			upstream.Header.Set("Anthropic-Beta", p.anthropicBeta)
		}
	}
	if c.authMode == "bearer" || p.provider == "openai" && c.authMode != "x-api-key" {
		upstream.Header.Set("Authorization", "Bearer "+c.credential.Secret)
	} else {
		upstream.Header.Set("X-Api-Key", c.credential.Secret)
	}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(upstream)
	if err == nil && !c.backup {
		if l, ok := reportedLimit(c.endpoint.Provider, response.Header); ok {
			h.limits.note(c.credential.ID, c.model, l)
		}
	}
	if err != nil {
		settlement.Status = "failed"
		settlement.ErrorCode = "upstream_transport"
		if !sent.Load() {
			settlement.Status = "not_sent"
			settlement.Usage.Known = true
			if r.Context().Err() == nil {
				// The request never reached this upstream, so another route can
				// take it without any risk of a second inference run.
				h.cool([]limits{{scope: "credential:" + c.credential.ID}}, failureCooldown)
				return reject(502, "Upstream connection failed")
			}
		}
		fail(w, 502, "upstream_error", "Upstream connection failed")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := response.StatusCode
		raw, _ := io.ReadAll(io.LimitReader(response.Body, maxCapturedUpstreamBytes+1))
		result.upstream = upstreamReply(response, raw, maxCapturedUpstreamBytes)
		h.coolRejected(c, response)
		settlement.Status = "failed"
		settlement.ErrorCode = fmt.Sprintf("upstream_%d", status)
		clientStatus := status
		if status < 400 || status == 401 || status == 402 || status == 403 {
			clientStatus = 502
		}
		if status == 429 {
			result.retryAfter = strconv.Itoa(int(retryDelay(response.Header.Get("Retry-After")).Seconds()))
		}
		if unavailableStatus(status) {
			// The upstream refused the request outright, which runs no inference
			// and has no upstream cost. Another route may still serve it.
			settlement.Usage = domain.Usage{Known: true}
			result.endpointWide = status >= 500
			return reject(clientStatus, "The selected upstream rejected or failed the request")
		}
		if result.retryAfter != "" {
			w.Header().Set("Retry-After", result.retryAfter)
		}
		fail(w, clientStatus, "upstream_error", upstreamError(raw))
		return
	}
	if p.stream {
		if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
			settlement.Status = "failed"
			settlement.ErrorCode = "invalid_stream"
			raw, _ := io.ReadAll(io.LimitReader(response.Body, maxCapturedUpstreamBytes+1))
			result.upstream = upstreamReply(response, raw, maxCapturedUpstreamBytes)
			fail(w, 502, "upstream_error", "Expected an SSE response")
			return
		}
		h.stream(w, r, response, p, c, settlement)
		return
	}
	if p.image != nil {
		h.relayImage(w, r, response, p, settlement)
		return
	}
	// Replies that can carry generated images: an image tool call, or a
	// stored response retrieved by ID.
	images := p.imageTool != nil || p.resource
	limit := int64(maxResponseBytes)
	if images {
		limit = maxImageToolResponseBytes
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	// Every rejection below returns a gateway error instead of this body.
	rejected := func() { result.upstream = upstreamReply(response, body, maxCapturedUpstreamBytes) }
	if err != nil || int64(len(body)) > limit {
		settlement.Status = "failed"
		settlement.ErrorCode = "invalid_response"
		rejected()
		fail(w, 502, "upstream_error", "Unable to read bounded upstream response")
		return
	}
	// A reply with generated images is parsed without them.
	parsed := body
	if images {
		parsed = compactImageJSON(body)
	}
	obj := object(parsed)
	if obj == nil {
		settlement.Status = "failed"
		settlement.ErrorCode = "invalid_response"
		rejected()
		fail(w, 502, "upstream_error", "Expected a JSON response")
		return
	}
	// The image model's tokens join the text model's verified usage.
	toolUsage := func() {
		if p.imageTool != nil && settlement.Usage.Known {
			u := &settlement.Usage
			u.ImageTextTokens, u.ImageInputTokens, u.ImageOutputTokens = p.imageTool.usage(obj, 0)
		}
	}
	if _, exists := obj["error"]; exists && string(obj["error"]) != "null" {
		settlement.Status = "failed"
		settlement.ErrorCode = "upstream_error"
		settlement.Usage, _ = parseUsage(obj["usage"], p.provider)
		toolUsage()
		h.price(settlement, p)
		rejected()
		fail(w, 502, "upstream_error", "The selected upstream returned an error")
		return
	}
	if status := str(obj["status"]); status == "failed" || status == "cancelled" || status == "canceled" {
		settlement.Status = "failed"
		settlement.ErrorCode = "upstream_response_failed"
		settlement.Usage, _ = parseUsage(obj["usage"], p.provider)
		toolUsage()
		h.price(settlement, p)
		rejected()
		fail(w, 502, "upstream_error", "The upstream response did not complete")
		return
	}
	if !p.resource && strings.HasSuffix(p.path, "/responses") {
		if status := str(obj["status"]); status == "queued" || status == "in_progress" {
			settlement.Status = "failed"
			settlement.ErrorCode = "background_unsupported"
			rejected()
			fail(w, 502, "upstream_error", "Background responses are unsupported")
			return
		}
		if err = h.saveResource(r.Context(), p, c, str(obj["id"])); err != nil {
			settlement.Status = "failed"
			settlement.ErrorCode = "resource_binding_failed"
			rejected()
			fail(w, 503, "storage_error", "Unable to persist response ownership")
			return
		}
	}
	if p.count || p.resource {
		settlement.Usage.Known = true
	} else {
		settlement.Usage, _ = parseUsage(obj["usage"], p.provider)
		toolUsage()
	}
	settlement.Status = "completed"
	h.price(settlement, p)
	if capture, ok := w.(*responseCapture); ok && images {
		// Inspection keeps the reply without its images.
		capture.summarize(parsed)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err = w.Write(body); err != nil {
		settlement.Status = "canceled"
		settlement.ErrorCode = "client_disconnected"
		// The upstream already returned a complete response with verified usage.
		// A failed client write does not undo that completed inference work.
		result.upstream = upstreamReply(response, body, maxCapturedResponseBytes)
	}
	return
}

func (h *Handler) saveResource(ctx context.Context, p plan, c candidate, id string) error {
	if !validID(id) {
		return errors.New("invalid response ID")
	}
	return h.repo.SaveResource(ctx, domain.Resource{ID: id, TenantID: p.key.TenantID, ProjectID: p.key.ProjectID, CredentialID: c.credential.ID, EndpointID: c.endpoint.ID, Model: p.model.ID, InputTokens: p.input + p.output})
}
func (h *Handler) price(s *domain.Settlement, p plan) {
	m := p.model
	if m.EffectiveBillingMode() == domain.BillingUsageOnly {
		// Token evidence is independent from pricing. Unknown cost must not
		// erase verified usage or become a synthetic free monetary settlement.
		s.ChargeMicros, s.CostMicros = 0, 0
		return
	}
	if !s.Usage.Known {
		return
	}
	usage := s.Usage
	if p.imageTool != nil {
		// The image tool's tokens are the image model's, priced below.
		usage.ImageTextTokens, usage.ImageInputTokens, usage.ImageOutputTokens = 0, 0, 0
	}
	charge, cost, err := charges(usage, m)
	if err == nil && p.imageTool != nil {
		tool := domain.Usage{InputTokens: s.Usage.ImageTextTokens, ImageInputTokens: s.Usage.ImageInputTokens, ImageOutputTokens: s.Usage.ImageOutputTokens, Known: true}
		var toolCharge, toolCost int64
		if toolCharge, toolCost, err = charges(tool, p.imageTool.model); err == nil {
			charge, cost = charge+toolCharge, cost+toolCost
		}
	}
	if err != nil {
		s.Usage.Known = false
		s.ErrorCode = "invalid_usage"
		return
	}
	s.CostMicros = cost
	if s.Status == "completed" || s.Status == "canceled" {
		s.ChargeMicros = charge
	}
}
func retryDelay(value string) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		if seconds > 86400 {
			seconds = 86400
		}
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d > 0 {
			if d > 24*time.Hour {
				return 24 * time.Hour
			}
			return d
		}
	}
	return 30 * time.Second
}

// unavailableStatus reports an upstream refusal that says the route itself is
// unusable right now (credentials, billing, permission, rate or service
// failure) rather than that the request is invalid. Such a refusal runs no
// inference, so another route may serve the same request.
func unavailableStatus(status int) bool {
	return status == 401 || status == 402 || status == 403 || status == 429 || status >= 500 && status <= 599
}

// coolRejected keeps a refused key out of routing for a while, so later
// requests go straight to another route instead of meeting the same failure.
func (h *Handler) coolRejected(c candidate, response *http.Response) {
	status, retryAfter := response.StatusCode, response.Header.Get("Retry-After")
	scopes := []limits{{scope: "credential:" + c.credential.ID}}
	var d time.Duration
	switch {
	case status == 429:
		d = retryDelay(retryAfter)
		if c.credential.QuotaGroup != "" {
			scopes = append(scopes, limits{scope: "group:" + c.credential.QuotaGroup})
		}
	case status == 401 || status == 403:
		d = 5 * time.Minute
	case status == 402:
		d = 5 * time.Minute
		if retryAfter != "" {
			d = retryDelay(retryAfter)
		}
	case status >= 500:
		d = min(retryDelay(retryAfter), 5*time.Minute)
	default:
		return
	}
	h.cool(scopes, d)
}

// cool is independent of the client: a disconnected caller must not keep a
// failing upstream in rotation.
func (h *Handler) cool(scopes []limits, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = h.limiter.cool(ctx, scopes, d)
}

// countsCacheReads reports a Claude model whose cache reads count toward
// ITPM: Claude Haiku 3.5.
func countsCacheReads(upstream string) bool {
	return strings.HasPrefix(upstream, "claude-3-5-haiku")
}

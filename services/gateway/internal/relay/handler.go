// Package relay forwards a deliberately bounded, text-and-function-tool subset of
// the native OpenAI and Anthropic protocols. Each request has at most one network
// attempt: a failed or interrupted send is never replayed and charged twice.
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
	Settle(context.Context, string, domain.Settlement) error
	SaveResource(context.Context, domain.Resource) error
	Resource(context.Context, string, string, string) (domain.Resource, error)
}
type Handler struct {
	repo         Repository
	limiter      routeLimiter
	allowPrivate bool
}

func New(repo Repository, client *redis.Client, allowPrivate bool) *Handler {
	return &Handler{repo: repo, limiter: &redisLimiter{client}, allowPrivate: allowPrivate}
}

type candidate struct {
	credential domain.Credential
	endpoint   domain.Endpoint
	proxyURL   string
	buckets    []limits
	score      float64
	balance    int64
}
type plan struct {
	key                       domain.AccessKey
	model                     domain.Model
	provider, path, requestID string
	body                      []byte
	anthropicBeta             string
	input, output             int64
	stream, count, resource   bool
	binding                   *domain.Resource
}

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
	if isCreate && r.Method == http.MethodPost {
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
	if !p.count && !p.resource {
		reserved, costReserve, err = estimate(p.input, p.output, p.model)
		if err != nil {
			fail(w, 400, "invalid_price", "Unable to reserve this request")
			return
		}
	}
	var chosen *candidate
	var capacityErr error
	var untrackedBudget bool
	for i := range candidates {
		c := &candidates[i]
		// Cost is a hard eligibility condition, even when load is the ranking
		// policy. Avoid consuming rate capacity for a predictably unfunded key.
		if c.credential.BudgetMicros > 0 && c.balance < costReserve {
			continue
		}
		err = h.limiter.acquire(ctx, p.requestID, c.buckets, p.input+p.output)
		if errors.Is(err, errCapacity) {
			capacityErr = err
			continue
		}
		if err != nil {
			fail(w, 503, "limiter_unavailable", "Rate limiter unavailable")
			return
		}
		reservation := domain.Reservation{Request: domain.Request{ID: p.requestID, TenantID: key.TenantID, ProjectID: key.ProjectID, AccessKeyID: key.ID, CredentialID: c.credential.ID, Provider: p.provider, Model: p.model.ID, Status: "reserved", ReservedMicros: reserved, CreatedAt: time.Now()}, Model: p.model, CredentialID: c.credential.ID, CostReserve: costReserve}
		err = h.repo.Reserve(ctx, reservation)
		if err == nil {
			chosen = c
			break
		}
		// No upstream call has been made, and a rejected money transaction has
		// no wallet hold. Release this candidate before trying another budget.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr := h.limiter.release(cleanup, p.requestID, c.buckets)
		cancel()
		if releaseErr != nil {
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
		// Customer balance and storage failures cannot be solved by changing
		// an upstream key. Only an atomic upstream-budget race retries routing.
		fail(w, 402, "budget_unavailable", "Customer balance or upstream budget is insufficient, or its reservation is unavailable")
		return
	}
	if chosen == nil {
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
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.limiter.release(cleanup, p.requestID, chosen.buckets)
	}()
	h.forward(w, r, p, *chosen)
}

func (h *Handler) models(w http.ResponseWriter, p plan, models []domain.Model) {
	endpoints, err := h.repo.ListEndpoints(context.Background())
	if err != nil {
		fail(w, 503, "configuration_unavailable", "Model registry unavailable")
		return
	}
	items := []map[string]any{}
	for _, m := range models {
		if !m.Enabled || m.Provider != p.provider || !allowed(p.key.Models, m.ID) {
			continue
		}
		usable := false
		for _, e := range endpoints {
			if e.Enabled && e.Provider == p.provider && (allowed(e.Models, m.ID) || allowed(e.Models, m.UpstreamModel)) {
				usable = true
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
	if !validAccounting(p.model) || p.model.MaxOutputTokens <= 0 {
		return errors.New("model has no complete price and output limit configuration")
	}
	if err = validatePayload(m, p.path); err != nil {
		return err
	}
	p.input = int64(len(body))*2 + 4096
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
	}
	if !p.count {
		field := "max_tokens"
		if strings.HasSuffix(p.path, "/responses") {
			field = "max_output_tokens"
		}
		if strings.HasSuffix(p.path, "/chat/completions") {
			if _, ok := m["max_completion_tokens"]; ok {
				field = "max_completion_tokens"
				if _, both := m["max_tokens"]; both {
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
	m["model"], _ = json.Marshal(p.model.UpstreamModel)
	p.body, err = json.Marshal(m)
	return err
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
			if e.ID == c.EndpointID && e.Enabled && e.Provider == p.provider && (allowed(e.Models, p.model.ID) || allowed(e.Models, p.model.UpstreamModel)) {
				endpoint = e
				break
			}
		}
		if endpoint.ID == "" || p.binding != nil && p.binding.EndpointID != endpoint.ID {
			continue
		}
		if _, err = upstreamURL(endpoint.BaseURL, p.path, h.allowPrivate); err != nil {
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
		buckets := []limits{{"tenant:" + p.key.TenantID, config.TenantRPM, 0, config.TenantConcurrency}, {"access:" + p.key.ID, p.key.RPM, 0, 0}, {"credential:" + c.ID, c.RPM, c.TPM, c.MaxConcurrency}}
		if c.QuotaGroup != "" {
			buckets = append(buckets, groupLimits(c, credentials))
		}
		load, err := h.limiter.load(ctx, buckets)
		if err != nil {
			return nil, err
		}
		balance := int64(math.MaxInt64)
		if c.BudgetMicros > 0 {
			balance = c.BudgetMicros - c.SpentMicros - c.HeldMicros
		}
		result = append(result, candidate{c, endpoint, proxyURL, buckets, load, balance})
	}
	if len(result) == 0 && untrackedBudget {
		return nil, store.ErrUntrackedCostBudget
	}
	// Balanced strategy accounts for normalized load and the remaining budget
	// fraction; priority is a stable tie-breaker, never an override of capacity.
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
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

func (h *Handler) forward(w http.ResponseWriter, r *http.Request, p plan, c candidate) {
	started := time.Now()
	settlement := domain.Settlement{Status: "unknown"}
	defer func() {
		settlement.LatencyMS = time.Since(started).Milliseconds()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.repo.Settle(ctx, p.requestID, settlement)
	}()
	tr, err := transport(c.proxyURL, h.allowPrivate)
	if err != nil {
		settlement = domain.Settlement{Status: "not_sent", Usage: domain.Usage{Known: true}, ErrorCode: "proxy_configuration"}
		fail(w, 502, "upstream_unavailable", "Upstream proxy configuration unavailable")
		return
	}
	defer tr.CloseIdleConnections()
	target, err := upstreamURL(c.endpoint.BaseURL, p.path, h.allowPrivate)
	if err != nil {
		settlement = domain.Settlement{Status: "not_sent", Usage: domain.Usage{Known: true}, ErrorCode: "upstream_configuration"}
		fail(w, 502, "upstream_unavailable", "Upstream configuration unavailable")
		return
	}
	var sent atomic.Bool
	ctx := httptrace.WithClientTrace(r.Context(), &httptrace.ClientTrace{WroteHeaders: func() { sent.Store(true) }, WroteRequest: func(httptrace.WroteRequestInfo) { sent.Store(true) }})
	upstream, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(p.body))
	if err != nil {
		settlement = domain.Settlement{Status: "not_sent", Usage: domain.Usage{Known: true}, ErrorCode: "invalid_upstream"}
		fail(w, 502, "upstream_unavailable", "Invalid upstream request")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
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
	if c.endpoint.AuthMode == "bearer" || p.provider == "openai" && c.endpoint.AuthMode != "x-api-key" {
		upstream.Header.Set("Authorization", "Bearer "+c.credential.Secret)
	} else {
		upstream.Header.Set("X-Api-Key", c.credential.Secret)
	}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(upstream)
	if err != nil {
		settlement.Status = "failed"
		settlement.ErrorCode = "upstream_transport"
		if !sent.Load() {
			settlement.Status = "not_sent"
			settlement.Usage.Known = true
		}
		fail(w, 502, "upstream_error", "Upstream connection failed")
		return
	}
	defer response.Body.Close()
	if response.StatusCode == 429 || response.StatusCode == 401 || response.StatusCode == 403 {
		d := retryDelay(response.Header.Get("Retry-After"))
		if response.StatusCode != 429 {
			d = 5 * time.Minute
		}
		cool := []limits{{scope: "credential:" + c.credential.ID}}
		if c.credential.QuotaGroup != "" && response.StatusCode == 429 {
			cool = append(cool, limits{scope: "group:" + c.credential.QuotaGroup})
		}
		_ = h.limiter.cool(r.Context(), cool, d)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		settlement.Status = "failed"
		settlement.ErrorCode = fmt.Sprintf("upstream_%d", response.StatusCode)
		status := response.StatusCode
		if status < 400 || status == 401 || status == 403 {
			status = 502
		}
		if status == 429 {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryDelay(response.Header.Get("Retry-After")).Seconds())))
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		fail(w, status, "upstream_error", "The selected upstream rejected or failed the request")
		return
	}
	if p.stream {
		if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
			settlement.Status = "failed"
			settlement.ErrorCode = "invalid_stream"
			fail(w, 502, "upstream_error", "Expected an SSE response")
			return
		}
		h.stream(w, r, response, p, c, &settlement)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		settlement.Status = "failed"
		settlement.ErrorCode = "invalid_response"
		fail(w, 502, "upstream_error", "Unable to read bounded upstream response")
		return
	}
	obj := object(body)
	if obj == nil {
		settlement.Status = "failed"
		settlement.ErrorCode = "invalid_response"
		fail(w, 502, "upstream_error", "Expected a JSON response")
		return
	}
	if _, exists := obj["error"]; exists && string(obj["error"]) != "null" {
		settlement.Status = "failed"
		settlement.ErrorCode = "upstream_error"
		settlement.Usage, _ = parseUsage(obj["usage"], p.provider)
		h.price(&settlement, p.model)
		fail(w, 502, "upstream_error", "The selected upstream returned an error")
		return
	}
	if status := str(obj["status"]); status == "failed" || status == "cancelled" || status == "canceled" {
		settlement.Status = "failed"
		settlement.ErrorCode = "upstream_response_failed"
		settlement.Usage, _ = parseUsage(obj["usage"], p.provider)
		h.price(&settlement, p.model)
		fail(w, 502, "upstream_error", "The upstream response did not complete")
		return
	}
	if !p.resource && strings.HasSuffix(p.path, "/responses") {
		if status := str(obj["status"]); status == "queued" || status == "in_progress" {
			settlement.Status = "failed"
			settlement.ErrorCode = "background_unsupported"
			fail(w, 502, "upstream_error", "Background responses are unsupported")
			return
		}
		if err = h.saveResource(r.Context(), p, c, str(obj["id"])); err != nil {
			settlement.Status = "failed"
			settlement.ErrorCode = "resource_binding_failed"
			fail(w, 503, "storage_error", "Unable to persist response ownership")
			return
		}
	}
	if p.count || p.resource {
		settlement.Usage.Known = true
	} else {
		settlement.Usage, _ = parseUsage(obj["usage"], p.provider)
	}
	settlement.Status = "completed"
	h.price(&settlement, p.model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err = w.Write(body); err != nil {
		settlement.Status = "canceled"
		settlement.ErrorCode = "client_disconnected"
		// The upstream already returned a complete response with verified usage.
		// A failed client write does not undo that completed inference work.
	}
}

func (h *Handler) saveResource(ctx context.Context, p plan, c candidate, id string) error {
	if !validID(id) {
		return errors.New("invalid response ID")
	}
	return h.repo.SaveResource(ctx, domain.Resource{ID: id, TenantID: p.key.TenantID, ProjectID: p.key.ProjectID, CredentialID: c.credential.ID, EndpointID: c.endpoint.ID, Model: p.model.ID, InputTokens: p.input + p.output})
}
func (h *Handler) price(s *domain.Settlement, m domain.Model) {
	if m.EffectiveBillingMode() == domain.BillingUsageOnly {
		// Token evidence is independent from pricing. Unknown cost must not
		// erase verified usage or become a synthetic free monetary settlement.
		s.ChargeMicros, s.CostMicros = 0, 0
		return
	}
	if !s.Usage.Known {
		return
	}
	charge, cost, err := charges(s.Usage, m)
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

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/relay"
	"relaypine.com/gateway/internal/store"
)

// Exercise the published API contract through the native relay and real durable
// storage. The test database is isolated from any real upstream credentials.
func TestBatchImportedUsageOnlyModelsWorkWithoutWalletFunds(t *testing.T) {
	f := newAPIFixture(t)
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		addr = os.Getenv("REDIS_ADDR")
	}
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR/REDIS_ADDR required for complete gateway integration")
	}
	redisClient := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD")})
	t.Cleanup(func() { redisClient.Close() })
	if err := redisClient.Ping(f.ctx).Err(); err != nil {
		t.Fatal(err)
	}
	f.s.Redis, f.s.AllowPrivate = redisClient, true
	f.s.Relay = relay.New(isolatedE2ERepository{Store: f.db, prefix: f.prefix}, redisClient, true)
	f.h = f.s.Handler()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{"DELETE a FROM attempts a JOIN requests r ON r.id=a.request_id WHERE r.tenant_id=?", "DELETE FROM requests WHERE tenant_id=?"} {
			if _, err := f.db.DB.ExecContext(ctx, q, f.operator.TenantID); err != nil {
				t.Logf("request cleanup: %v", err)
			}
		}
	})
	priced := domain.Model{ID: f.prefix + "priced", Name: "Preserved metered model", Provider: "anthropic", UpstreamModel: "paid-model", Enabled: true, InputRate: 4_000_000, OutputRate: 20_000_000, CostInputRate: 4_000_000, CostOutputRate: 20_000_000, MaxOutputTokens: 64}
	f.models = append(f.models, priced.ID)
	w := f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, priced)
	expectStatus(t, w, 200)
	priced = decodeResponse[domain.Model](t, w)
	var pricedBefore []byte
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT config FROM models WHERE id=?", priced.ID).Scan(&pricedBefore); err != nil {
		t.Fatal(err)
	}
	models := []domain.Model{usageOnlyImportModel(f.prefix + "openai"), usageOnlyImportModel(f.prefix + "anthropic")}
	models[0].Provider = "openai"
	for i := range models {
		models[i].UpstreamModel = "mock-" + models[i].Provider
		models[i].MaxOutputTokens = 64
		f.models = append(f.models, models[i].ID)
	}
	// A conflict must not silently convert an existing priced model.
	batch := append([]domain.Model{usageOnlyImportModel(priced.ID)}, models...)
	w = f.call(t, "POST", "/api/admin/models/import", f.operatorCookie, f.s.AppURL, map[string]any{"models": batch})
	expectStatus(t, w, 200)
	result := decodeResponse[store.ModelImportResult](t, w)
	if result.CreatedCount != 2 || result.SkippedCount != 1 {
		t.Fatalf("batch not published: %+v", result)
	}
	type issued struct {
		Key    domain.AccessKey `json:"key"`
		Secret string           `json:"secret"`
	}
	w = f.call(t, "POST", "/api/keys", f.operatorCookie, f.s.AppURL, map[string]any{"name": "Usage-only integration", "rpm": 100, "models": []string{models[0].ID, models[1].ID, priced.ID}})
	expectStatus(t, w, 201)
	access := decodeResponse[issued](t, w)
	scopes := []string{"tenant:" + f.operator.TenantID, "access:" + access.Key.ID}
	t.Cleanup(func() {
		for _, scope := range scopes {
			sum := sha256.Sum256([]byte(scope))
			var cursor uint64
			for {
				keys, next, err := redisClient.Scan(context.Background(), cursor, "relay:"+hex.EncodeToString(sum[:16])+":*", 100).Result()
				if err != nil {
					t.Logf("Redis cleanup: %v", err)
					break
				}
				if len(keys) > 0 {
					redisClient.Del(context.Background(), keys...)
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
		}
	})
	for _, model := range models {
		t.Run(model.Provider, func(t *testing.T) {
			endpointID, credentialID := f.prefix+"e_"+model.Provider, f.prefix+"c_"+model.Provider
			f.endpoints, f.credentials = append(f.endpoints, endpointID), append(f.credentials, credentialID)
			quotaGroup := f.prefix + model.Provider
			scopes = append(scopes, "credential:"+credentialID, "group:"+quotaGroup)
			path := "/v1/chat/completions"
			if model.Provider == "anthropic" {
				path = "/v1/messages"
			}
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Method != "POST" || r.URL.Path != path {
					t.Errorf("native path changed: %s %s", r.Method, r.URL.Path)
				}
				if model.Provider == "openai" && r.Header.Get("Authorization") != "Bearer mock-secret" || model.Provider == "anthropic" && r.Header.Get("X-Api-Key") != "mock-secret" {
					t.Error("upstream authentication changed")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if string(body["model"]) != fmt.Sprintf("%q", model.UpstreamModel) || string(body["max_tokens"]) != "64" {
					t.Error("imported upstream alias or output cap was not applied")
				}
				// Admission should persist the attempt without even temporary money holds.
				var held, costHeld int64
				if err := f.db.DB.QueryRowContext(f.ctx, "SELECT w.held_micros,c.held_micros FROM wallets w JOIN credentials c ON c.id=? WHERE w.tenant_id=?", credentialID, f.operator.TenantID).Scan(&held, &costHeld); err != nil || held != 0 || costHeld != 0 {
					t.Errorf("money held while forwarding: customer=%d cost=%d err=%v", held, costHeld, err)
				}
				if string(body["stream"]) == "true" {
					w.Header().Set("Content-Type", "text/event-stream")
					if model.Provider == "openai" {
						_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\ndata: [DONE]\n\n")
					} else {
						_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0,\"cache_read_input_tokens\":2}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					}
				} else {
					w.Header().Set("Content-Type", "application/json")
					if model.Provider == "openai" {
						_, _ = io.WriteString(w, `{"id":"chat_usage","usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":2}}}`)
					} else {
						_, _ = io.WriteString(w, `{"id":"msg_usage","usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":2}}`)
					}
				}
			}))
			t.Cleanup(upstream.Close)
			expectStatus(t, f.call(t, "POST", "/api/admin/endpoints", f.operatorCookie, f.s.AppURL, domain.Endpoint{ID: endpointID, Name: "Usage-only fixture", Provider: model.Provider, BaseURL: upstream.URL, Enabled: true}), 200)
			expectStatus(t, f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AppURL, map[string]any{"id": credentialID, "name": "Usage-only fixture", "endpoint_id": endpointID, "secret": "mock-secret", "quota_group": quotaGroup, "enabled": true, "rpm": 100, "tpm": 100000, "max_concurrency": 5, "budget_micros": 0}), 200)
			for _, stream := range []bool{false, true} {
				body, _ := json.Marshal(map[string]any{"model": model.ID, "messages": []any{map[string]string{"role": "user", "content": "hello"}}, "stream": stream})
				r := httptest.NewRequest("POST", "/"+model.Provider+path, bytes.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if model.Provider == "anthropic" {
					r.Header.Set("X-Api-Key", access.Secret)
					r.Header.Set("Anthropic-Version", "2023-06-01")
				} else {
					r.Header.Set("Authorization", "Bearer "+access.Secret)
				}
				response := httptest.NewRecorder()
				f.h.ServeHTTP(response, r)
				expectStatus(t, response, 200)
				if stream && !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
					t.Fatal("native stream was not returned")
				}
				id := response.Header().Get("X-Request-Id")
				var phase string
				var held, costHeld bool
				var reserved, charge, cost int64
				if err := f.db.DB.QueryRowContext(f.ctx, "SELECT phase,customer_hold_active,cost_hold_active,reserved_micros,charge_micros,cost_micros FROM requests WHERE id=?", id).Scan(&phase, &held, &costHeld, &reserved, &charge, &cost); err != nil {
					t.Fatal(err)
				}
				if phase != "settled" || held || costHeld || reserved != 0 || charge != 0 || cost != 0 {
					t.Fatalf("request was not durably settled without money: phase=%s holds=%t/%t reserved=%d charge=%d cost=%d", phase, held, costHeld, reserved, charge, cost)
				}
				state := f.call(t, "GET", "/api/state", f.operatorCookie, "", nil)
				expectStatus(t, state, 200)
				found := false
				for _, request := range decodeResponse[domain.Snapshot](t, state).Requests {
					if request.ID != id {
						continue
					}
					found = true
					if request.Status != "completed" || request.BillingMode != domain.BillingUsageOnly || request.CostStatus != "untracked" || request.Usage != (domain.Usage{InputTokens: 10, OutputTokens: 3, CachedTokens: 2, Known: true}) {
						t.Fatalf("usage/cost evidence missing from console API: %+v", request)
					}
				}
				if !found {
					t.Fatal("completed imported-model request missing from console API")
				}
			}
			var spent, held int64
			if err := f.db.DB.QueryRowContext(f.ctx, "SELECT spent_micros,held_micros FROM credentials WHERE id=?", credentialID).Scan(&spent, &held); err != nil || spent != 0 || held != 0 || hits.Load() != 2 {
				t.Fatalf("credential money/calls changed: spent=%d held=%d calls=%d err=%v", spent, held, hits.Load(), err)
			}
		})
	}
	var balance, held, ledger int64
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros,held_micros FROM wallets WHERE tenant_id=?", f.operator.TenantID).Scan(&balance, &held); err != nil {
		t.Fatal(err)
	}
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM ledger_entries WHERE tenant_id=?", f.operator.TenantID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if balance != 0 || held != 0 || ledger != 0 {
		t.Fatalf("usage-only import required funds or created ledger entries: balance=%d held=%d ledger=%d", balance, held, ledger)
	}
	var pricedAfter []byte
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT config FROM models WHERE id=?", priced.ID).Scan(&pricedAfter); err != nil || !bytes.Equal(pricedBefore, pricedAfter) {
		t.Fatal("batch import or usage-only requests changed existing metered configuration")
	}
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/pricing"
	"relaypine.com/gateway/internal/relay"
)

func referencePriceFixture(t *testing.T) *apiFixture {
	t.Helper()
	f := newAPIFixture(t)
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		addr = os.Getenv("REDIS_ADDR")
	}
	if addr == "" {
		t.Skip("isolated Redis required")
	}
	rc := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD")})
	if err := rc.Ping(f.ctx).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rc.Close() })
	f.s.Redis = rc
	f.s.PricingFetcher = func(context.Context, []string) pricing.Catalog {
		now := time.Now().UTC()
		return pricing.Catalog{Sources: []pricing.Source{{Provider: "anthropic", URL: pricing.AnthropicURL, CheckedAt: now}}, Rates: map[string]pricing.Rate{"anthropic:claude-opus-5-5": {Input: 4_000_000, Output: 20_000_000, Cached: 200_000, CacheWrite: 5_000_000, Reference: domain.PriceReference{Provider: "anthropic", Model: "claude-opus-5-5", SourceURL: pricing.AnthropicURL, SourceName: "Anthropic official reference", CheckedAt: now, Hash: "test-source", Scope: "standard_text", MaxInputTokens: 1000000}}}}
	}
	f.h = f.s.Handler()
	return f
}
func pricePreview(t *testing.T, f *apiFixture, body any) pricing.Preview {
	t.Helper()
	w := f.call(t, "POST", "/api/admin/models/prices/preview", f.operatorCookie, f.s.AdminAppURL, body)
	expectStatus(t, w, 200)
	p := decodeResponse[pricing.Preview](t, w)
	t.Cleanup(func() { f.s.Redis.Del(context.Background(), "rp:price-preview:"+p.ID) })
	return p
}
func legacyUnpricedModel(t *testing.T, f *apiFixture, suffix string) domain.Model {
	t.Helper()
	m := domain.Model{ID: f.prefix + suffix, Name: "Legacy unpriced model", Provider: "anthropic", UpstreamModel: "claude-opus-5-5", Enabled: true, BillingMode: domain.BillingUsageOnly, MaxOutputTokens: 64}
	f.models = append(f.models, m.ID)
	saved, err := f.db.CreateModel(f.ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestReferencePriceBackfillChargesWalletAndRejectsUnfundedCalls(t *testing.T) {
	f := referencePriceFixture(t)
	customer, customerCookie := f.verifiedCustomer(t, "meteredcustomer")
	m := legacyUnpricedModel(t, f, "priced")
	p := pricePreview(t, f, map[string]any{"model_ids": []string{m.ID}})
	if len(p.Items) != 1 || p.Items[0].Status != "matched" || p.Items[0].After.BillingMode != domain.BillingMetered {
		t.Fatal("legacy reference not matched")
	}
	expectStatus(t, f.call(t, "POST", "/api/admin/models/prices/apply", f.operatorCookie, f.s.AdminAppURL, map[string]any{"preview_id": p.ID, "model_ids": []string{m.ID}}), 200)
	var balance, held int64
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros,held_micros FROM wallets WHERE tenant_id=?", customer.TenantID).Scan(&balance, &held); err != nil || balance != 0 || held != 0 {
		t.Fatal("backfill changed balance", err)
	}
	// An explicit same-price manual edit permits our isolated compatible mock;
	// automatic reference routing itself never contacts a compatible host.
	m = p.Items[0].After
	w := f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AdminAppURL, struct {
		domain.Model
		Manual bool `json:"manual_pricing"`
	}{m, true})
	expectStatus(t, w, 200)
	m = decodeResponse[domain.Model](t, w)
	if m.PricingReference != nil {
		t.Fatal("explicit identical manual rates did not clear reference routing policy")
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"id":"msg_test","type":"message","content":[],"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}`)
	}))
	defer upstream.Close()
	endpointID, credentialID := f.prefix+"endpoint", f.prefix+"credential"
	f.endpoints = append(f.endpoints, endpointID)
	f.credentials = append(f.credentials, credentialID)
	_, err := f.db.SaveEndpoint(f.ctx, domain.Endpoint{ID: endpointID, Name: "Isolated mock", Provider: "anthropic", BaseURL: upstream.URL, Enabled: true, Models: []string{m.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.db.SaveCredential(f.ctx, domain.Credential{ID: credentialID, EndpointID: endpointID, Name: "Mock key", Secret: "mock-test-secret", Enabled: true, RPM: 100, TPM: 1000000, MaxConcurrency: 10})
	if err != nil {
		t.Fatal(err)
	}
	issuedResponse := f.call(t, "POST", "/api/keys", customerCookie, f.s.AppURL, map[string]any{"name": "Metered test", "rpm": 100, "models": []string{m.ID}})
	expectStatus(t, issuedResponse, 201)
	issued := decodeResponse[struct {
		Key    domain.AccessKey `json:"key"`
		Secret string           `json:"secret"`
	}](t, issuedResponse)
	key, secret := issued.Key, issued.Secret
	f.s.Relay = relay.New(isolatedE2ERepository{Store: f.db, prefix: f.prefix}, f.s.Redis, true)
	f.h = f.s.Handler()
	t.Cleanup(func() {
		for _, scope := range []string{"tenant:" + customer.TenantID, "access:" + key.ID, "credential:" + credentialID} {
			sum := sha256.Sum256([]byte(scope))
			prefix := "relay:" + hex.EncodeToString(sum[:16]) + ":"
			var cursor uint64
			for {
				keys, next, e := f.s.Redis.Scan(context.Background(), cursor, prefix+"*", 100).Result()
				if e != nil {
					break
				}
				if len(keys) > 0 {
					f.s.Redis.Del(context.Background(), keys...)
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
		}
	})
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"model":"`+m.ID+`","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`))
		r.Header.Set("X-Api-Key", secret)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		return w
	}
	expectStatus(t, call(), 402)
	if calls.Load() != 0 {
		t.Fatal("empty wallet reached upstream")
	}
	if err = f.db.Credit(f.ctx, customer.TenantID, 1_000_000, f.prefix+"credit", "isolated test credit"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, call(), 200)
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros,held_micros FROM wallets WHERE tenant_id=?", customer.TenantID).Scan(&balance, &held); err != nil || balance != 999864 || held != 0 || calls.Load() != 1 {
		t.Fatalf("reference-rate charge mismatch balance=%d held=%d calls=%d err=%v", balance, held, calls.Load(), err)
	}
	var charge, cost int64
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT charge_micros,cost_micros FROM requests WHERE tenant_id=? AND status='completed'", customer.TenantID).Scan(&charge, &cost); err != nil || charge != 136 || cost != 136 {
		t.Fatal("durable charge/cost missing", charge, cost, err)
	}
}

func TestReferencePricePreviewIsAtomicVersionedAndUnforgeable(t *testing.T) {
	f := referencePriceFixture(t)
	a, b := legacyUnpricedModel(t, f, "a"), legacyUnpricedModel(t, f, "b")
	p := pricePreview(t, f, map[string]any{"model_ids": []string{a.ID, b.ID}})
	b.Name = "Concurrent edit"
	if _, err := f.db.SaveModel(f.ctx, b); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.call(t, "POST", "/api/admin/models/prices/apply", f.operatorCookie, f.s.AdminAppURL, map[string]any{"preview_id": p.ID, "model_ids": []string{a.ID, b.ID}}), 409)
	var raw []byte
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT config FROM models WHERE id=?", a.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var unchanged domain.Model
	_ = json.Unmarshal(raw, &unchanged)
	if unchanged.Version != a.Version || unchanged.BillingMode != domain.BillingUsageOnly {
		t.Fatal("stale batch partially updated")
	}
	expectStatus(t, f.call(t, "POST", "/api/admin/models/prices/apply", f.operatorCookie, f.s.AdminAppURL, map[string]any{"preview_id": strings.Repeat("a", 48), "model_ids": []string{a.ID}}), 409)
	expectStatus(t, f.call(t, "POST", "/api/admin/models/prices/apply", f.operatorCookie, f.s.AdminAppURL, map[string]any{"preview_id": p.ID, "model_ids": []string{a.ID, a.ID}}), 409)
	a.Enabled = true
	expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AdminAppURL, a), 400)
}

func TestReferenceImportUsesServerPricesAndUnknownModelsStayDisabled(t *testing.T) {
	f := referencePriceFixture(t)
	endpointID, credentialID := f.prefix+"official", f.prefix+"cred"
	f.endpoints = append(f.endpoints, endpointID)
	f.credentials = append(f.credentials, credentialID)
	if _, err := f.db.SaveEndpoint(f.ctx, domain.Endpoint{ID: endpointID, Name: "Official", Provider: "anthropic", BaseURL: "https://api.anthropic.com", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SaveCredential(f.ctx, domain.Credential{ID: credentialID, EndpointID: endpointID, Name: "Stored mock", Secret: "never-sent-to-public-price-source", Enabled: true, RPM: 50, MaxConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	known := domain.Model{ID: f.prefix + "known", Name: "Known", Provider: "anthropic", UpstreamModel: "claude-opus-5-5", Enabled: true, BillingMode: domain.BillingUsageOnly, InputRate: 1, CostInputRate: 1, MaxOutputTokens: 64, PricingReference: &domain.PriceReference{SourceURL: "https://malicious.invalid"}}
	unknown := known
	unknown.ID = f.prefix + "unknown"
	unknown.UpstreamModel = "unknown-future-model"
	f.models = append(f.models, known.ID, unknown.ID)
	p := pricePreview(t, f, map[string]any{"credential_id": credentialID, "models": []domain.Model{known, unknown}})
	if p.Kind != "import" || p.Items[0].After.InputRate != 4_000_000 || p.Items[0].After.PricingReference.SourceURL != pricing.AnthropicURL || p.Items[1].After.Enabled {
		t.Fatal("client price claims trusted or unknown model enabled")
	}
	payload := map[string]any{"preview_id": p.ID, "model_ids": []string{known.ID, unknown.ID}}
	expectStatus(t, f.call(t, "POST", "/api/admin/models/import", f.operatorCookie, f.s.AdminAppURL, payload), 200)
	w := f.call(t, "POST", "/api/admin/models/import", f.operatorCookie, f.s.AdminAppURL, payload)
	expectStatus(t, w, 200)
	var receipt struct {
		Created int `json:"created_count"`
		Skipped int `json:"skipped_count"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &receipt)
	if receipt.Created != 0 || receipt.Skipped != 2 {
		t.Fatal("safe preview import retry changed models")
	}
	f.s.PricingFetcher = func(context.Context, []string) pricing.Catalog {
		return pricing.Catalog{Rates: map[string]pricing.Rate{}, Sources: []pricing.Source{{Provider: "anthropic", URL: pricing.AnthropicURL, Error: "Official pricing could not be verified. Existing prices were not changed."}}}
	}
	expectStatus(t, f.call(t, "POST", "/api/admin/models/prices/preview", f.operatorCookie, f.s.AdminAppURL, map[string]any{"model_ids": []string{known.ID}}), 503)
	var raw []byte
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT config FROM models WHERE id=?", known.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved domain.Model
	_ = json.Unmarshal(raw, &saved)
	if !saved.Enabled || saved.InputRate != 4_000_000 || saved.Version != 1 {
		t.Fatal("source outage changed prices")
	}
}

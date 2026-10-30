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

// Official prices with a long context tier are applied, a prompt above the
// threshold settles at the long rates and one below it at the standard rates,
// the wallet moves by exactly each charge, and customers see the long context
// sales rates without the upstream cost.
func TestLongContextPricesEndToEnd(t *testing.T) {
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
	f.s.Redis = redisClient
	f.s.AllowPrivate = true
	f.s.PricingFetcher = func(context.Context, []string) pricing.Catalog {
		now := time.Now().UTC()
		ref := func(id string, all bool) domain.PriceReference {
			return domain.PriceReference{Provider: "openai", Model: id, SourceURL: pricing.OpenAIURL, SourceName: "OpenAI official reference", CheckedAt: now, Hash: "test-source", Scope: "standard_text", MaxInputTokens: 272000, AllPromptLengths: all}
		}
		return pricing.Catalog{Sources: []pricing.Source{{Provider: "openai", URL: pricing.OpenAIURL, CheckedAt: now}}, Rates: map[string]pricing.Rate{
			"openai:gpt-5.5": {Input: 5_000_000, Output: 30_000_000, Cached: 500_000, Long: &pricing.LongRate{Input: 10_000_000, Output: 45_000_000, Cached: 1_000_000}, Reference: ref("gpt-5.5", false)},
			"openai:gpt-5.2": {Input: 1_750_000, Output: 14_000_000, Cached: 175_000, Reference: ref("gpt-5.2", true)},
		}}
	}
	f.s.Relay = relay.New(isolatedE2ERepository{Store: f.db, prefix: f.prefix}, redisClient, true, true)
	f.h = f.s.Handler()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.db.DB.ExecContext(ctx, "DELETE a FROM attempts a JOIN requests r ON r.id=a.request_id WHERE r.tenant_id=?", f.operator.TenantID)
		_, _ = f.db.DB.ExecContext(ctx, "DELETE FROM request_payloads WHERE request_id IN (SELECT id FROM requests WHERE tenant_id=?)", f.operator.TenantID)
		_, _ = f.db.DB.ExecContext(ctx, "DELETE FROM requests WHERE tenant_id=?", f.operator.TenantID)
	})
	admin := f.s.AdminAppURL
	tiered, uniform := f.prefix+"m_long", f.prefix+"m_uniform"
	official, mock, credential := f.prefix+"e_official", f.prefix+"e_mock", f.prefix+"c_mock"
	f.models = append(f.models, tiered, uniform)
	f.endpoints = append(f.endpoints, official, mock)
	f.credentials = append(f.credentials, credential)
	// Reference prices need an enabled official upstream. It has no key, so
	// no request is ever sent to it.
	if _, err := f.db.SaveEndpoint(f.ctx, domain.Endpoint{ID: official, Name: "Official", Provider: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true, Models: []string{tiered, uniform}}); err != nil {
		t.Fatal(err)
	}
	for id, upstream := range map[string]string{tiered: "gpt-5.5", uniform: "gpt-5.2"} {
		if _, err := f.db.CreateModel(f.ctx, domain.Model{ID: id, Name: upstream, Provider: "openai", UpstreamModel: upstream, Enabled: true, BillingMode: domain.BillingUsageOnly, MaxOutputTokens: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	p := pricePreview(t, f, map[string]any{"model_ids": []string{tiered, uniform}})
	long := &domain.LongContext{AboveInputTokens: 272000, InputRate: 10_000_000, OutputRate: 45_000_000, CachedRate: 1_000_000,
		CostInputRate: 10_000_000, CostOutputRate: 45_000_000, CostCachedRate: 1_000_000}
	for _, item := range p.Items {
		ref := item.After.PricingReference
		want := (*domain.LongContext)(nil)
		if item.ID == tiered {
			want = long
		}
		if item.Status != "matched" || ref == nil || ref.AllPromptLengths != (item.ID == uniform) || (want == nil) != (item.After.LongContext == nil) || want != nil && *item.After.LongContext != *want {
			t.Fatalf("preview of %s: %+v %+v", item.ID, item.After.LongContext, ref)
		}
	}
	expectStatus(t, f.call(t, "POST", "/api/admin/models/prices/apply", f.operatorCookie, admin, map[string]any{"preview_id": p.ID, "model_ids": []string{tiered, uniform}}), 200)

	// Customers see the long context sales rates, never the upstream cost.
	_, customerCookie := f.verifiedCustomer(t, "longcustomer")
	state := decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", customerCookie, f.s.AppURL, nil))
	seen := false
	for _, m := range state.Models {
		if m.ID == tiered {
			seen = true
			if l := m.LongContext; l == nil || l.InputRate != 10_000_000 || l.OutputRate != 45_000_000 || l.CostInputRate != 0 || l.CostOutputRate != 0 || l.CostCachedRate != 0 {
				t.Fatalf("customer view of long context prices %+v", l)
			}
		}
	}
	if !seen {
		t.Fatal("customer cannot see the long context model")
	}

	// Reference prices pin routing to the official host, so the test keeps
	// the same rates as a manual price and routes it to a local mock.
	var saved domain.Model
	for _, item := range p.Items {
		if item.ID == tiered {
			saved = item.After
		}
	}
	saved.Version = 0
	edited := f.call(t, "POST", "/api/admin/models", f.operatorCookie, admin, struct {
		domain.Model
		Manual bool `json:"manual_pricing"`
	}{saved, true})
	expectStatus(t, edited, 200)
	if m := decodeResponse[domain.Model](t, edited); m.PricingReference != nil || m.LongContext == nil || *m.LongContext != *long {
		t.Fatalf("manual edit lost the long context price: %+v", m.LongContext)
	}
	var prompt atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_long","object":"response","status":"completed","output":[],"usage":{"input_tokens":`+jsonInt(prompt.Load())+`,"input_tokens_details":{"cached_tokens":80000},"output_tokens":1000,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":0}}`)
	}))
	t.Cleanup(upstream.Close)
	expectStatus(t, f.call(t, "POST", "/api/admin/endpoints", f.operatorCookie, admin, domain.Endpoint{ID: mock, Name: "Long context mock", Provider: "openai", BaseURL: upstream.URL + "/v1", Enabled: true, Models: []string{tiered}}), 200)
	expectStatus(t, f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, admin, map[string]any{"id": credential, "name": "Long context mock", "endpoint_id": mock, "secret": "mock-long-" + testID(), "quota_group": f.prefix + "long", "enabled": true, "rpm": 100, "tpm": 1000000, "max_concurrency": 10, "budget_micros": 100_000_000}), 200)
	issued := f.call(t, "POST", "/api/keys", f.operatorCookie, admin, map[string]any{"name": "Long context", "rpm": 100, "models": []string{tiered}})
	expectStatus(t, issued, 201)
	access := decodeResponse[struct {
		Key    domain.AccessKey `json:"key"`
		Secret string           `json:"secret"`
	}](t, issued)
	t.Cleanup(func() {
		for _, scope := range []string{"tenant:" + f.operator.TenantID, "access:" + access.Key.ID, "credential:" + credential, "group:" + f.prefix + "long"} {
			sum := sha256.Sum256([]byte(scope))
			keys, _ := redisClient.Keys(context.Background(), "relay:"+hex.EncodeToString(sum[:16])+":*").Result()
			if len(keys) > 0 {
				redisClient.Del(context.Background(), keys...)
			}
		}
	})
	expectStatus(t, f.call(t, "POST", "/api/admin/credits", f.operatorCookie, admin, map[string]any{"tenant_id": f.operator.TenantID, "amount_micros": 10_000_000, "reason": "Long context E2E credit", "idempotency_key": f.prefix + "long-funds"}), 200)
	gateway := httptest.NewServer(f.h)
	t.Cleanup(gateway.Close)
	client := gateway.Client()
	client.Timeout = 10 * time.Second
	// A 150 KB prompt bounds its input above 272K tokens, so its hold is taken
	// at the long context rates.
	body := `{"model":"` + tiered + `","input":"` + strings.Repeat("a", 150_000) + `","store":false,"max_output_tokens":1000}`
	for _, tc := range []struct {
		prompt      int64
		charge      int64
		longContext bool
		description string
	}{
		// 200,000 input at $10, 80,000 cached at $1 and 1,000 output at $45 per million.
		{280_000, 2_125_000, true, "above the threshold"},
		// 192,000 input at $5, 80,000 cached at $0.50 and 1,000 output at $30 per million.
		{272_000, 1_030_000, false, "at the threshold"},
	} {
		prompt.Store(tc.prompt)
		var before int64
		if err := f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros FROM wallets WHERE tenant_id=?", f.operator.TenantID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest("POST", gateway.URL+"/openai/v1/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+access.Secret)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		reply, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("%s: HTTP %d %s", tc.description, response.StatusCode, reply)
		}
		requestID := response.Header.Get("X-Request-Id")
		var charge, cost, reserved int64
		var usageRaw []byte
		if err := f.db.DB.QueryRowContext(f.ctx, "SELECT charge_micros,cost_micros,reserved_micros,usage_json FROM requests WHERE id=?", requestID).Scan(&charge, &cost, &reserved, &usageRaw); err != nil {
			t.Fatal(err)
		}
		var usage domain.Usage
		_ = json.Unmarshal(usageRaw, &usage)
		if charge != tc.charge || cost != tc.charge || usage.PromptTokens() != tc.prompt || reserved < 3_000_000 {
			t.Fatalf("%s: charge %d cost %d hold %d usage %+v", tc.description, charge, cost, reserved, usage)
		}
		var after int64
		if err := f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros FROM wallets WHERE tenant_id=?", f.operator.TenantID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before-after != charge {
			t.Fatalf("%s: wallet moved %d, charge %d", tc.description, before-after, charge)
		}
		found := false
		for _, r := range decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", f.operatorCookie, admin, nil)).Requests {
			if r.ID == requestID {
				found = true
				if r.LongContext != tc.longContext {
					t.Fatalf("%s: request marked long context %v", tc.description, r.LongContext)
				}
			}
		}
		if !found {
			t.Fatalf("%s: request %s missing from the operator view", tc.description, requestID)
		}
	}
}

// Long context prices are checked like the standard ones: none below the
// standard rate, none on usage-only models, and image models drop them.
func TestLongContextPriceValidation(t *testing.T) {
	f := newAPIFixture(t)
	admin := f.s.AdminAppURL
	id := f.prefix + "m_check"
	f.models = append(f.models, id)
	base := domain.Model{ID: id, Name: "Long context check", Provider: "openai", UpstreamModel: "gpt-5.5", Enabled: true, MaxOutputTokens: 1000,
		InputRate: 5_000_000, OutputRate: 30_000_000, CachedRate: 500_000, CostInputRate: 5_000_000, CostOutputRate: 30_000_000, CostCachedRate: 500_000,
		LongContext: &domain.LongContext{AboveInputTokens: 272000, InputRate: 10_000_000, OutputRate: 45_000_000, CachedRate: 1_000_000, CostInputRate: 10_000_000, CostOutputRate: 45_000_000, CostCachedRate: 1_000_000}}
	for _, change := range []func(*domain.Model){
		func(m *domain.Model) { m.LongContext.CachedRate = 400_000 },
		func(m *domain.Model) { m.LongContext.AboveInputTokens = 0 },
		func(m *domain.Model) { m.LongContext.CostOutputRate = 0 },
		func(m *domain.Model) {
			m.BillingMode, m.Enabled = domain.BillingUsageOnly, false
			m.InputRate, m.OutputRate, m.CachedRate, m.CostInputRate, m.CostOutputRate, m.CostCachedRate = 0, 0, 0, 0, 0, 0
		},
	} {
		m := base
		l := *base.LongContext
		m.LongContext = &l
		change(&m)
		expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, admin, m), 400)
	}
	w := f.call(t, "POST", "/api/admin/models", f.operatorCookie, admin, base)
	expectStatus(t, w, 200)
	if m := decodeResponse[domain.Model](t, w); m.LongContext == nil || *m.LongContext != *base.LongContext {
		t.Fatalf("saved long context price %+v", m.LongContext)
	}
	image := base
	image.ID, image.UpstreamModel, image.OutputRate, image.CostOutputRate = f.prefix+"m_image_check", "gpt-image-2", 0, 0
	image.ImageInputRate, image.ImageOutputRate, image.CostImageInputRate, image.CostImageOutputRate = 8_000_000, 30_000_000, 8_000_000, 30_000_000
	f.models = append(f.models, image.ID)
	w = f.call(t, "POST", "/api/admin/models", f.operatorCookie, admin, image)
	expectStatus(t, w, 200)
	if m := decodeResponse[domain.Model](t, w); m.LongContext != nil || !m.IsImage() {
		t.Fatalf("image model kept a long context price %+v", m.LongContext)
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

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
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/relay"
)

// An image model is published with image rates, a customer generates an image
// through the gateway, and MySQL records the image tokens and exact charge.
func TestImageGenerationEndToEndWithDurableBilling(t *testing.T) {
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
	f.s.Relay = relay.New(isolatedE2ERepository{Store: f.db, prefix: f.prefix}, redisClient, true, true)
	f.h = f.s.Handler()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.db.DB.ExecContext(ctx, "DELETE a FROM attempts a JOIN requests r ON r.id=a.request_id WHERE r.tenant_id=?", f.operator.TenantID)
		_, _ = f.db.DB.ExecContext(ctx, "DELETE FROM request_payloads WHERE request_id IN (SELECT id FROM requests WHERE tenant_id=?)", f.operator.TenantID)
		_, _ = f.db.DB.ExecContext(ctx, "DELETE FROM requests WHERE tenant_id=?", f.operator.TenantID)
	})
	const reply = `{"created":1730000000,"background":"opaque","data":[{"b64_json":"iVBORw0KGgo="}],"output_format":"png","quality":"low","size":"1024x1024","usage":{"input_tokens":25,"input_tokens_details":{"text_tokens":25,"image_tokens":0},"output_tokens":196,"total_tokens":221}}`
	var sent map[string]json.RawMessage
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/custom/v1/images/generations" {
			t.Errorf("upstream path %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(upstream.Close)
	model, endpoint, credential := f.prefix+"m_image", f.prefix+"e_image", f.prefix+"c_image"
	f.models, f.endpoints, f.credentials = append(f.models, model), append(f.endpoints, endpoint), append(f.credentials, credential)
	admin := f.s.AdminAppURL
	expectStatus(t, f.call(t, "POST", "/api/admin/endpoints", f.operatorCookie, admin, domain.Endpoint{ID: endpoint, Name: "Image E2E", Provider: "openai", BaseURL: upstream.URL + "/custom/v1", Enabled: true, Models: []string{model}}), 200)
	// The kind is derived from the upstream model, never taken from the client.
	saved := f.call(t, "POST", "/api/admin/models", f.operatorCookie, admin, domain.Model{ID: model, Name: "GPT Image 2", Provider: "openai", UpstreamModel: "gpt-image-2", Enabled: true, MaxOutputTokens: 1,
		InputRate: 5_000_000, CachedRate: 1_250_000, ImageInputRate: 8_000_000, ImageOutputRate: 30_000_000,
		CostInputRate: 4_000_000, CostCachedRate: 1_000_000, CostImageInputRate: 6_000_000, CostImageOutputRate: 24_000_000})
	expectStatus(t, saved, 200)
	if m := decodeResponse[domain.Model](t, saved); !m.IsImage() {
		t.Fatalf("published model kind %q", m.Kind)
	}
	expectStatus(t, f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, admin, map[string]any{"id": credential, "name": "Image E2E", "endpoint_id": endpoint, "secret": "mock-image-" + testID(), "quota_group": f.prefix + "image", "enabled": true, "rpm": 100, "tpm": 1000000, "max_concurrency": 10, "budget_micros": 10_000_000}), 200)
	issued := f.call(t, "POST", "/api/keys", f.operatorCookie, admin, map[string]any{"name": "Image integration", "rpm": 100, "models": []string{model}})
	expectStatus(t, issued, 201)
	access := decodeResponse[struct {
		Key    domain.AccessKey `json:"key"`
		Secret string           `json:"secret"`
	}](t, issued)
	t.Cleanup(func() {
		for _, scope := range []string{"tenant:" + f.operator.TenantID, "access:" + access.Key.ID, "credential:" + credential, "group:" + f.prefix + "image"} {
			sum := sha256.Sum256([]byte(scope))
			keys, _ := redisClient.Keys(context.Background(), "relay:"+hex.EncodeToString(sum[:16])+":*").Result()
			if len(keys) > 0 {
				redisClient.Del(context.Background(), keys...)
			}
		}
	})
	expectStatus(t, f.call(t, "POST", "/api/admin/credits", f.operatorCookie, admin, map[string]any{"tenant_id": f.operator.TenantID, "amount_micros": 5_000_000, "reason": "Image E2E credit", "idempotency_key": f.prefix + "image-funds"}), 200)
	var before int64
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros FROM wallets WHERE tenant_id=?", f.operator.TenantID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(f.h)
	t.Cleanup(gateway.Close)
	req, _ := http.NewRequest("POST", gateway.URL+"/openai/v1/images/generations", strings.NewReader(`{"model":"`+model+`","prompt":"a lighthouse at dusk","size":"1024x1024","quality":"low"}`))
	req.Header.Set("Authorization", "Bearer "+access.Secret)
	req.Header.Set("Content-Type", "application/json")
	client := gateway.Client()
	client.Timeout = 10 * time.Second
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || string(body) != reply {
		t.Fatalf("HTTP %d %s", response.StatusCode, body)
	}
	if string(sent["model"]) != `"gpt-image-2"` {
		t.Fatalf("upstream model %s", sent["model"])
	}
	requestID := response.Header.Get("X-Request-Id")
	var status string
	var charge, cost int64
	var usageRaw []byte
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT status,charge_micros,cost_micros,usage_json FROM requests WHERE id=?", requestID).Scan(&status, &charge, &cost, &usageRaw); err != nil {
		t.Fatal(err)
	}
	var usage domain.Usage
	_ = json.Unmarshal(usageRaw, &usage)
	// 25 text input tokens and 196 image output tokens: $5 and $30 per million
	// for sales, $4 and $24 for upstream cost, each rounded up to a micro-USD.
	if status != "completed" || charge != 6_005 || cost != 4_804 || usage != (domain.Usage{InputTokens: 25, ImageOutputTokens: 196, Known: true}) {
		t.Fatalf("request %s charge %d cost %d usage %+v", status, charge, cost, usage)
	}
	var after int64
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros FROM wallets WHERE tenant_id=?", f.operator.TenantID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before-after != charge {
		t.Fatalf("wallet moved %d, charge %d", before-after, charge)
	}
	// Customers see sales rates, never the upstream image cost.
	state := decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", f.customerCookie, f.s.AppURL, nil))
	for _, m := range state.Models {
		if m.ID == model && (m.CostImageInputRate != 0 || m.CostImageOutputRate != 0 || m.ImageOutputRate != 30_000_000) {
			t.Fatalf("customer view of the image model %+v", m)
		}
	}
}

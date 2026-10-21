package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

type testPayments struct {
	enabled                    bool
	checkoutCount, refundCount int
	checkoutTenant             string
	checkoutAmount             int64
}

func (p *testPayments) Enabled() bool { return p.enabled }
func (p *testPayments) Checkout(_ context.Context, u domain.User, amount int64) (string, error) {
	p.checkoutCount++
	p.checkoutTenant = u.TenantID
	p.checkoutAmount = amount
	return "https://checkout.stripe.test/session", nil
}
func (p *testPayments) Refund(_ context.Context, _, _ string) (string, error) {
	p.refundCount++
	return "pending", nil
}
func (p *testPayments) Webhook(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

type apiFixture struct {
	s                                           *Server
	h                                           http.Handler
	db                                          *store.Store
	ctx                                         context.Context
	operator, customer, other                   domain.User
	operatorCookie, customerCookie, otherCookie *http.Cookie
	payments                                    *testPayments
	prefix, remote                              string
	users                                       []domain.User
	endpoints, credentials, proxies, models     []string
}

func testID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN not set; API MySQL integration test skipped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	master := os.Getenv("MASTER_KEY")
	if master == "" {
		master = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	db, err := store.Open(ctx, dsn, master)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &apiFixture{db: db, ctx: ctx, prefix: "apit_" + testID(), payments: &testPayments{enabled: true}, remote: "192.0.2.25:10001"}
	f.s = &Server{Store: db, Relay: http.NotFoundHandler(), Payments: f.payments, AppURL: "https://relaypine.test"}
	f.h = f.s.Handler()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, u := range f.users {
			for _, q := range []string{"DELETE FROM resources WHERE tenant_id=?", "DELETE FROM ledger_entries WHERE tenant_id=?", "DELETE FROM access_keys WHERE tenant_id=?", "DELETE se FROM sessions se JOIN users u ON u.id=se.user_id WHERE u.tenant_id=?", "DELETE FROM users WHERE tenant_id=?", "DELETE FROM projects WHERE tenant_id=?", "DELETE FROM wallets WHERE tenant_id=?", "DELETE FROM tenants WHERE id=?"} {
				if _, err := db.DB.ExecContext(ctx, q, u.TenantID); err != nil {
					t.Logf("fixture cleanup: %v", err)
				}
			}
		}
		for table, ids := range map[string][]string{"credentials": f.credentials, "proxies": f.proxies, "models": f.models} {
			for _, id := range ids {
				db.DB.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", id)
			}
		}
		for _, id := range f.endpoints {
			db.DB.ExecContext(ctx, "DELETE FROM endpoints WHERE id=?", id)
		}
	})
	for i, operator := range []bool{true, false, false} {
		u, err := db.Register(ctx, f.prefix+string(rune('a'+i))+"@example.test", "API integration", "test-password-strong", operator)
		if err != nil {
			t.Fatal(err)
		}
		f.users = append(f.users, u)
		token, err := db.CreateSession(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		cookie := &http.Cookie{Name: "rp_session", Value: token}
		switch i {
		case 0:
			f.operator = u
			f.operatorCookie = cookie
		case 1:
			f.customer = u
			f.customerCookie = cookie
		case 2:
			f.other = u
			f.otherCookie = cookie
		}
	}
	return f
}
func (f *apiFixture) call(t *testing.T, method, path string, cookie *http.Cookie, origin string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		data = b
	default:
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.RemoteAddr = f.remote
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}
func expectStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP status %d, want %d: %s", w.Code, status, w.Body.String())
	}
}
func decodeResponse[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestReadRejectsOversizeAndTrailingJSON(t *testing.T) {
	for _, body := range []string{`{"name":"a"} {"name":"b"}`, `{"name":"a","unknown":1}`, `{"name":"a"}` + strings.Repeat(" ", 1<<20) + `{"name":"b"}`, `{"name":"` + strings.Repeat("a", 1<<20) + `"}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var v struct {
			Name string `json:"name"`
		}
		if err := read(r, &v); err == nil {
			t.Fatalf("invalid JSON body of %d bytes accepted", len(body))
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"valid"}`))
	r.Header.Set("Content-Type", "application/json")
	var v struct {
		Name string `json:"name"`
	}
	if err := read(r, &v); err != nil || v.Name != "valid" {
		t.Fatalf("valid JSON rejected: %v", err)
	}
}

func TestAPIOriginAndOperatorAuthorization(t *testing.T) {
	f := newAPIFixture(t)
	origin := f.s.AppURL
	tests := []struct {
		name, method, path, origin string
		cookie                     *http.Cookie
		body                       any
		want                       int
	}{
		{"anonymous state", "GET", "/api/state", "", nil, nil, 401},
		{"missing origin", "POST", "/api/keys", "", f.customerCookie, map[string]any{"name": "Blocked", "rpm": 10}, 403},
		{"foreign origin", "POST", "/api/keys", "https://attacker.test", f.customerCookie, map[string]any{"name": "Blocked", "rpm": 10}, 403},
		{"origin port mismatch", "POST", "/api/keys", origin + ":443", f.customerCookie, map[string]any{"name": "Blocked", "rpm": 10}, 403},
		{"customer admin mutation", "POST", "/api/admin/credits", origin, f.customerCookie, map[string]any{}, 403},
		{"customer admin read", "GET", "/api/admin/credits", "", f.customerCookie, nil, 403},
		{"operator admin wrong method", "GET", "/api/admin/credits", "", f.operatorCookie, nil, 405},
		{"customer refund", "POST", "/api/admin/refunds", origin, f.customerCookie, map[string]any{"order_id": "another-order"}, 403},
		{"registration role injection", "POST", "/api/auth/register", origin, nil, map[string]any{"name": "Injected", "email": f.prefix + "inject@example.test", "password": "test-password-strong", "operator": true}, 400},
		{"logout requires origin", "POST", "/api/auth/logout", "https://attacker.test", f.customerCookie, nil, 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expectStatus(t, f.call(t, tc.method, tc.path, tc.cookie, tc.origin, tc.body), tc.want)
		})
	}
	if f.payments.refundCount != 0 {
		t.Fatal("unauthorized request reached payment provider")
	}
	expectStatus(t, f.call(t, "GET", "/api/auth/me", f.customerCookie, "", nil), 200)
	var count int
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM access_keys WHERE tenant_id=?", f.customer.TenantID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("origin-rejected mutation created a key")
	}
	w := f.call(t, "POST", "/api/auth/logout", f.customerCookie, origin, nil)
	expectStatus(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != -1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("logout cookie does not securely expire")
	}
	expectStatus(t, f.call(t, "GET", "/api/state", f.customerCookie, "", nil), 401)
}

func TestAPIKeyOwnershipAndSecretRedaction(t *testing.T) {
	f := newAPIFixture(t)
	origin := f.s.AppURL
	proxyID := f.prefix + "p"
	f.proxies = append(f.proxies, proxyID)
	proxySecret := "proxy-password-" + testID()
	proxyURL := "socks5://proxy-user:" + proxySecret + "@proxy.example.test:1080"
	w := f.call(t, "POST", "/api/admin/proxies", f.operatorCookie, origin, map[string]any{"id": proxyID, "name": "Private exit", "url": proxyURL, "enabled": true})
	expectStatus(t, w, 200)
	if strings.Contains(w.Body.String(), proxySecret) || strings.Contains(w.Body.String(), "proxy-user") {
		t.Fatal("proxy API echoed auth credentials")
	}
	credentialID := f.prefix + "c"
	f.credentials = append(f.credentials, credentialID)
	upstreamSecret := "sk-test-" + testID()
	w = f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, origin, map[string]any{"id": credentialID, "name": "Private upstream", "endpoint_id": "openai-official", "secret": upstreamSecret, "quota_group": f.prefix, "proxy_id": proxyID, "enabled": true, "rpm": 30, "tpm": 10000, "max_concurrency": 3, "budget_micros": 1000000, "spent_micros": 900, "held_micros": 800})
	expectStatus(t, w, 200)
	cred := decodeResponse[domain.Credential](t, w)
	if strings.Contains(w.Body.String(), upstreamSecret) || cred.SpentMicros != 0 || cred.HeldMicros != 0 {
		t.Fatal("credential secret echoed or client changed accounting")
	}
	modelID := f.prefix + "m"
	f.models = append(f.models, modelID)
	w = f.call(t, "POST", "/api/admin/models", f.operatorCookie, origin, domain.Model{ID: modelID, Name: "Customer model", Provider: "openai", UpstreamModel: "private-upstream-model-" + f.prefix, Enabled: true, InputRate: 1000000, OutputRate: 2000000, CostInputRate: 777777, CostOutputRate: 888888, MaxOutputTokens: 1024, Version: 9000})
	expectStatus(t, w, 200)
	model := decodeResponse[domain.Model](t, w)
	if model.Version != 1 {
		t.Fatal("client controlled pricing version")
	}
	w = f.call(t, "POST", "/api/keys", f.customerCookie, origin, map[string]any{"name": "Customer access", "models": []string{modelID}, "rpm": 25})
	expectStatus(t, w, 201)
	created := decodeResponse[struct {
		Key    domain.AccessKey `json:"key"`
		Secret string           `json:"secret"`
	}](t, w)
	if !strings.HasPrefix(created.Secret, "rp_live_") || created.Key.TenantID != f.customer.TenantID {
		t.Fatal("key missing one-time secret or wrong tenant")
	}
	if _, err := f.db.AuthenticateKey(f.ctx, created.Secret); err != nil {
		t.Fatalf("issued secret did not authenticate: %v", err)
	}
	for _, cookie := range []*http.Cookie{f.customerCookie, f.operatorCookie} {
		state := f.call(t, "GET", "/api/state", cookie, "", nil)
		expectStatus(t, state, 200)
		for _, secret := range []string{created.Secret, upstreamSecret, proxySecret, "test-password-strong"} {
			if strings.Contains(state.Body.String(), secret) {
				t.Fatal("state response disclosed a secret")
			}
		}
		if strings.Contains(state.Body.String(), "password_hash") || strings.Contains(state.Body.String(), "token_hash") {
			t.Fatal("state response disclosed auth hashes")
		}
	}
	state := f.call(t, "GET", "/api/state", f.customerCookie, "", nil)
	snap := decodeResponse[domain.Snapshot](t, state)
	if len(snap.Endpoints) != 0 || len(snap.Credentials) != 0 || len(snap.Proxies) != 0 || len(snap.Customers) != 0 {
		t.Fatal("customer state contains operator infrastructure")
	}
	for _, m := range snap.Models {
		if m.CostInputRate != 0 || m.CostOutputRate != 0 || m.CostCachedRate != 0 || m.CostCacheWriteRate != 0 || m.UpstreamModel != "" {
			t.Fatal("customer state exposed procurement details")
		}
	}
	other := decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", f.otherCookie, "", nil))
	if len(other.Keys) != 0 {
		t.Fatal("other tenant sees customer keys")
	}
	expectStatus(t, f.call(t, "DELETE", "/api/keys/"+created.Key.ID, f.otherCookie, origin, nil), 404)
	if _, err := f.db.AuthenticateKey(f.ctx, created.Secret); err != nil {
		t.Fatal("cross-tenant revoke modified key")
	}
	expectStatus(t, f.call(t, "DELETE", "/api/keys/"+created.Key.ID, f.customerCookie, origin, nil), 200)
	if _, err := f.db.AuthenticateKey(f.ctx, created.Secret); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("revoked key still authenticates")
	}
}

func TestAPIConfigurationAndBillingBounds(t *testing.T) {
	f := newAPIFixture(t)
	origin := f.s.AppURL
	proxy := func(suffix, url string) map[string]any {
		id := f.prefix + suffix
		f.proxies = append(f.proxies, id)
		return map[string]any{"id": id, "name": "Invalid proxy", "url": url, "enabled": true}
	}
	tests := []struct {
		name, path string
		body       any
	}{
		{"insecure endpoint", "/api/admin/endpoints", map[string]any{"name": "Bad endpoint", "provider": "openai", "base_url": "http://api.example.test"}},
		{"endpoint URL credentials", "/api/admin/endpoints", map[string]any{"name": "Bad endpoint", "provider": "openai", "base_url": "https://user:password@api.example.test"}},
		{"endpoint URL query", "/api/admin/endpoints", map[string]any{"name": "Bad endpoint", "provider": "openai", "base_url": "https://api.example.test?api_key=hidden"}},
		{"unsupported provider", "/api/admin/endpoints", map[string]any{"name": "Bad endpoint", "provider": "other"}},
		{"proxy unsupported scheme", "/api/admin/proxies", proxy("p1", "https://proxy.example.test:1080")},
		{"proxy missing port", "/api/admin/proxies", proxy("p2", "socks5://proxy.example.test")},
		{"proxy zero port", "/api/admin/proxies", proxy("p3", "socks5://proxy.example.test:0")},
		{"proxy too large port", "/api/admin/proxies", proxy("p4", "socks5://proxy.example.test:99999")},
		{"invalid route strategy", "/api/admin/routing", domain.RouteConfig{Strategy: "random", TenantRPM: 20, TenantConcurrency: 2}},
		{"invalid route concurrency", "/api/admin/routing", domain.RouteConfig{Strategy: "least_load", TenantRPM: 20, TenantConcurrency: 0}},
		{"model negative price", "/api/admin/models", domain.Model{ID: f.prefix + "negative", Name: "Invalid model", Provider: "openai", UpstreamModel: "test", InputRate: -1, OutputRate: 1000, MaxOutputTokens: 100}},
		{"enabled zero price", "/api/admin/models", domain.Model{ID: f.prefix + "free", Name: "Invalid model", Provider: "openai", UpstreamModel: "test", Enabled: true, MaxOutputTokens: 100}},
		{"model excessive output", "/api/admin/models", domain.Model{ID: f.prefix + "limit", Name: "Invalid model", Provider: "openai", UpstreamModel: "test", InputRate: 1000, OutputRate: 1000, MaxOutputTokens: 65537}},
		{"negative adjustment", "/api/admin/credits", map[string]any{"tenant_id": f.customer.TenantID, "amount_micros": -1, "reason": "Test adjustment", "idempotency_key": f.prefix}},
		{"missing adjustment audit", "/api/admin/credits", map[string]any{"tenant_id": f.customer.TenantID, "amount_micros": 100, "reason": "", "idempotency_key": f.prefix}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expectStatus(t, f.call(t, "POST", tc.path, f.operatorCookie, origin, tc.body), 400)
		})
	}
	for _, amount := range []int64{-1, 0, 499, 100001} {
		expectStatus(t, f.call(t, "POST", "/api/billing/checkout", f.customerCookie, origin, map[string]any{"amount_cents": amount}), 400)
	}
	if f.payments.checkoutCount != 0 {
		t.Fatal("invalid topup reached payment provider")
	}
	expectStatus(t, f.call(t, "POST", "/api/billing/checkout", f.customerCookie, origin, map[string]any{"amount_cents": 500}), 200)
	if f.payments.checkoutCount != 1 || f.payments.checkoutTenant != f.customer.TenantID || f.payments.checkoutAmount != 500 {
		t.Fatal("checkout used client-controlled ownership or wrong amount")
	}
	adjust := map[string]any{"tenant_id": f.customer.TenantID, "amount_micros": 1000000, "reason": "Integration adjustment audit", "idempotency_key": f.prefix + "credit"}
	for i := 0; i < 2; i++ {
		expectStatus(t, f.call(t, "POST", "/api/admin/credits", f.operatorCookie, origin, adjust), 200)
	}
	state := decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", f.customerCookie, "", nil))
	if state.Wallet.BalanceMicros != 1000000 || len(state.Ledger) != 1 || !strings.Contains(state.Ledger[0].Reason, f.operator.ID) {
		t.Fatal("adjustment not idempotent or missing actor audit")
	}
}

func TestAPIAuthRegistrationAndSessions(t *testing.T) {
	f := newAPIFixture(t)
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		addr = os.Getenv("REDIS_ADDR")
	}
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR/REDIS_ADDR required for auth integration")
	}
	f.s.Redis = redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD")})
	t.Cleanup(func() { f.s.Redis.Close() })
	if err := f.s.Redis.Ping(f.ctx).Err(); err != nil {
		t.Fatal(err)
	}
	email := f.prefix + "new@example.test"
	body := map[string]any{"name": "Registered customer", "email": email, "password": "test-password-strong"}
	w := f.call(t, "POST", "/api/auth/register", nil, f.s.AppURL, body)
	expectStatus(t, w, 200)
	result := decodeResponse[struct {
		User domain.User `json:"user"`
	}](t, w)
	f.users = append(f.users, result.User)
	if result.User.Operator || result.User.PasswordHash != "" || strings.Contains(w.Body.String(), "test-password-strong") {
		t.Fatal("public registration granted privilege or leaked password")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].MaxAge <= 0 {
		t.Fatal("session cookie lacks security attributes")
	}
	expectStatus(t, f.call(t, "GET", "/api/auth/me", cookies[0], "", nil), 200)
	w = f.call(t, "POST", "/api/auth/login", nil, f.s.AppURL, map[string]any{"email": email, "password": "wrong-password-valid-length"})
	expectStatus(t, w, 401)
	expectStatus(t, f.call(t, "POST", "/api/auth/login", nil, f.s.AppURL, map[string]any{"email": strings.ToUpper(email), "password": "test-password-strong"}), 200)
	if _, err := f.db.DB.ExecContext(f.ctx, "UPDATE tenants SET status='suspended' WHERE id=?", result.User.TenantID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.call(t, "GET", "/api/state", cookies[0], "", nil), 401)
	expectStatus(t, f.call(t, "POST", "/api/auth/login", nil, f.s.AppURL, map[string]any{"email": email, "password": "test-password-strong"}), 401)
}

func TestAuthLimiterBlocksSprayingAndDistributedAttempts(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		addr = os.Getenv("REDIS_ADDR")
	}
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR/REDIS_ADDR required for limiter integration")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD")})
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	s := &Server{Redis: client}
	keys := map[string]bool{}
	t.Cleanup(func() {
		all := make([]string, 0, len(keys))
		for key := range keys {
			all = append(all, key)
		}
		if len(all) > 0 {
			client.Del(context.Background(), all...)
		}
	})
	hash := func(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
	for _, kind := range []string{"pair", "ip", "email"} {
		t.Run(kind, func(t *testing.T) {
			id := testID()
			limit := 30
			if kind == "ip" {
				limit = 100
			} else if kind == "email" {
				limit = 40
			}
			for i := 0; i <= limit; i++ {
				host := fmt.Sprintf("fd00:%s:%s::1", id[:4], id[4:8])
				email := id + "@example.test"
				if kind == "ip" {
					email = fmt.Sprintf("%s-%d@example.test", id, i)
				} else if kind == "email" {
					host = fmt.Sprintf("fd00:%s:%s::%x", id[:4], id[4:8], i+1)
				}
				keys["rp:auth:pair:"+hash(host+"|"+email)] = true
				keys["rp:auth:ip:"+hash(host)] = true
				keys["rp:auth:email:"+hash(email)] = true
				r := httptest.NewRequest("POST", "/api/auth/login", nil)
				r.RemoteAddr = "[" + host + "]:12345"
				if i%2 == 1 {
					email = strings.ToUpper(email)
				}
				if got := s.authLimit(ctx, r, email); got != (i < limit) {
					t.Fatalf("%s attempt %d allowed=%v", kind, i+1, got)
				}
			}
		})
	}
}

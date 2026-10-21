package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/relay"
)

func TestCredentialDiscoveryRequiresAdminAndOriginBeforeNetwork(t *testing.T) {
	f := newAPIFixture(t)
	f.enablePublicRequests(t)
	body := map[string]string{"endpoint_id": "openai-official", "secret": "never-sent-test-key"}
	path := "/api/admin/credentials/discover"
	expectStatus(t, f.call(t, "POST", path, nil, f.s.AdminAppURL, body), 401)
	expectStatus(t, f.call(t, "POST", path, f.customerCookie, f.s.AdminAppURL, body), 401)
	expectStatus(t, f.call(t, "POST", path, f.operatorCookie, "https://attacker.invalid", body), 403)
	expectStatus(t, f.call(t, "GET", path, f.operatorCookie, "", nil), 405)
	for _, bad := range []map[string]string{
		{"endpoint_id": "openai-official"},
		{"endpoint_id": "openai-official", "secret": "bad\r\nInjected: yes"},
		{"endpoint_id": "anthropic-official", "secret": "test-key", "workspace_id": "wrong_workspace"},
	} {
		expectStatus(t, f.call(t, "POST", path, f.operatorCookie, f.s.AdminAppURL, bad), 400)
	}
}

func TestCredentialDiscoveryReusesStoredSecretWithoutPersistingOrExposingIt(t *testing.T) {
	f := newAPIFixture(t)
	f.enablePublicRequests(t)
	f.s.AllowPrivate = true
	const secret = "sk-ant-discovery-stored-private-secret"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("X-Api-Key") != secret || r.Header.Get("Anthropic-Workspace-Id") != "wrkspc_Discovery" {
			t.Error("discovery did not use stored key, selected workspace, or metadata endpoint")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-discovery","display_name":"Claude discovery","max_tokens":4096}]}`)
	}))
	defer upstream.Close()
	endpoint, err := f.db.SaveEndpoint(f.ctx, domain.Endpoint{ID: f.prefix + "discovery", Name: "Discovery upstream", Provider: "anthropic", BaseURL: upstream.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f.endpoints = append(f.endpoints, endpoint.ID)
	credential, err := f.db.SaveCredential(f.ctx, domain.Credential{Name: "Saved key", EndpointID: endpoint.ID, Secret: secret, Enabled: true, RPM: 20, TPM: 20000, MaxConcurrency: 5, QuotaGroup: "original-group", WorkspaceID: "wrkspc_Original"})
	if err != nil {
		t.Fatal(err)
	}
	f.credentials = append(f.credentials, credential.ID)
	var before int
	f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM credentials").Scan(&before)
	body := map[string]string{"id": credential.ID, "endpoint_id": endpoint.ID, "workspace_id": "wrkspc_Discovery"}
	w := f.call(t, "POST", "/api/admin/credentials/discover", f.operatorCookie, f.s.AdminAppURL, body)
	expectStatus(t, w, 200)
	result := decodeResponse[relay.DiscoveryResult](t, w)
	if result.Status != "verified" || len(result.Models) != 1 || result.WorkspaceID != "wrkspc_Discovery" || strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), `"secret"`) || len(w.Result().Cookies()) != 0 {
		t.Fatal("discovery response invalid or secret/session exposed")
	}
	var after int
	f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM credentials").Scan(&after)
	if before != after || calls.Load() != 1 {
		t.Fatal("discovery saved a credential or made extra network calls")
	}
	credentials, err := f.db.ListCredentials(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range credentials {
		if saved.ID == credential.ID && (saved.WorkspaceID != "wrkspc_Original" || saved.RPM != 20 || saved.Secret != secret) {
			t.Fatal("discovery mutated stored configuration")
		}
	}
	// Editing the target must never silently forward an existing key elsewhere.
	body["endpoint_id"] = "anthropic-official"
	w = f.call(t, "POST", "/api/admin/credentials/discover", f.operatorCookie, f.s.AdminAppURL, body)
	expectStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), "stored_key_endpoint_mismatch") || calls.Load() != 1 {
		t.Fatal("stored-key reuse was not bound to its saved endpoint")
	}
}

func TestCredentialDiscoveryRateLimitAndProxySelectionFailClosed(t *testing.T) {
	f := newAPIFixture(t)
	f.enablePublicRequests(t)
	body := map[string]string{"endpoint_id": "openai-official", "secret": "no-network-call-key", "proxy_id": "missing-proxy"}
	w := f.call(t, "POST", "/api/admin/credentials/discover", f.operatorCookie, f.s.AdminAppURL, body)
	expectStatus(t, w, 404)
	proxy, err := f.db.SaveProxy(f.ctx, domain.Proxy{Name: "Disabled discovery proxy", URL: "socks5://private-user:private-password@127.0.0.1:1", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	f.proxies = append(f.proxies, proxy.ID)
	body["proxy_id"] = proxy.ID
	w = f.call(t, "POST", "/api/admin/credentials/discover", f.operatorCookie, f.s.AdminAppURL, body)
	expectStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), "proxy_disabled") || strings.Contains(w.Body.String(), "private-password") {
		t.Fatal("disabled proxy rejection leaked credentials or lacked actionable status")
	}
	if err := f.s.Redis.Set(f.ctx, "rp:discovery:"+f.operator.ID, 10, 0).Err(); err != nil {
		t.Fatal(err)
	}
	w = f.call(t, "POST", "/api/admin/credentials/discover", f.operatorCookie, f.s.AdminAppURL, body)
	expectStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("admin discovery rate limit omitted retry guidance")
	}
	f.s.Redis.Del(f.ctx, "rp:discovery:"+f.operator.ID)
	f.s.Redis.Close()
	expectStatus(t, f.call(t, "POST", "/api/admin/credentials/discover", f.operatorCookie, f.s.AdminAppURL, body), 503)
}

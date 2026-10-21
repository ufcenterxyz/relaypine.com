package api

import (
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestAdministratorCredentialWorkspaceRoundTripAndSecretRedaction(t *testing.T) {
	f := newAPIFixture(t)
	secret := "sk-ant-usr-api-workspace-test-private-secret"
	payload := map[string]any{
		"name": "Anthropic workspace credential", "endpoint_id": "anthropic-official",
		"workspace_id": "  wrkspc_Api123  ", "secret": secret, "quota_group": f.prefix,
		"enabled": true, "rpm": 100, "tpm": 10000, "max_concurrency": 10,
		"budget_micros": 1000000, "priority": 1,
	}
	w := f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AppURL, payload)
	expectStatus(t, w, 200)
	saved := decodeResponse[domain.Credential](t, w)
	f.credentials = append(f.credentials, saved.ID)
	if saved.WorkspaceID != "wrkspc_Api123" || strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), `"secret"`) {
		t.Fatal("save response failed workspace normalization or secret redaction")
	}
	payload["id"] = saved.ID
	payload["workspace_id"] = "wrkspc_ApiUpdated456"
	delete(payload, "secret")
	w = f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AppURL, payload)
	expectStatus(t, w, 200)
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("workspace-only update exposed the stored API key")
	}
	w = f.call(t, "GET", "/api/state", f.operatorCookie, "", nil)
	expectStatus(t, w, 200)
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("snapshot exposed the stored API key")
	}
	snapshot := decodeResponse[domain.Snapshot](t, w)
	found := false
	for _, credential := range snapshot.Credentials {
		if credential.ID == saved.ID {
			found = credential.WorkspaceID == "wrkspc_ApiUpdated456"
		}
	}
	if !found {
		t.Fatal("updated workspace was missing from snapshot")
	}
	credentials, err := f.db.ListCredentials(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range credentials {
		if credential.ID == saved.ID && credential.Secret != secret {
			t.Fatal("omitted secret replaced the encrypted API key")
		}
	}
	// Omission intentionally clears the optional selection without a legacy mode.
	delete(payload, "workspace_id")
	w = f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AppURL, payload)
	expectStatus(t, w, 200)
	if decodeResponse[domain.Credential](t, w).WorkspaceID != "" {
		t.Fatal("omitting workspace did not clear explicit selection")
	}
}

func TestAdministratorCredentialWorkspaceRejectsUnsafeAndWrongProviderValues(t *testing.T) {
	f := newAPIFixture(t)
	payload := map[string]any{
		"name": "Workspace validation", "endpoint_id": "anthropic-official",
		"secret": "sk-ant-usr-api-validation-key", "quota_group": f.prefix,
		"enabled": true, "rpm": 100, "tpm": 10000, "max_concurrency": 10,
		"budget_micros": 1000000, "priority": 1,
	}
	var before int
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM credentials").Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{"wrkspc_", "wrkspc_has space", "wrkspc_valid\r\nX-Injected: true", "wrkspc_" + strings.Repeat("a", 122)} {
		payload["workspace_id"] = workspace
		w := f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AppURL, payload)
		expectStatus(t, w, 400)
		if !strings.Contains(w.Body.String(), `"code":"invalid_workspace_id"`) || !strings.Contains(w.Body.String(), "wrkspc_") {
			t.Fatal("invalid workspace response did not identify the field and expected format")
		}
	}
	payload["workspace_id"] = "wrkspc_Valid123"
	payload["endpoint_id"] = "openai-official"
	w := f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AppURL, payload)
	expectStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), `"code":"workspace_provider_mismatch"`) || !strings.Contains(w.Body.String(), "Anthropic") {
		t.Fatal("provider mismatch response did not explain the supported provider")
	}
	var after int
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM credentials").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("rejected credentials were persisted")
	}
	payload["endpoint_id"] = "anthropic-official"
	delete(payload, "workspace_id")
	w = f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AppURL, payload)
	expectStatus(t, w, 200)
	saved := decodeResponse[domain.Credential](t, w)
	f.credentials = append(f.credentials, saved.ID)
	if saved.WorkspaceID != "" {
		t.Fatal("server inferred workspace from API-key prefix")
	}
}

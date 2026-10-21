package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestCredentialWorkspacePersistenceAndSecretPreservation(t *testing.T) {
	f := integrationFixture(t)
	candidate := f.cred
	candidate.ID = ""
	candidate.EndpointID = "anthropic-official"
	candidate.WorkspaceID = "  wrkspc_Example123\t"
	candidate.Secret = "sk-ant-usr-workspace-test-private-secret"
	saved, err := f.s.SaveCredential(f.ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.s.DB.ExecContext(context.Background(), "DELETE FROM credentials WHERE id=?", saved.ID) })
	assertStored := func(workspace string) {
		t.Helper()
		credentials, err := f.s.ListCredentials(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, credential := range credentials {
			if credential.ID == saved.ID {
				found = true
				if credential.WorkspaceID != workspace || credential.Secret != candidate.Secret {
					t.Fatal("stored workspace or encrypted API key did not survive round trip")
				}
			}
		}
		if !found {
			t.Fatal("saved credential was not returned")
		}
		var raw []byte
		if err := f.s.DB.QueryRowContext(f.ctx, "SELECT config FROM credentials WHERE id=?", saved.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		if config["workspace_id"] != workspace || config["secret"] != nil || strings.Contains(string(raw), candidate.Secret) {
			t.Fatal("JSON config lost workspace or exposed the plaintext API key")
		}
	}
	if saved.WorkspaceID != "wrkspc_Example123" {
		t.Fatal("workspace was not trimmed before saving")
	}
	assertStored("wrkspc_Example123")
	saved.Secret = ""
	saved.WorkspaceID = "wrkspc_Updated456"
	saved, err = f.s.SaveCredential(f.ctx, saved)
	if err != nil {
		t.Fatal(err)
	}
	assertStored("wrkspc_Updated456")
	saved.Secret = ""
	saved.WorkspaceID = "  "
	if _, err = f.s.SaveCredential(f.ctx, saved); err != nil {
		t.Fatal(err)
	}
	assertStored("")
}

func TestCredentialWorkspaceRejectsInvalidIDsAndNonAnthropicEndpoints(t *testing.T) {
	f := integrationFixture(t)
	candidate := f.cred
	candidate.EndpointID = "anthropic-official"
	candidate.Secret = ""
	for _, workspace := range []string{
		"workspace_123", "wrkspc_", "wrkspc_has-dash", "wrkspc_has space", "wrkspc_é",
		"wrkspc_valid\r\nX-Injected: true", "wrkspc_" + strings.Repeat("a", 122),
	} {
		candidate.WorkspaceID = workspace
		if _, err := f.s.SaveCredential(f.ctx, candidate); !errors.Is(err, ErrInvalidWorkspaceID) {
			t.Fatalf("invalid workspace ID did not produce the validation error: %q, %v", workspace, err)
		}
	}
	candidate.EndpointID = "openai-official"
	candidate.WorkspaceID = "wrkspc_Valid123"
	if _, err := f.s.SaveCredential(f.ctx, candidate); !errors.Is(err, ErrWorkspaceProviderMismatch) {
		t.Fatal("OpenAI credential did not produce the workspace provider validation error")
	}
	// Failed edits must preserve the original credential and its secret.
	credentials, err := f.s.ListCredentials(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range credentials {
		if credential.ID == candidate.ID && (credential.EndpointID != "openai-official" || credential.WorkspaceID != "" || credential.Secret != f.cred.Secret) {
			t.Fatal("rejected workspace edit changed the stored credential")
		}
	}
	// The key prefix does not identify whether workspace selection is required.
	candidate.EndpointID = "anthropic-official"
	candidate.WorkspaceID = ""
	candidate.Secret = "sk-ant-usr-scoped-key-without-workspace"
	if _, err = f.s.SaveCredential(f.ctx, candidate); err != nil {
		t.Fatal("API-key prefix incorrectly required a workspace")
	}
	// The longest valid identifier is accepted by the shared boundary validator.
	if !domain.ValidAnthropicWorkspaceID("wrkspc_" + strings.Repeat("a", 121)) {
		t.Fatal("128-byte workspace ID rejected")
	}
}

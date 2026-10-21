package api

import (
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestMinimalCredentialCreationUsesLocalDefaultsAndSharedQuota(t *testing.T) {
	f := newAPIFixture(t)
	var first domain.Credential
	for i := 0; i < 2; i++ {
		w := f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AdminAppURL, map[string]any{
			"endpoint_id": "openai-official", "secret": "  test-onboarding-key  ",
		})
		expectStatus(t, w, 200)
		c := decodeResponse[domain.Credential](t, w)
		f.credentials = append(f.credentials, c.ID)
		if c.Name == "" || c.RPM != 60 || c.TPM != 100000 || c.MaxConcurrency != 10 || !c.Enabled || c.BudgetMicros != 0 || c.QuotaGroup != "auto:openai-official" {
			t.Fatalf("unexpected minimal creation defaults: %+v", c)
		}
		if strings.Contains(w.Body.String(), "test-onboarding-key") {
			t.Fatal("creation exposed API key")
		}
		if i == 0 {
			first = c
		} else if first.ID == c.ID || first.QuotaGroup != c.QuotaGroup {
			t.Fatal("new keys should be distinct but conservatively share endpoint capacity")
		}
	}
	credentials, err := f.db.ListCredentials(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range credentials {
		if c.ID == first.ID && c.Secret != "test-onboarding-key" {
			t.Fatal("save did not normalize the pasted API key")
		}
	}
}

func TestCredentialDefaultsNeverReplaceExplicitOverrides(t *testing.T) {
	f := newAPIFixture(t)
	payload := map[string]any{
		"endpoint_id": "anthropic-official", "secret": "test-manual-key", "name": "Manual key",
		"quota_group": "manual-group", "rpm": 23, "tpm": 45678, "max_concurrency": 2,
		"enabled": false, "budget_micros": 1000000, "priority": 3,
	}
	w := f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AdminAppURL, payload)
	expectStatus(t, w, 200)
	c := decodeResponse[domain.Credential](t, w)
	f.credentials = append(f.credentials, c.ID)
	if c.Name != "Manual key" || c.QuotaGroup != "manual-group" || c.RPM != 23 || c.TPM != 45678 || c.MaxConcurrency != 2 || c.Enabled || c.BudgetMicros != 1000000 || c.Priority != 3 {
		t.Fatalf("defaults overwrote explicit settings: %+v", c)
	}
	for _, field := range []string{"rpm", "tpm", "max_concurrency"} {
		invalid := map[string]any{"endpoint_id": "anthropic-official", "secret": "test-manual-key", field: 0}
		expectStatus(t, f.call(t, "POST", "/api/admin/credentials", f.operatorCookie, f.s.AdminAppURL, invalid), 400)
	}
}

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

func backupFixture(t *testing.T, f *fixture, vendor string) domain.Credential {
	t.Helper()
	backup, err := f.s.SaveBackup(f.ctx, vendor, domain.Credential{Secret: vendor + "-integration-secret", RPM: 60, TPM: 100000, MaxConcurrency: 10, Priority: 1}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean := context.Background()
		// Requests reference the credential, so remove this tenant's history first.
		for _, q := range []string{"DELETE a FROM attempts a JOIN requests r ON r.id=a.request_id WHERE r.tenant_id=?", "DELETE FROM requests WHERE tenant_id=?"} {
			_, _ = f.s.DB.ExecContext(clean, q, f.user.TenantID)
		}
		_, _ = f.s.DB.ExecContext(clean, "DELETE FROM credentials WHERE id=?", backup.ID)
		_, _ = f.s.DB.ExecContext(clean, "DELETE FROM endpoints WHERE id=?", domain.BackupEndpointID(vendor))
	})
	return backup
}

func held(t *testing.T, f *fixture, credential string) (int64, int64) {
	t.Helper()
	var spent, holds int64
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT spent_micros,held_micros FROM credentials WHERE id=?", credential).Scan(&spent, &holds); err != nil {
		t.Fatal(err)
	}
	return spent, holds
}

func reserveRequest(t *testing.T, f *fixture, id, credential string) {
	t.Helper()
	err := f.s.Reserve(f.ctx, domain.Reservation{Request: domain.Request{ID: id, TenantID: f.user.TenantID, ProjectID: f.key.ProjectID, AccessKeyID: f.key.ID, Provider: "openai", Model: f.model.ID, ReservedMicros: 1000}, Model: f.model, CredentialID: credential, CostReserve: 500})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRerouteMovesOnlyTheUpstreamHold(t *testing.T) {
	f := integrationFixture(t)
	backup := backupFixture(t, f, "openrouter")
	if err := f.s.Credit(f.ctx, f.user.TenantID, 5000, "reroute-test-credit-"+time.Now().Format("150405.000000"), "Reroute integration test"); err != nil {
		t.Fatal(err)
	}
	id := newID("req_")
	reserveRequest(t, f, id, f.cred.ID)
	_, primaryBefore := held(t, f, f.cred.ID)
	var walletHeld int64
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT held_micros FROM wallets WHERE tenant_id=?", f.user.TenantID).Scan(&walletHeld); err != nil || walletHeld != 1000 {
		t.Fatalf("wallet hold %d %v", walletHeld, err)
	}
	if err := f.s.Reroute(f.ctx, id, domain.Reroute{FromCredentialID: f.cred.ID, ToCredentialID: backup.ID, CostReserve: 500, ErrorCode: "upstream_529"}); err != nil {
		t.Fatal(err)
	}
	if _, after := held(t, f, f.cred.ID); after != primaryBefore-500 {
		t.Fatalf("primary hold %d -> %d", primaryBefore, after)
	}
	if _, after := held(t, f, backup.ID); after != 500 {
		t.Fatalf("backup hold %d", after)
	}
	var walletAfter int64
	var credential string
	_ = f.s.DB.QueryRowContext(f.ctx, "SELECT held_micros FROM wallets WHERE tenant_id=?", f.user.TenantID).Scan(&walletAfter)
	_ = f.s.DB.QueryRowContext(f.ctx, "SELECT credential_id FROM requests WHERE id=?", id).Scan(&credential)
	if walletAfter != 1000 || credential != backup.ID {
		t.Fatalf("wallet %d credential %s", walletAfter, credential)
	}
	// A request can be rerouted only from its current, unfinished attempt.
	if err := f.s.Reroute(f.ctx, id, domain.Reroute{FromCredentialID: f.cred.ID, ToCredentialID: backup.ID, CostReserve: 500}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale reroute %v", err)
	}
	if err := f.s.Settle(f.ctx, id, domain.Settlement{Status: "completed", Usage: domain.Usage{InputTokens: 100, OutputTokens: 10, Known: true}, ChargeMicros: 120, CostMicros: 60}); err != nil {
		t.Fatal(err)
	}
	if spent, holds := held(t, f, backup.ID); spent != 60 || holds != 0 {
		t.Fatalf("backup spent %d held %d", spent, holds)
	}
	if spent, _ := held(t, f, f.cred.ID); spent != 0 {
		t.Fatalf("refused attempt charged upstream cost %d", spent)
	}
	var balance int64
	_ = f.s.DB.QueryRowContext(f.ctx, "SELECT balance_micros-held_micros FROM wallets WHERE tenant_id=?", f.user.TenantID).Scan(&balance)
	if balance != 5000-120 {
		t.Fatalf("customer charged %d", 5000-balance)
	}
	trails, err := f.s.listAttempts(f.ctx, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	trail := trails[id]
	if len(trail) != 2 || trail[0] != (domain.Attempt{CredentialID: f.cred.ID, Status: attemptFailedOver, ErrorCode: "upstream_529"}) || trail[1].CredentialID != backup.ID || trail[1].Status != "settled" {
		t.Fatalf("attempts %+v", trail)
	}
	snapshot, err := f.s.Snapshot(f.ctx, f.user)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snapshot.Requests {
		if r.ID == id && len(r.Attempts) != 2 {
			t.Fatalf("administrator snapshot attempts %+v", r.Attempts)
		}
	}
	if err = f.s.Reroute(f.ctx, id, domain.Reroute{FromCredentialID: backup.ID, ToCredentialID: f.cred.ID, CostReserve: 500}); !errors.Is(err, ErrConflict) {
		t.Fatalf("settled request rerouted: %v", err)
	}
}

func TestRerouteRefusesUnusableTargets(t *testing.T) {
	f := integrationFixture(t)
	backup := backupFixture(t, f, "zenmux")
	if err := f.s.Credit(f.ctx, f.user.TenantID, 5000, "reroute-refuse-credit-"+time.Now().Format("150405.000000"), "Reroute integration test"); err != nil {
		t.Fatal(err)
	}
	id := newID("req_")
	reserveRequest(t, f, id, f.cred.ID)
	_, before := held(t, f, f.cred.ID)
	// Exceeding the target budget changes nothing.
	if _, err := f.s.DB.ExecContext(f.ctx, "UPDATE credentials SET budget_micros=100 WHERE id=?", backup.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Reroute(f.ctx, id, domain.Reroute{FromCredentialID: f.cred.ID, ToCredentialID: backup.ID, CostReserve: 500}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("over budget reroute %v", err)
	}
	if _, after := held(t, f, f.cred.ID); after != before {
		t.Fatalf("failed reroute moved the hold %d -> %d", before, after)
	}
	if _, err := f.s.DB.ExecContext(f.ctx, "UPDATE credentials SET budget_micros=0 WHERE id=?", backup.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SaveBackup(f.ctx, "zenmux", domain.Credential{RPM: 60, TPM: 100000, MaxConcurrency: 10}, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Reroute(f.ctx, id, domain.Reroute{FromCredentialID: f.cred.ID, ToCredentialID: backup.ID, CostReserve: 500}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled backup reroute %v", err)
	}
	if err := f.s.Reroute(f.ctx, id, domain.Reroute{FromCredentialID: f.cred.ID, ToCredentialID: f.cred.ID, CostReserve: 500}); err == nil {
		t.Fatal("rerouted to the same credential")
	}
	// The request is still intact and settles normally on its first route.
	if err := f.s.Settle(f.ctx, id, domain.Settlement{Status: "failed", Usage: domain.Usage{Known: true}, ErrorCode: "upstream_503"}); err != nil {
		t.Fatal(err)
	}
	if _, after := held(t, f, f.cred.ID); after != before-500 {
		t.Fatalf("settlement hold %d -> %d", before, after)
	}
}

func TestBackupConfigurationIsIsolated(t *testing.T) {
	f := integrationFixture(t)
	if _, err := f.s.SaveBackup(f.ctx, "openrouter", domain.Credential{RPM: 60, TPM: 100000, MaxConcurrency: 10}, true, nil); err == nil {
		t.Fatal("new backup saved without an API key")
	}
	backup := backupFixture(t, f, "openrouter")
	if backup.MaskedKey == "" || backup.QuotaGroup != "backup:openrouter" || backup.EndpointID != "backup-openrouter" {
		t.Fatalf("saved backup %+v", backup)
	}
	endpoints, err := f.s.ListEndpoints(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var endpoint domain.Endpoint
	for _, e := range endpoints {
		if e.ID == "backup-openrouter" {
			endpoint = e
		}
	}
	if !endpoint.IsBackup() || endpoint.Vendor != "openrouter" || endpoint.Provider != "" || !endpoint.Serves("anthropic") || !endpoint.Serves("openai") {
		t.Fatalf("backup endpoint %+v", endpoint)
	}
	// Generic upstream edits cannot create, retarget or reuse backups.
	for _, e := range []domain.Endpoint{{Name: "Fake", Provider: "openai", Role: domain.RoleBackup, Vendor: "openrouter"}, {ID: "backup-openrouter", Name: "Hijack", Provider: "openai", BaseURL: "https://example.com"}} {
		if _, err = f.s.SaveEndpoint(f.ctx, e); !errors.Is(err, ErrBackupManaged) {
			t.Fatalf("generic endpoint save %+v: %v", e, err)
		}
	}
	for _, c := range []domain.Credential{{Name: "Hijack", EndpointID: "backup-openrouter", Secret: "x", Enabled: true, RPM: 1, TPM: 1, MaxConcurrency: 1}, {ID: "backup-openrouter", Name: "Hijack", EndpointID: "openai-official", Secret: "x", Enabled: true, RPM: 1, TPM: 1, MaxConcurrency: 1}} {
		if _, err = f.s.SaveCredential(f.ctx, c); !errors.Is(err, ErrBackupManaged) {
			t.Fatalf("generic credential save %+v: %v", c, err)
		}
	}
	// An update without a key keeps the stored key and switches both rows.
	if _, err = f.s.SaveBackup(f.ctx, "openrouter", domain.Credential{RPM: 120, TPM: 100000, MaxConcurrency: 10, Priority: 3}, false, nil); err != nil {
		t.Fatal(err)
	}
	credentials, err := f.s.ListCredentials(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range credentials {
		if c.ID == backup.ID && (c.Secret != "openrouter-integration-secret" || c.Enabled || c.RPM != 120 || c.Priority != 3) {
			t.Fatalf("updated backup %+v", c)
		}
	}
	endpoints, _ = f.s.ListEndpoints(f.ctx)
	for _, e := range endpoints {
		if e.ID == "backup-openrouter" && e.Enabled {
			t.Fatal("disabled backup endpoint still enabled")
		}
	}
	// A disabled backup cannot take a reservation either.
	err = f.s.Reserve(f.ctx, domain.Reservation{Request: domain.Request{ID: newID("req_"), TenantID: f.user.TenantID, ProjectID: f.key.ProjectID, AccessKeyID: f.key.ID, Provider: "anthropic", Model: f.model.ID}, Model: f.model, CredentialID: backup.ID})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled backup reservation %v", err)
	}
}

func TestBackupCanTakeTheFirstReservation(t *testing.T) {
	f := integrationFixture(t)
	backup := backupFixture(t, f, "zenmux")
	if err := f.s.Credit(f.ctx, f.user.TenantID, 5000, "backup-first-credit-"+time.Now().Format("150405.000000"), "Backup integration test"); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"openai", "anthropic"} {
		id := newID("req_")
		err := f.s.Reserve(f.ctx, domain.Reservation{Request: domain.Request{ID: id, TenantID: f.user.TenantID, ProjectID: f.key.ProjectID, AccessKeyID: f.key.ID, Provider: provider, Model: f.model.ID, ReservedMicros: 1000}, Model: f.model, CredentialID: backup.ID, CostReserve: 500})
		if err != nil {
			t.Fatalf("%s reservation: %v", provider, err)
		}
		if err = f.s.Settle(f.ctx, id, domain.Settlement{Status: "completed", Usage: domain.Usage{InputTokens: 10, OutputTokens: 1, Known: true}, ChargeMicros: 12, CostMicros: 6}); err != nil {
			t.Fatal(err)
		}
	}
	if spent, holds := held(t, f, backup.ID); spent != 12 || holds != 0 {
		t.Fatalf("backup spent %d held %d", spent, holds)
	}
}

func TestManagementKeyIsSealedPerVendor(t *testing.T) {
	f := integrationFixture(t)
	backupFixture(t, f, "zenmux")
	t.Cleanup(func() {
		f.s.DB.ExecContext(context.Background(), "DELETE FROM backup_management_keys WHERE vendor IN ('zenmux','openrouter')")
	})
	management := "fixture-zenmux-management"
	if _, err := f.s.SaveBackup(f.ctx, "zenmux", domain.Credential{RPM: 60, TPM: 100000, MaxConcurrency: 10}, true, &management); err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.BackupManagementKey(f.ctx, "zenmux"); err != nil || got != management {
		t.Fatalf("management key %q %v", got, err)
	}
	var sealed []byte
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT encrypted_key FROM backup_management_keys WHERE vendor='zenmux'").Scan(&sealed); err != nil || strings.Contains(string(sealed), management) {
		t.Fatalf("management key stored in plain text: %v", err)
	}
	// A sealed key copied to another vendor cannot be opened there.
	if _, err := f.s.DB.ExecContext(f.ctx, "INSERT INTO backup_management_keys(vendor,encrypted_key,updated_at) VALUES('openrouter',?,UTC_TIMESTAMP(6))", sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.BackupManagementKey(f.ctx, "openrouter"); err == nil {
		t.Fatal("management key opened under another vendor")
	}
	if got, _ := f.s.BackupManagementKey(f.ctx, "unknown"); got != "" {
		t.Fatal("missing management key returned a value")
	}
}

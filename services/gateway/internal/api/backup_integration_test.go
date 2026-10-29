package api

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/relay"
)

func TestBackupAdministration(t *testing.T) {
	f := newAPIFixture(t)
	f.credentials = append(f.credentials, "backup-openrouter")
	f.endpoints = append(f.endpoints, "backup-openrouter")
	admin := f.s.AdminAppURL
	secret := "sk-or-v1-" + testID() + testID()
	body := map[string]any{"vendor": "openrouter", "secret": secret, "proxy_id": "", "enabled": true, "priority": 2, "rpm": 300, "tpm": 2000000, "max_concurrency": 20}

	// Backups are administrator configuration, invisible on the customer host.
	expectStatus(t, f.call(t, "POST", "/api/admin/backups", f.customerCookie, f.s.AppURL, body), 404)
	expectStatus(t, f.call(t, "POST", "/api/admin/backups", f.operatorCookie, admin, map[string]any{"vendor": "openrouter"}), 400)
	expectStatus(t, f.call(t, "POST", "/api/admin/backups", f.operatorCookie, admin, map[string]any{"vendor": "example", "secret": secret}), 400)
	expectStatus(t, f.call(t, "POST", "/api/admin/backups", f.operatorCookie, admin, map[string]any{"vendor": "openrouter", "secret": secret, "rpm": 0}), 400)

	w := f.call(t, "POST", "/api/admin/backups", f.operatorCookie, admin, body)
	expectStatus(t, w, 200)
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("backup response exposed the API key")
	}
	saved := decodeResponse[domain.Credential](t, w)
	if saved.ID != "backup-openrouter" || saved.MaskedKey == "" || saved.RPM != 300 || saved.Priority != 2 || !saved.Enabled || saved.BudgetMicros != 0 {
		t.Fatalf("saved backup %+v", saved)
	}
	state := f.call(t, "GET", "/api/state", f.operatorCookie, "", nil)
	if strings.Contains(state.Body.String(), secret) {
		t.Fatal("administrator snapshot exposed the backup key")
	}
	snapshot := decodeResponse[domain.Snapshot](t, state)
	var endpoint domain.Endpoint
	for _, e := range snapshot.Endpoints {
		if e.ID == "backup-openrouter" {
			endpoint = e
		}
	}
	if !endpoint.IsBackup() || endpoint.Vendor != "openrouter" || !endpoint.Enabled {
		t.Fatalf("backup endpoint %+v", endpoint)
	}

	// Partial updates keep the key and the remaining settings.
	expectStatus(t, f.call(t, "POST", "/api/admin/backups", f.operatorCookie, admin, map[string]any{"vendor": "openrouter", "enabled": false}), 200)
	credentials, err := f.db.ListCredentials(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range credentials {
		if c.ID == "backup-openrouter" && (c.Secret != secret || c.Enabled || c.RPM != 300 || c.Priority != 2) {
			t.Fatalf("updated backup %+v", c)
		}
	}

	// The generic upstream forms cannot edit or impersonate a backup.
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/api/admin/endpoints", map[string]any{"name": "Fake backup", "provider": "openai", "base_url": "https://api.example.test", "role": "backup", "vendor": "openrouter"}},
		{"/api/admin/endpoints", map[string]any{"id": "backup-openrouter", "name": "Retarget", "provider": "openai", "base_url": "https://api.example.test"}},
		{"/api/admin/credentials", map[string]any{"endpoint_id": "backup-openrouter", "secret": "sk-other"}},
		{"/api/admin/credentials", map[string]any{"id": "backup-openrouter", "endpoint_id": "openai-official", "name": "Retarget", "quota_group": "auto:openai-official", "secret": "sk-other", "rpm": 60, "tpm": 1000, "max_concurrency": 1, "enabled": true}},
	} {
		w := f.call(t, "POST", tc.path, f.operatorCookie, admin, tc.body)
		expectStatus(t, w, 400)
		if !strings.Contains(w.Body.String(), "backup_managed") {
			t.Fatalf("%s accepted backup edit: %s", tc.path, w.Body.String())
		}
	}
	// Model import takes its catalog from official or compatible upstreams.
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR required for the model import check")
	}
	f.s.Redis = redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD"), DB: 15})
	t.Cleanup(func() { f.s.Redis.Close() })
	expectStatus(t, f.call(t, "POST", "/api/admin/backups", f.operatorCookie, admin, map[string]any{"vendor": "openrouter", "enabled": true}), 200)
	w = f.call(t, "POST", "/api/admin/models/prices/preview", f.operatorCookie, admin, map[string]any{"credential_id": "backup-openrouter", "models": []domain.Model{{ID: f.prefix + "m", Name: "M", Provider: "openai", UpstreamModel: "gpt-5", MaxOutputTokens: 10}}})
	expectStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), "Backup routes serve models") {
		t.Fatalf("backup credential import: %s", w.Body.String())
	}
}

func TestBackupBalances(t *testing.T) {
	f := newAPIFixture(t)
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR required for balance caching")
	}
	f.s.Redis = redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD"), DB: 15})
	t.Cleanup(func() {
		f.s.Redis.Del(context.Background(), backupBalanceKey("zenmux"))
		f.s.Redis.Close()
	})
	f.credentials = append(f.credentials, "backup-zenmux")
	f.endpoints = append(f.endpoints, "backup-zenmux")
	t.Cleanup(func() {
		f.db.DB.ExecContext(context.Background(), "DELETE FROM backup_management_keys WHERE vendor='zenmux'")
	})
	admin := f.s.AdminAppURL
	secret, management := "sk-ai-v1-"+testID(), "sk-mg-v1-"+testID()
	calls := 0
	f.s.BackupBalanceFetcher = func(_ context.Context, vendor, gotSecret, gotManagement, proxyURL string) relay.BackupBalance {
		calls++
		if vendor != "zenmux" || gotSecret != secret || proxyURL != "" {
			t.Errorf("fetch %s %q %q", vendor, gotSecret, proxyURL)
		}
		balance := int64(482740000)
		result := relay.BackupBalance{Vendor: vendor, ManagementKey: gotManagement != "", CheckedAt: "2026-10-28T13:00:00Z"}
		if gotManagement == management {
			result.BalanceMicros = &balance
		}
		return result
	}
	w := f.call(t, "POST", "/api/admin/backups", f.operatorCookie, admin, map[string]any{"vendor": "zenmux", "secret": secret, "management_key": management})
	expectStatus(t, w, 200)
	state := f.call(t, "GET", "/api/state", f.operatorCookie, "", nil)
	for _, body := range []string{w.Body.String(), state.Body.String()} {
		if strings.Contains(body, management) || strings.Contains(body, secret) {
			t.Fatal("response exposed a backup key")
		}
	}
	snapshot := decodeResponse[domain.Snapshot](t, state)
	for _, e := range snapshot.Endpoints {
		if e.ID == "backup-zenmux" && !e.ManagementKey {
			t.Fatal("saved management key not reported")
		}
	}
	if stored, err := f.db.BackupManagementKey(f.ctx, "zenmux"); err != nil || stored != management {
		t.Fatalf("stored management key %v", err)
	}

	read := func(refresh bool) relay.BackupBalance {
		t.Helper()
		w := f.call(t, "POST", "/api/admin/backups/balances", f.operatorCookie, admin, map[string]any{"refresh": refresh})
		expectStatus(t, w, 200)
		if strings.Contains(w.Body.String(), management) || strings.Contains(w.Body.String(), secret) {
			t.Fatal("balance response exposed a key")
		}
		body := decodeResponse[struct {
			Balances []relay.BackupBalance `json:"balances"`
		}](t, w)
		for _, b := range body.Balances {
			if b.Vendor == "zenmux" {
				return b
			}
		}
		t.Fatalf("no ZenMux balance in %+v", body.Balances)
		return relay.BackupBalance{}
	}
	if b := read(false); b.BalanceMicros == nil || *b.BalanceMicros != 482740000 || calls != 1 {
		t.Fatalf("balance %+v calls %d", b, calls)
	}
	if read(false); calls != 1 {
		t.Fatalf("cached balance fetched again: %d", calls)
	}
	if read(true); calls != 2 {
		t.Fatalf("refresh did not fetch: %d", calls)
	}
	// A browser that leaves mid-read does not cancel the vendor read, so the
	// cache keeps the real balance instead of a cancelled read.
	browser, leave := context.WithCancel(context.Background())
	defer leave()
	fetcher := f.s.BackupBalanceFetcher
	f.s.BackupBalanceFetcher = func(ctx context.Context, vendor, gotSecret, gotManagement, proxyURL string) relay.BackupBalance {
		leave()
		if ctx.Err() != nil {
			t.Error("the balance read was cancelled with the browser request")
		}
		return fetcher(ctx, vendor, gotSecret, gotManagement, proxyURL)
	}
	request := httptest.NewRequest("POST", "/api/admin/backups/balances", strings.NewReader(`{"refresh":true}`)).WithContext(browser)
	parsed, _ := url.Parse(admin)
	request.Host, request.RemoteAddr = parsed.Host, f.remote
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", admin)
	request.AddCookie(f.operatorCookie)
	f.h.ServeHTTP(httptest.NewRecorder(), request)
	f.s.BackupBalanceFetcher = fetcher
	if b := read(false); b.BalanceMicros == nil || b.Error != "" || calls != 3 {
		t.Fatalf("balance after the browser left %+v calls %d", b, calls)
	}
	// A check resolves keys exactly as saving would store them: empty fields
	// use the saved keys, a requested removal checks without the management
	// key, and a newly entered management key is used as given.
	var checked string
	f.s.BackupChecker = func(_ context.Context, vendor, gotSecret, gotManagement, proxyURL string, _ []domain.Model) (relay.BackupCheck, error) {
		checked = gotSecret + "|" + gotManagement
		return relay.BackupCheck{Vendor: vendor, Models: []relay.BackupCoverage{}, Balance: relay.BackupBalance{Vendor: vendor}}, nil
	}
	for _, c := range []struct {
		input map[string]any
		want  string
	}{
		{map[string]any{"vendor": "zenmux"}, secret + "|" + management},
		{map[string]any{"vendor": "zenmux", "remove_management_key": true}, secret + "|"},
		{map[string]any{"vendor": "zenmux", "management_key": "fixture-new-management", "remove_management_key": true}, secret + "|fixture-new-management"},
	} {
		expectStatus(t, f.call(t, "POST", "/api/admin/backups/check", f.operatorCookie, admin, c.input), 200)
		if checked != c.want {
			t.Fatalf("check %v used the wrong keys", c.input)
		}
	}
	// Saving the backup without the field keeps the management key; removing
	// it clears both the key and the cached balance.
	expectStatus(t, f.call(t, "POST", "/api/admin/backups", f.operatorCookie, admin, map[string]any{"vendor": "zenmux", "enabled": true}), 200)
	if stored, _ := f.db.BackupManagementKey(f.ctx, "zenmux"); stored != management {
		t.Fatal("management key lost on update")
	}
	expectStatus(t, f.call(t, "POST", "/api/admin/backups", f.operatorCookie, admin, map[string]any{"vendor": "zenmux", "remove_management_key": true}), 200)
	if stored, _ := f.db.BackupManagementKey(f.ctx, "zenmux"); stored != "" {
		t.Fatal("management key not removed")
	}
	if b := read(false); b.BalanceMicros != nil || b.ManagementKey || calls != 4 {
		t.Fatalf("balance after removal %+v calls %d", b, calls)
	}
}

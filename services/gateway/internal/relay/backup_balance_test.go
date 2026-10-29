package relay

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

// vendorServer answers like OpenRouter or ZenMux: the inference key reads its
// own metadata, and only the management key reads the account balance.
func vendorServer(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) domain.BackupVendor {
	t.Helper()
	mux := http.NewServeMux()
	for path, handler := range routes {
		mux.HandleFunc(path, handler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return domain.BackupVendor{OpenAIBaseURL: server.URL + "/api/v1", AnthropicBaseURL: server.URL + "/api", AnthropicAuthMode: "bearer"}
}

func requireKey(key string, w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+key {
		w.WriteHeader(403)
		return false
	}
	return true
}

func TestOpenRouterBalanceUsesManagementKeyForAccount(t *testing.T) {
	vendor := vendorServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/key": func(w http.ResponseWriter, r *http.Request) {
			if requireKey("sk-or-inference", w, r) {
				_, _ = io.WriteString(w, `{"data":{"label":"relaypine","limit":10,"limit_remaining":4.25,"usage":5.75}}`)
			}
		},
		"/api/v1/credits": func(w http.ResponseWriter, r *http.Request) {
			if requireKey("sk-or-management", w, r) {
				_, _ = io.WriteString(w, `{"data":{"total_credits":100.5,"total_usage":25.75}}`)
			}
		},
	})
	vendor.ID, vendor.Name = "openrouter", "OpenRouter"
	got := fetchBackupBalance(context.Background(), vendor, "sk-or-inference", "sk-or-management", http.DefaultClient)
	if got.Error != "" || got.BalanceMicros == nil || *got.BalanceMicros != 74750000 || got.KeyLimitRemainingMicros == nil || *got.KeyLimitRemainingMicros != 4250000 || !got.ManagementKey {
		t.Fatalf("balance %+v", got)
	}
	// Without a management key only the key's own cap is known.
	got = fetchBackupBalance(context.Background(), vendor, "sk-or-inference", "", http.DefaultClient)
	if got.Error != "" || got.BalanceMicros != nil || got.KeyLimitRemainingMicros == nil || got.ManagementKey {
		t.Fatalf("inference-only balance %+v", got)
	}
	// An inference key in the management slot is refused by the vendor.
	got = fetchBackupBalance(context.Background(), vendor, "sk-or-inference", "sk-or-inference", http.DefaultClient)
	if got.BalanceMicros != nil || !strings.Contains(got.Error, "rejected the management key") || strings.Contains(got.Error, "sk-or") {
		t.Fatalf("rejected management key %+v", got)
	}
	got = fetchBackupBalance(context.Background(), vendor, "sk-or-revoked", "sk-or-management", http.DefaultClient)
	if got.BalanceMicros != nil || !strings.Contains(got.Error, "rejected this API key") {
		t.Fatalf("revoked inference key %+v", got)
	}
}

func TestZenMuxBalanceReadsPayAsYouGoCredits(t *testing.T) {
	limited := false
	vendor := vendorServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/management/payg/balance": func(w http.ResponseWriter, r *http.Request) {
			if limited {
				w.WriteHeader(422)
				return
			}
			if requireKey("sk-mg-management", w, r) {
				_, _ = io.WriteString(w, `{"success":true,"data":{"currency":"usd","total_credits":482.74,"top_up_credits":35.0,"bonus_credits":447.74}}`)
			}
		},
	})
	vendor.ID, vendor.Name = "zenmux", "ZenMux"
	got := fetchBackupBalance(context.Background(), vendor, "sk-ai-inference", "sk-mg-management", http.DefaultClient)
	if got.Error != "" || got.BalanceMicros == nil || *got.BalanceMicros != 482740000 || got.KeyLimitRemainingMicros != nil {
		t.Fatalf("balance %+v", got)
	}
	// ZenMux inference keys cannot read any balance.
	got = fetchBackupBalance(context.Background(), vendor, "sk-ai-inference", "", http.DefaultClient)
	if got.Error != "" || got.BalanceMicros != nil || got.ManagementKey {
		t.Fatalf("inference-only balance %+v", got)
	}
	limited = true
	got = fetchBackupBalance(context.Background(), vendor, "sk-ai-inference", "sk-mg-management", http.DefaultClient)
	if got.BalanceMicros != nil || !strings.Contains(got.Error, "rate-limited") {
		t.Fatalf("rate limited %+v", got)
	}
}

func TestBackupCheckReportsAccountBalance(t *testing.T) {
	vendor := vendorServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/key": func(w http.ResponseWriter, r *http.Request) {
			if requireKey("sk-or-inference", w, r) {
				_, _ = io.WriteString(w, `{"data":{"label":"relaypine","limit_remaining":null}}`)
			}
		},
		"/api/v1/credits": func(w http.ResponseWriter, r *http.Request) {
			if requireKey("sk-or-management", w, r) {
				_, _ = io.WriteString(w, `{"data":{"total_credits":20,"total_usage":7.5}}`)
			}
		},
		"/api/v1/models": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"data":[{"id":"openai/gpt-5.4-mini"}]}`)
		},
	})
	vendor.ID, vendor.Name = "openrouter", "OpenRouter"
	models := []domain.Model{{ID: "mini", Provider: "openai", UpstreamModel: "gpt-5.4-mini", Enabled: true}}
	result, err := checkBackup(context.Background(), vendor, "sk-or-inference", "sk-or-management", http.DefaultClient, models)
	if err != nil || result.Balance.BalanceMicros == nil || *result.Balance.BalanceMicros != 12500000 || result.Balance.KeyLimitRemainingMicros != nil || !result.Models[0].Available {
		t.Fatalf("check %+v %v", result, err)
	}
	// A wrong management key does not fail the inference key check.
	result, err = checkBackup(context.Background(), vendor, "sk-or-inference", "sk-or-wrong", http.DefaultClient, models)
	if err != nil || result.Balance.BalanceMicros != nil || !strings.Contains(result.Balance.Error, "management key") || !result.Models[0].Available {
		t.Fatalf("wrong management key %+v %v", result, err)
	}
}

// The OpenRouter catalog serves both protocols: a check downloads it once,
// compressed, and a reply that expands beyond the size limit is refused.
func TestBackupCatalogIsCompressedAndReadOnce(t *testing.T) {
	var catalogReads int
	var bomb bool
	vendor := vendorServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/key": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"data":{"label":"relaypine","limit_remaining":null}}`)
		},
		"/api/v1/models": func(w http.ResponseWriter, r *http.Request) {
			catalogReads++
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				t.Error("catalog request did not ask for compression")
			}
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			defer zw.Close()
			if bomb {
				_, _ = io.WriteString(zw, `{"data":[{"id":"`+strings.Repeat("a", backupCatalogMaxBody)+`"}]}`)
				return
			}
			_, _ = io.WriteString(zw, `{"data":[{"id":"openai/gpt-5.4-mini"},{"id":"anthropic/claude-sonnet-4.5"}]}`)
		},
	})
	vendor.ID, vendor.Name = "openrouter", "OpenRouter"
	models := []domain.Model{
		{ID: "mini", Provider: "openai", UpstreamModel: "gpt-5.4-mini", Enabled: true},
		{ID: "sonnet", Provider: "anthropic", UpstreamModel: "claude-sonnet-4-5", Enabled: true},
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	result, err := checkBackup(context.Background(), vendor, "sk-or-inference", "", client, models)
	if err != nil || catalogReads != 1 || !result.Models[0].Available || !result.Models[1].Available {
		t.Fatalf("check %+v %v reads=%d", result.Models, err, catalogReads)
	}
	bomb = true
	if _, err = checkBackup(context.Background(), vendor, "sk-or-inference", "", client, models); err == nil || !strings.Contains(err.Error(), "oversized") {
		t.Fatalf("oversized compressed catalog accepted: %v", err)
	}
}

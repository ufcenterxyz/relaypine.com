package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/relay"
	"relaypine.com/gateway/internal/store"
)

// Pointer fields distinguish an omitted setting, which keeps the saved value,
// from an explicit one. proxy_id is always authoritative: empty means direct.
type backupInput struct {
	Vendor  string `json:"vendor"`
	Secret  string `json:"secret"`
	ProxyID string `json:"proxy_id"`
	// ManagementKey replaces the saved vendor management key when nonempty.
	// RemoveManagementKey deletes it; otherwise the saved key is kept.
	ManagementKey       string `json:"management_key"`
	RemoveManagementKey bool   `json:"remove_management_key"`
	Enabled             *bool  `json:"enabled"`
	Priority            *int   `json:"priority"`
	RPM                 *int   `json:"rpm"`
	TPM                 *int   `json:"tpm"`
	MaxConcurrency      *int   `json:"max_concurrency"`
}

func validSecretInput(secret string) bool {
	return len(secret) <= 4096 && !strings.ContainsAny(secret, "\r\n\x00")
}

// savedBackup returns the stored backup credential of a vendor, including its
// decrypted key for server-side use only.
func (s *Server) savedBackup(ctx context.Context, vendor string) (domain.Credential, bool, error) {
	credentials, err := s.Store.ListCredentials(ctx)
	if err != nil {
		return domain.Credential{}, false, err
	}
	for _, c := range credentials {
		if c.ID == domain.BackupCredentialID(vendor) && c.EndpointID == domain.BackupEndpointID(vendor) {
			return c, true, nil
		}
	}
	return domain.Credential{}, false, nil
}

// The parent admin dispatcher has already enforced mailbox-authenticated
// superadministrator access, POST, and the console's exact Origin.
func (s *Server) saveBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var input backupInput
	if read(r, &input) != nil || !validSecretInput(input.Secret) || !validSecretInput(input.ManagementKey) || len(input.ProxyID) > 64 {
		fail(w, 400, "invalid_backup", "Choose OpenRouter or ZenMux and enter a valid API key.")
		return
	}
	vendor, ok := domain.FindBackupVendor(input.Vendor)
	if !ok {
		fail(w, 400, "invalid_backup", "Choose OpenRouter or ZenMux and enter a valid API key.")
		return
	}
	var management *string
	if key := strings.TrimSpace(input.ManagementKey); key != "" {
		management = &key
	} else if input.RemoveManagementKey {
		management = new(string)
	}
	c, exists, err := s.savedBackup(ctx, vendor.ID)
	if err != nil {
		s.report(w, err)
		return
	}
	enabled := c.Enabled
	if !exists {
		defaults := domain.DefaultCredential(domain.Endpoint{ID: domain.BackupEndpointID(vendor.ID), Name: vendor.Name})
		c, enabled = domain.Credential{RPM: defaults.RPM, TPM: defaults.TPM, MaxConcurrency: defaults.MaxConcurrency}, true
	}
	c.Secret = strings.TrimSpace(input.Secret)
	if !exists && c.Secret == "" {
		fail(w, 400, "api_key_required", "Enter the "+vendor.Name+" API key.")
		return
	}
	c.ProxyID = input.ProxyID
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.Priority != nil {
		c.Priority = *input.Priority
	}
	if input.RPM != nil {
		c.RPM = *input.RPM
	}
	if input.TPM != nil {
		c.TPM = *input.TPM
	}
	if input.MaxConcurrency != nil {
		c.MaxConcurrency = *input.MaxConcurrency
	}
	// A backup has no local monetary cap; the vendor account or key limit is
	// the spending boundary. Usage still records upstream cost estimates.
	c.BudgetMicros = 0
	if c.RPM < 1 || c.RPM > 100000 || c.TPM < 1 || c.TPM > 100000000 || c.MaxConcurrency < 1 || c.MaxConcurrency > 1000 || c.Priority < 0 || c.Priority > 1000 {
		fail(w, 400, "invalid_backup", "Enter capacity limits and an order within the supported ranges.")
		return
	}
	saved, err := s.Store.SaveBackup(ctx, vendor.ID, c, enabled, management)
	if s.Redis != nil {
		// A new key, exit or management key changes what the balance shows.
		_ = s.Redis.Del(context.Background(), backupBalanceKey(vendor.ID)).Err()
	}
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(w, 409, "backup_conflict", "An upstream with the reserved backup ID already exists. Rename it before adding this backup.")
			return
		}
		s.report(w, err)
		return
	}
	saved.Secret = ""
	write(w, 200, saved)
}

// checkBackup verifies a backup key and reports which RelayPine models the
// vendor serves. It is read-only and never runs paid inference.
func (s *Server) checkBackup(w http.ResponseWriter, r *http.Request, user domain.User) {
	ctx := r.Context()
	var input struct {
		Vendor        string `json:"vendor"`
		Secret        string `json:"secret"`
		ProxyID       string `json:"proxy_id"`
		ManagementKey string `json:"management_key"`
	}
	if read(r, &input) != nil || !validSecretInput(input.Secret) || !validSecretInput(input.ManagementKey) || len(input.ProxyID) > 64 {
		fail(w, 400, "invalid_backup", "Choose OpenRouter or ZenMux and enter a valid API key.")
		return
	}
	vendor, ok := domain.FindBackupVendor(input.Vendor)
	if !ok {
		fail(w, 400, "invalid_backup", "Choose OpenRouter or ZenMux and enter a valid API key.")
		return
	}
	if s.Redis == nil {
		fail(w, 503, "discovery_unavailable", "Checks are temporarily unavailable. Try again later.")
		return
	}
	// Shares the discovery budget, so checks cannot become an unrestricted
	// authenticated network probe.
	count, err := s.Redis.Eval(ctx, `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`, []string{"rp:discovery:" + user.ID}).Int()
	if err != nil {
		fail(w, 503, "discovery_unavailable", "Checks are temporarily unavailable. Try again later.")
		return
	}
	if count > 10 {
		rateLimited(w, 60)
		return
	}
	secret := strings.TrimSpace(input.Secret)
	if secret == "" {
		saved, exists, err := s.savedBackup(ctx, vendor.ID)
		if err != nil {
			s.report(w, err)
			return
		}
		if !exists {
			fail(w, 400, "api_key_required", "Enter the "+vendor.Name+" API key.")
			return
		}
		secret = saved.Secret
	}
	// An empty management key checks the saved one, if any.
	managementKey := strings.TrimSpace(input.ManagementKey)
	if managementKey == "" {
		if managementKey, err = s.Store.BackupManagementKey(ctx, vendor.ID); err != nil {
			s.report(w, err)
			return
		}
	}
	proxyURL := ""
	if input.ProxyID != "" {
		proxies, err := s.Store.ListProxies(ctx)
		if err != nil {
			s.report(w, err)
			return
		}
		for _, proxy := range proxies {
			if proxy.ID == input.ProxyID {
				if !proxy.Enabled {
					fail(w, 400, "proxy_disabled", "Enable the selected proxy or choose another exit before checking this key.")
					return
				}
				proxyURL = proxy.URL
			}
		}
		if proxyURL == "" {
			s.report(w, store.ErrNotFound)
			return
		}
	}
	models, err := s.Store.ListModels(ctx)
	if err != nil {
		s.report(w, err)
		return
	}
	result, err := relay.CheckBackup(ctx, vendor.ID, secret, managementKey, proxyURL, s.AllowPrivate, models)
	if err != nil {
		var problem *relay.DiscoveryError
		if errors.As(err, &problem) {
			fail(w, problem.Status, problem.Code, problem.Message)
		} else {
			fail(w, 502, "backup_check_failed", "The key could not be checked. Try again later.")
		}
		return
	}
	write(w, 200, result)
}

func backupBalanceKey(vendor string) string { return "rp:backup-balance:" + vendor }

// Balances, and failures to read them, are cached for a minute so opening the
// console does not call the vendors on every view: ZenMux allows only a few
// balance reads per minute. A refresh bypasses the cache under the check budget.
const backupBalanceTTL = time.Minute

func (s *Server) fetchBackupBalance(ctx context.Context, vendor, secret, managementKey, proxyURL string) relay.BackupBalance {
	if s.BackupBalanceFetcher != nil {
		return s.BackupBalanceFetcher(ctx, vendor, secret, managementKey, proxyURL)
	}
	return relay.FetchBackupBalance(ctx, vendor, secret, managementKey, proxyURL, s.AllowPrivate)
}

// backupBalances returns the latest balance of every saved backup, enabled
// or not. Each vendor is read through the backup's own egress.
func (s *Server) backupBalances(w http.ResponseWriter, r *http.Request, user domain.User) {
	ctx := r.Context()
	var input struct {
		Refresh bool `json:"refresh"`
	}
	if read(r, &input) != nil {
		fail(w, 400, "invalid_request", "The request could not be completed. Check the values and try again.")
		return
	}
	if input.Refresh {
		if s.Redis == nil {
			fail(w, 503, "discovery_unavailable", "Checks are temporarily unavailable. Try again later.")
			return
		}
		count, err := s.Redis.Eval(ctx, `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`, []string{"rp:discovery:" + user.ID}).Int()
		if err != nil {
			fail(w, 503, "discovery_unavailable", "Checks are temporarily unavailable. Try again later.")
			return
		}
		if count > 10 {
			rateLimited(w, 60)
			return
		}
	}
	endpoints, err := s.Store.ListEndpoints(ctx)
	if err != nil {
		s.report(w, err)
		return
	}
	credentials, err := s.Store.ListCredentials(ctx)
	if err != nil {
		s.report(w, err)
		return
	}
	proxies, err := s.Store.ListProxies(ctx)
	if err != nil {
		s.report(w, err)
		return
	}
	type job struct {
		vendor, secret, managementKey, proxyURL, problem string
	}
	jobs := []job{}
	for _, e := range endpoints {
		if !e.IsBackup() {
			continue
		}
		for _, c := range credentials {
			if c.EndpointID != e.ID {
				continue
			}
			j := job{vendor: e.Vendor, secret: c.Secret}
			if j.managementKey, err = s.Store.BackupManagementKey(ctx, e.Vendor); err != nil {
				s.report(w, err)
				return
			}
			if c.ProxyID != "" {
				j.problem = "The backup's egress proxy is disabled or missing."
				for _, p := range proxies {
					if p.ID == c.ProxyID && p.Enabled {
						j.proxyURL, j.problem = p.URL, ""
					}
				}
			}
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].vendor < jobs[k].vendor })
	results := make([]relay.BackupBalance, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		if j.problem != "" {
			results[i] = relay.BackupBalance{Vendor: j.vendor, ManagementKey: j.managementKey != "", CheckedAt: time.Now().UTC().Format(time.RFC3339), Error: j.problem}
			continue
		}
		if !input.Refresh && s.Redis != nil {
			if raw, err := s.Redis.Get(ctx, backupBalanceKey(j.vendor)).Bytes(); err == nil && json.Unmarshal(raw, &results[i]) == nil {
				continue
			}
		}
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			results[i] = s.fetchBackupBalance(ctx, j.vendor, j.secret, j.managementKey, j.proxyURL)
			if s.Redis == nil {
				return
			}
			if raw, err := json.Marshal(results[i]); err == nil {
				_ = s.Redis.Set(context.Background(), backupBalanceKey(j.vendor), raw, backupBalanceTTL).Err()
			}
		}(i, j)
	}
	wg.Wait()
	write(w, 200, map[string]any{"balances": results})
}

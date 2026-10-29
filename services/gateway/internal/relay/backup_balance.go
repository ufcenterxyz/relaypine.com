package relay

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"relaypine.com/gateway/internal/domain"
)

// BackupBalance is a backup vendor's latest balance, read from its own API.
// Amounts are micro-USD.
type BackupBalance struct {
	Vendor string `json:"vendor"`
	// BalanceMicros is the account balance. Vendors expose it only to a
	// management key, never to an inference key.
	BalanceMicros *int64 `json:"balance_micros,omitempty"`
	// KeyLimitRemainingMicros is what the inference key's own credit cap
	// still allows (OpenRouter). It is absent when the key has no cap.
	KeyLimitRemainingMicros *int64 `json:"key_limit_remaining_micros,omitempty"`
	ManagementKey           bool   `json:"management_key"`
	CheckedAt               string `json:"checked_at"`
	// Error is an application-authored reason the balance is unavailable. It
	// never contains a vendor response, URL or key.
	Error string `json:"error,omitempty"`
}

// FetchBackupBalance reads a backup's balance through its own egress with
// read-only requests. It never runs inference.
func FetchBackupBalance(ctx context.Context, vendorID, secret, managementKey, proxyURL string, allowPrivate bool) BackupBalance {
	result := BackupBalance{Vendor: vendorID, ManagementKey: managementKey != "", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	vendor, ok := domain.FindBackupVendor(vendorID)
	if !ok {
		result.Error = "Unsupported backup provider."
		return result
	}
	tr, err := transport(proxyURL, allowPrivate)
	if err != nil {
		result.Error = "The backup's proxy configuration is not usable."
		return result
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return fetchBackupBalance(ctx, vendor, secret, managementKey, client)
}

func fetchBackupBalance(ctx context.Context, vendor domain.BackupVendor, secret, managementKey string, client *http.Client) BackupBalance {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := BackupBalance{Vendor: vendor.ID, ManagementKey: managementKey != "", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	// OpenRouter reports the inference key's own credit cap to that key.
	if vendor.ID == "openrouter" && secret != "" {
		var check BackupCheck
		if err := verifyBackupKey(ctx, vendor, secret, client, &check); err != nil {
			result.Error = problemMessage(err)
			return result
		}
		result.KeyLimitRemainingMicros = check.Balance.KeyLimitRemainingMicros
	}
	if managementKey != "" {
		accountBalance(ctx, vendor, managementKey, client, &result)
	}
	return result
}

// accountBalance reads the account balance with a vendor management key.
func accountBalance(ctx context.Context, vendor domain.BackupVendor, key string, client *http.Client, result *BackupBalance) {
	unavailable := vendor.Name + " did not return the account balance. Try again later."
	var status int
	var err error
	var balance float64
	var known bool
	switch vendor.ID {
	case "openrouter":
		var credits struct {
			Data *struct {
				TotalCredits *float64 `json:"total_credits"`
				TotalUsage   *float64 `json:"total_usage"`
			} `json:"data"`
		}
		status, err = backupGet(ctx, client, vendor.OpenAIBaseURL+"/credits", key, "bearer", &credits)
		if err == nil && status >= 200 && status < 300 && credits.Data != nil && credits.Data.TotalCredits != nil && credits.Data.TotalUsage != nil {
			balance, known = *credits.Data.TotalCredits-*credits.Data.TotalUsage, true
		}
	case "zenmux":
		var payg struct {
			Success bool `json:"success"`
			Data    *struct {
				Currency     string   `json:"currency"`
				TotalCredits *float64 `json:"total_credits"`
			} `json:"data"`
		}
		status, err = backupGet(ctx, client, vendor.OpenAIBaseURL+"/management/payg/balance", key, "bearer", &payg)
		if err == nil && status >= 200 && status < 300 && payg.Success && payg.Data != nil && payg.Data.TotalCredits != nil && (payg.Data.Currency == "" || payg.Data.Currency == "usd") {
			balance, known = *payg.Data.TotalCredits, true
		}
	default:
		result.Error = unavailable
		return
	}
	switch {
	case err != nil:
		result.Error = problemMessage(err)
	case status == 401 || status == 403:
		result.Error = vendor.Name + " rejected the management key. Use a management key from the same account."
	case status == 422 || status == 429:
		result.Error = vendor.Name + " rate-limited the balance check. Try again in a minute."
	case !known:
		result.Error = unavailable
	default:
		if micros, ok := usdMicros(balance); ok {
			result.BalanceMicros = &micros
		} else {
			result.Error = unavailable
		}
	}
}

func usdMicros(v float64) (int64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < -1e9 || v > 1e9 {
		return 0, false
	}
	return int64(math.Round(v * 1e6)), true
}

func problemMessage(err error) string {
	var problem *DiscoveryError
	if errors.As(err, &problem) {
		return problem.Message
	}
	return "The balance could not be read. Try again later."
}

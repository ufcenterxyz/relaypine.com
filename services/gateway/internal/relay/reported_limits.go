package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
)

// Official upstreams state a key's per-minute rate limits on their responses:
// OpenAI in x-ratelimit-limit-requests and -tokens for the model called, and
// Anthropic in anthropic-ratelimit-requests-limit, -tokens-limit,
// -input-tokens-limit and -output-tokens-limit. The gateway keeps the latest
// values per credential and model so operators can set each key's limits to
// its real tier instead of guessing.
const (
	reportedLimitsTTL     = 30 * 24 * time.Hour
	reportedLimitsRefresh = 10 * time.Minute
	maxReportedLimit      = 1_000_000_000_000
)

func reportedLimitsKey(credential string) string { return "rp:reported-limits:" + credential }

// reportedLimit reads the limits in an official upstream's response headers.
func reportedLimit(provider string, header http.Header) (domain.ReportedLimit, bool) {
	read := func(name string) int64 {
		v, err := strconv.ParseInt(strings.TrimSpace(header.Get(name)), 10, 64)
		if err != nil || v <= 0 || v > maxReportedLimit {
			return 0
		}
		return v
	}
	var l domain.ReportedLimit
	switch provider {
	case "openai":
		l.Requests, l.Tokens = read("X-Ratelimit-Limit-Requests"), read("X-Ratelimit-Limit-Tokens")
	case "anthropic":
		l.Requests, l.Tokens = read("Anthropic-Ratelimit-Requests-Limit"), read("Anthropic-Ratelimit-Tokens-Limit")
		l.InputTokens, l.OutputTokens = read("Anthropic-Ratelimit-Input-Tokens-Limit"), read("Anthropic-Ratelimit-Output-Tokens-Limit")
	}
	return l, l.Requests > 0 || l.Tokens > 0 || l.InputTokens > 0 || l.OutputTokens > 0
}

// limitRecorder stores reported limits in Redis. Unchanged values are written
// at most every ten minutes, and never on the request path.
type limitRecorder struct {
	client *redis.Client
	mu     sync.Mutex
	last   map[string]domain.ReportedLimit
}

func (r *limitRecorder) note(credential, model string, l domain.ReportedLimit) {
	if r == nil || r.client == nil || credential == "" || model == "" {
		return
	}
	l.Model, l.ObservedAt = model, time.Now().UTC()
	key := credential + "\x00" + model
	r.mu.Lock()
	prev, seen := r.last[key]
	if seen && sameReportedLimit(prev, l) && l.ObservedAt.Sub(prev.ObservedAt) < reportedLimitsRefresh {
		r.mu.Unlock()
		return
	}
	r.last[key] = l
	r.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		raw, err := json.Marshal(l)
		if err != nil {
			return
		}
		pipe := r.client.TxPipeline()
		pipe.HSet(ctx, reportedLimitsKey(credential), model, raw)
		pipe.Expire(ctx, reportedLimitsKey(credential), reportedLimitsTTL)
		_, _ = pipe.Exec(ctx)
	}()
}

func sameReportedLimit(a, b domain.ReportedLimit) bool {
	return a.Requests == b.Requests && a.Tokens == b.Tokens && a.InputTokens == b.InputTokens && a.OutputTokens == b.OutputTokens
}

// ReportedLimits returns the latest limits upstreams stated for a credential,
// one per model, sorted by model.
func ReportedLimits(ctx context.Context, client *redis.Client, credential string) ([]domain.ReportedLimit, error) {
	values, err := client.HGetAll(ctx, reportedLimitsKey(credential)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]domain.ReportedLimit, 0, len(values))
	for model, raw := range values {
		var l domain.ReportedLimit
		if json.Unmarshal([]byte(raw), &l) != nil || l.Model != model {
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

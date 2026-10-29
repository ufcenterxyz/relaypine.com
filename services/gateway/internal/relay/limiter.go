package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
)

var errCapacity = errors.New("route capacity exhausted")

type limits struct {
	scope                 string
	rpm, tpm, concurrency int
}
type routeLimiter interface {
	load(context.Context, []limits) (float64, error)
	// acquire admits a request in every bucket and returns the one-minute
	// window its estimated tokens were charged to.
	acquire(context.Context, string, []limits, int64) (int64, error)
	release(context.Context, string, []limits) error
	// adjust moves the tokens charged to a window by delta, never below zero,
	// so an estimate becomes the tokens a request actually used.
	adjust(context.Context, []limits, int64, int64) error
	cool(context.Context, []limits, time.Duration) error
}
type redisLimiter struct{ client *redis.Client }

func redisKey(scope string) string {
	h := sha256.Sum256([]byte(scope))
	return "relay:" + hex.EncodeToString(h[:16])
}
func window(now time.Time) int64 { return now.Unix() / 60 }

func tpmKey(scope string, w int64) string {
	return redisKey(scope) + ":tpm:" + strconv.FormatInt(w, 10)
}

func bucketKeys(scope string, now time.Time) []string {
	k := redisKey(scope)
	minute := strconv.FormatInt(window(now), 10)
	return []string{k + ":rpm:" + minute, k + ":tpm:" + minute, k + ":active", k + ":cool"}
}

// All tenant, access key, upstream key and shared-group checks and increments
// execute in one transaction. Rejected candidates consume no capacity. A token
// bucket admits while its minute is below its limit, so a request whose
// estimate alone exceeds the limit still runs on an idle key; it then holds the
// minute until settlement replaces the estimate with the tokens it used.
var acquireScript = redis.NewScript(`
local now=tonumber(ARGV[1]); local expires=tonumber(ARGV[2]); local id=ARGV[3]; local tokens=tonumber(ARGV[4])
for i=1,#KEYS,4 do
  local n=(i-1)/4; local a=5+n*3
  redis.call('ZREMRANGEBYSCORE',KEYS[i+2],'-inf',now)
  if tonumber(redis.call('GET',KEYS[i+3]) or '0')>now then return 0 end
  local rpm=tonumber(ARGV[a]); local tpm=tonumber(ARGV[a+1]); local conc=tonumber(ARGV[a+2])
  if rpm>0 and tonumber(redis.call('GET',KEYS[i]) or '0')+1>rpm then return 0 end
  if tpm>0 and tonumber(redis.call('GET',KEYS[i+1]) or '0')>=tpm then return 0 end
  if conc>0 and redis.call('ZCARD',KEYS[i+2])+1>conc then return 0 end
end
for i=1,#KEYS,4 do
  redis.call('INCR',KEYS[i]); redis.call('EXPIRE',KEYS[i],120)
  redis.call('INCRBY',KEYS[i+1],tokens); redis.call('EXPIRE',KEYS[i+1],120)
  redis.call('ZADD',KEYS[i+2],expires,id); redis.call('EXPIRE',KEYS[i+2],480)
end
return 1`)

func (l *redisLimiter) acquire(ctx context.Context, id string, buckets []limits, tokens int64) (int64, error) {
	if l.client == nil {
		return 0, errors.New("limiter unavailable")
	}
	now := time.Now()
	keys := []string{}
	args := []any{now.UnixMilli(), now.Add(7 * time.Minute).UnixMilli(), id, tokens}
	for _, b := range buckets {
		keys = append(keys, bucketKeys(b.scope, now)...)
		args = append(args, b.rpm, b.tpm, b.concurrency)
	}
	n, err := acquireScript.Run(ctx, l.client, keys, args...).Int()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, errCapacity
	}
	return window(now), nil
}

// A window whose counter has expired is left alone: its minute is over and no
// later admission reads it. A refund never takes a counter below zero.
var adjustScript = redis.NewScript(`
local delta=tonumber(ARGV[1])
for i=1,#KEYS do
  local value=redis.call('GET',KEYS[i])
  if value then
    local next=tonumber(value)+delta
    if next<0 then next=0 end
    redis.call('SET',KEYS[i],next,'KEEPTTL')
  end
end
return 1`)

func (l *redisLimiter) adjust(ctx context.Context, buckets []limits, w, delta int64) error {
	if l.client == nil {
		return errors.New("limiter unavailable")
	}
	if delta == 0 || len(buckets) == 0 {
		return nil
	}
	keys := make([]string, len(buckets))
	for i, b := range buckets {
		keys[i] = tpmKey(b.scope, w)
	}
	return adjustScript.Run(ctx, l.client, keys, delta).Err()
}
func (l *redisLimiter) release(ctx context.Context, id string, buckets []limits) error {
	if l.client == nil {
		return errors.New("limiter unavailable")
	}
	pipe := l.client.Pipeline()
	for _, b := range buckets {
		pipe.ZRem(ctx, redisKey(b.scope)+":active", id)
	}
	_, err := pipe.Exec(ctx)
	return err
}
func (l *redisLimiter) cool(ctx context.Context, buckets []limits, d time.Duration) error {
	if l.client == nil {
		return errors.New("limiter unavailable")
	}
	if d < time.Second {
		d = time.Second
	}
	if d > 24*time.Hour {
		d = 24 * time.Hour
	}
	// Concurrent cooldowns only extend an existing deadline.
	script := redis.NewScript(`local old=tonumber(redis.call('GET',KEYS[1]) or '0');local next=tonumber(ARGV[1]);if next>old then redis.call('SET',KEYS[1],next,'PX',ARGV[2]) end;return 1`)
	for _, b := range buckets {
		if err := script.Run(ctx, l.client, []string{redisKey(b.scope) + ":cool"}, time.Now().Add(d).UnixMilli(), d.Milliseconds()).Err(); err != nil {
			return err
		}
	}
	return nil
}
func (l *redisLimiter) load(ctx context.Context, buckets []limits) (float64, error) {
	if l.client == nil {
		return 0, errors.New("limiter unavailable")
	}
	now := time.Now()
	pipe := l.client.Pipeline()
	type item struct {
		b       limits
		r, t, c *redis.StringCmd
		a       *redis.IntCmd
	}
	items := make([]item, 0, len(buckets))
	for _, b := range buckets {
		k := bucketKeys(b.scope, now)
		pipe.ZRemRangeByScore(ctx, k[2], "-inf", fmt.Sprint(now.UnixMilli()))
		items = append(items, item{b, pipe.Get(ctx, k[0]), pipe.Get(ctx, k[1]), pipe.Get(ctx, k[3]), pipe.ZCard(ctx, k[2])})
	}
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	score := 0.0
	for _, v := range items {
		r, _ := v.r.Int64()
		t, _ := v.t.Int64()
		c, _ := v.c.Int64()
		a, err := v.a.Result()
		if err != nil {
			return 0, err
		}
		if c > now.UnixMilli() {
			return 1e9, nil
		}
		for _, p := range [][2]int64{{r, int64(v.b.rpm)}, {t, int64(v.b.tpm)}, {a, int64(v.b.concurrency)}} {
			if p[1] > 0 {
				ratio := float64(p[0]) / float64(p[1])
				if ratio > score {
					score = ratio
				}
			}
		}
	}
	return score, nil
}

func groupLimits(c domain.Credential, all []domain.Credential) limits {
	b := limits{scope: "group:" + c.QuotaGroup, rpm: c.RPM, tpm: c.TPM, concurrency: c.MaxConcurrency}
	for _, other := range all {
		if other.Enabled && other.QuotaGroup == c.QuotaGroup {
			b.rpm = minPositive(b.rpm, other.RPM)
			b.tpm = minPositive(b.tpm, other.TPM)
			b.concurrency = minPositive(b.concurrency, other.MaxConcurrency)
		}
	}
	return b
}
func minPositive(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

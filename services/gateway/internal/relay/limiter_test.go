package relay

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
)

func TestPublicTargets(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "172.16.1.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "192.0.2.2", "198.18.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "64:ff9b::7f00:1"} {
		if publicIP(netip.MustParseAddr(address)) {
			t.Errorf("unsafe address allowed %s", address)
		}
	}
	for _, address := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(address)) {
			t.Errorf("public address refused %s", address)
		}
	}
}

func TestBasePath(t *testing.T) {
	for _, tc := range []struct{ base, path, want string }{{"https://example.com", "/v1/messages", "https://example.com/v1/messages"}, {"https://example.com/v1/", "/v1/messages", "https://example.com/v1/messages"}, {"https://example.com/api/v1", "/v1/responses", "https://example.com/api/v1/responses"}} {
		got, err := upstreamURL(tc.base, tc.path, false)
		if err != nil || got != tc.want {
			t.Fatalf("%s %s => %s %v", tc.base, tc.path, got, err)
		}
	}
	for _, base := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com?secret=x", "file:///tmp/foo"} {
		if _, err := upstreamURL(base, "/v1/messages", false); err == nil {
			t.Errorf("unsafe base accepted %s", base)
		}
	}
}

func TestSharedGroupUsesConservativeCaps(t *testing.T) {
	a := domain.Credential{Enabled: true, QuotaGroup: "shared", RPM: 100, TPM: 20000, MaxConcurrency: 8}
	b := domain.Credential{Enabled: true, QuotaGroup: "shared", RPM: 20, TPM: 30000, MaxConcurrency: 3}
	g := groupLimits(a, []domain.Credential{a, b})
	if g.rpm != 20 || g.tpm != 20000 || g.concurrency != 3 {
		t.Fatalf("group caps %+v", g)
	}
}

func TestRedisAtomicSharedCapacity(t *testing.T) {
	address := os.Getenv("REDIS_TEST_ADDR")
	if address == "" {
		t.Skip("set REDIS_TEST_ADDR for Redis Lua integration")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	l := &redisLimiter{client: client}
	prefix := "test:" + requestID()
	group := limits{scope: prefix + ":group", rpm: 100, tpm: 100000, concurrency: 3}
	tenant := limits{scope: prefix + ":tenant", rpm: 100, concurrency: 20}
	scopes := []string{group.scope, tenant.scope, prefix + ":a", prefix + ":b", prefix + ":tokens"}
	defer func() {
		for _, scope := range scopes {
			keys := bucketKeys(scope, time.Now())
			_ = client.Del(ctx, keys...).Err()
		}
	}()
	var accepted atomic.Int64
	var wg sync.WaitGroup
	var lock sync.Mutex
	ids := []string{}
	errorsSeen := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := requestID()
			credential := limits{scope: prefix + ":a", rpm: 60, tpm: 100000, concurrency: 10}
			if i%2 == 0 {
				credential.scope = prefix + ":b"
			}
			_, err := l.acquire(ctx, id, []limits{tenant, credential, group}, 100, 7*time.Minute)
			if err == nil {
				accepted.Add(1)
				lock.Lock()
				ids = append(ids, id)
				lock.Unlock()
			} else if !errors.Is(err, errCapacity) {
				errorsSeen <- err
			}
		}(i)
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	if accepted.Load() != 3 {
		t.Fatalf("accepted %d, expected shared concurrency limit 3", accepted.Load())
	}
	keys := bucketKeys(group.scope, time.Now())
	rpm, _ := client.Get(ctx, keys[0]).Int64()
	if rpm != 3 {
		t.Fatalf("rejected candidates consumed RPM: %d", rpm)
	}
	load, err := l.load(ctx, []limits{group})
	if err != nil || load != 1 {
		t.Fatalf("normalized load %f %v", load, err)
	}
	for _, id := range ids {
		if err := l.release(ctx, id, []limits{tenant, group, {scope: prefix + ":a"}, {scope: prefix + ":b"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.cool(ctx, []limits{group}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := l.acquire(ctx, requestID(), []limits{group}, 1, 7*time.Minute); !errors.Is(err, errCapacity) {
		t.Fatalf("cooldown ignored: %v", err)
	}
	// A token bucket admits while its minute is below the limit: the request
	// that crosses it runs, and the next one waits.
	tokens := limits{scope: prefix + ":tokens", tpm: 20}
	if _, err := l.acquire(ctx, requestID(), []limits{tokens}, 13, 7*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := l.acquire(ctx, requestID(), []limits{tokens}, 8, 7*time.Minute); err != nil {
		t.Fatalf("request below the limit refused: %v", err)
	}
	if _, err := l.acquire(ctx, requestID(), []limits{tokens}, 1, 7*time.Minute); !errors.Is(err, errCapacity) {
		t.Fatalf("TPM limit ignored: %v", err)
	}
	// Expired process leases are removed before admission; crashed gateways cannot
	// permanently lock the shared group.
	_ = client.Del(ctx, keys[3]).Err()
	_ = client.ZAdd(ctx, keys[2], redis.Z{Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: "expired"}).Err()
	if _, err := l.acquire(ctx, requestID(), []limits{group}, 1, 7*time.Minute); err != nil {
		t.Fatalf("stale lease blocked admission: %v", err)
	}
}

// A streamed Flex request holds its concurrency slot for its whole 15
// minutes: its lease outlives the request, and the set of running requests is
// never given a shorter life by the five-minute requests admitted after it.
func TestRedisFlexLeaseOutlivesShorterLeases(t *testing.T) {
	address := os.Getenv("REDIS_TEST_ADDR")
	if address == "" {
		t.Skip("set REDIS_TEST_ADDR for Redis Lua integration")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	l := &redisLimiter{client: client}
	flexBucket := limits{scope: "test:" + requestID() + ":flex", concurrency: 2}
	standardBucket := limits{scope: "test:" + requestID() + ":standard", concurrency: 2}
	keys := append(bucketKeys(flexBucket.scope, time.Now()), bucketKeys(standardBucket.scope, time.Now())...)
	defer client.Del(ctx, keys...)
	long := requestID()
	if _, err := l.acquire(ctx, long, []limits{flexBucket}, 1, flexStreamTimeout+leaseMargin); err != nil {
		t.Fatal(err)
	}
	if _, err := l.acquire(ctx, requestID(), []limits{flexBucket}, 1, requestTimeout+leaseMargin); err != nil {
		t.Fatal(err)
	}
	active := keys[2]
	if ttl, err := client.TTL(ctx, active).Result(); err != nil || ttl < 17*time.Minute || ttl > 18*time.Minute {
		t.Fatalf("running set lives %s: %v", ttl, err)
	}
	score, err := client.ZScore(ctx, active, long).Result()
	if left := time.Until(time.UnixMilli(int64(score))); err != nil || left < 16*time.Minute || left > 17*time.Minute {
		t.Fatalf("Flex lease ends in %s: %v", left, err)
	}
	if _, err := l.acquire(ctx, requestID(), []limits{flexBucket}, 1, requestTimeout+leaseMargin); !errors.Is(err, errCapacity) {
		t.Fatalf("a full bucket admitted a third request: %v", err)
	}
	// A five-minute request keeps the seven-minute lease and the eight-minute
	// set it always had.
	short := requestID()
	if _, err := l.acquire(ctx, short, []limits{standardBucket}, 1, requestTimeout+leaseMargin); err != nil {
		t.Fatal(err)
	}
	if ttl, err := client.TTL(ctx, keys[6]).Result(); err != nil || ttl < 7*time.Minute+50*time.Second || ttl > 8*time.Minute {
		t.Fatalf("standard running set lives %s: %v", ttl, err)
	}
	score, err = client.ZScore(ctx, keys[6], short).Result()
	if left := time.Until(time.UnixMilli(int64(score))); err != nil || left < 6*time.Minute+50*time.Second || left > 7*time.Minute {
		t.Fatalf("standard lease ends in %s: %v", left, err)
	}
}

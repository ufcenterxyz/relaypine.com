package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

func (f *apiFixture) enablePublicRequests(t *testing.T) {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		addr = os.Getenv("REDIS_ADDR")
	}
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR/REDIS_ADDR required for public access integration")
	}
	// Keep fixed-admin limiter counters away from the development service.
	f.s.Redis = redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD"), DB: 15})
	if err := f.s.Redis.Ping(f.ctx).Err(); err != nil {
		t.Fatal(err)
	}
	f.remote = "[fd00:" + f.prefix[5:9] + ":" + f.prefix[9:13] + "::1]:12345"
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	adminCounters := []string{"rp:otp-send:email:" + hash(store.SuperAdminEmail), "rp:otp-verify:email:" + hash(store.SuperAdminEmail), challengeKey(store.SuperAdminEmail), cooldownKey(store.SuperAdminEmail), "rp:otp:{" + emailHash(store.SuperAdminEmail) + "}:hour"}
	f.s.Redis.Del(f.ctx, adminCounters...)
	t.Cleanup(func() { f.s.Redis.Del(context.Background(), adminCounters...); f.s.Redis.Close() })
}

func TestLegacyAuthenticationRoutesCannotBypassVerification(t *testing.T) {
	f := newAPIFixture(t)
	for _, path := range []string{"/api/waitlist", "/api/auth/register", "/api/auth/login"} {
		for _, email := range []string{store.SuperAdminEmail, f.prefix + "new@example.test"} {
			w := f.call(t, "POST", path, nil, f.s.AppURL, map[string]string{"email": email, "password": "test-password-strong"})
			expectStatus(t, w, 410)
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("legacy authentication created a session")
			}
		}
	}
}

func TestLegacyAccountsAndCredentialsHaveNoPrivateAccess(t *testing.T) {
	f := newAPIFixture(t)
	for i, user := range []domain.User{f.customer, f.other} {
		cookie := []*http.Cookie{f.customerCookie, f.otherCookie}[i]
		key, secret := f.legacyKey(t, user)
		if _, err := f.db.AuthenticateKey(f.ctx, secret); !errors.Is(err, store.ErrUnauthorized) {
			t.Fatalf("legacy key authenticated: %v", err)
		}
		if _, err := f.db.CreateSession(f.ctx, user.ID); !errors.Is(err, store.ErrUnauthorized) {
			t.Fatalf("legacy session created: %v", err)
		}
		if _, _, err := f.db.CreateKey(f.ctx, user, "forbidden", nil, 1); !errors.Is(err, store.ErrUnauthorized) {
			t.Fatalf("legacy key created: %v", err)
		}
		forged := user
		forged.Email, forged.Operator = store.SuperAdminEmail, true
		if _, _, err := f.db.CreateKey(f.ctx, forged, "forged identity", nil, 1); err == nil {
			t.Fatal("forged in-memory administrator created a key")
		}
		if _, err := f.db.Snapshot(f.ctx, forged); err == nil {
			t.Fatal("forged in-memory administrator read private state")
		}
		for _, tc := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/api/state", nil}, {"GET", "/api/auth/me", nil},
			{"POST", "/api/keys", map[string]any{"name": "forbidden", "rpm": 10}},
			{"DELETE", "/api/keys/" + key.ID, nil},
			{"POST", "/api/admin/credits", map[string]any{"tenant_id": user.TenantID, "amount_micros": 100, "reason": "forbidden adjustment", "idempotency_key": f.prefix}},
		} {
			expectStatus(t, f.call(t, tc.method, tc.path, cookie, f.s.AppURL, tc.body), 401)
		}
	}
}

func TestBootstrapPromotionResetsUnverifiedCredentialsAndPreservesHistory(t *testing.T) {
	f := newAPIFixture(t)
	key, secret, err := f.db.CreateKey(f.ctx, f.operator, "Before policy migration", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	oldHash, err := bcrypt.GenerateFromPassword([]byte("old-unverified-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.DB.ExecContext(f.ctx, "UPDATE users SET operator=FALSE,password_hash=? WHERE id=?", oldHash, f.operator.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Credit(f.ctx, f.other.TenantID, 12345, "legacy-credit:"+f.prefix, "Existing historical adjustment"); err != nil {
		t.Fatal(err)
	}
	_, legacySecret := f.legacyKey(t, f.other)
	admin, err := f.db.EnsureSuperAdmin(f.ctx, "Verified administrator")
	if err != nil {
		t.Fatal(err)
	}
	if admin.ID != f.operator.ID || !store.IsSuperAdmin(admin) {
		t.Fatal("bootstrap replaced identity or failed promotion")
	}
	if _, err = f.db.EmailAdministrator(f.ctx, store.SuperAdminEmail); err != nil {
		t.Fatal(err)
	}
	var passwordHash string
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT password_hash FROM users WHERE id=?", admin.ID).Scan(&passwordHash); err != nil || passwordHash != "!email-otp" {
		t.Fatal("old password credential survived migration")
	}
	if _, err = f.db.SessionUser(f.ctx, f.operatorCookie.Value); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("old administrator session survived promotion")
	}
	for _, token := range []string{secret, legacySecret} {
		if _, err = f.db.AuthenticateKey(f.ctx, token); !errors.Is(err, store.ErrUnauthorized) {
			t.Fatal("old access key survived promotion")
		}
	}
	var enabled bool
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT enabled FROM access_keys WHERE id=?", key.ID).Scan(&enabled); err != nil || enabled {
		t.Fatal("unverified access key not explicitly disabled")
	}
	var operators, waitlisted int
	var balance int64
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM users WHERE operator=TRUE").Scan(&operators); err != nil {
		t.Fatal(err)
	}
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM waitlist_entries WHERE email IN (?,?)", f.customer.Email, f.other.Email).Scan(&waitlisted); err != nil {
		t.Fatal(err)
	}
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros FROM wallets WHERE tenant_id=?", f.other.TenantID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if operators != 1 || waitlisted != 2 || balance != 12345 {
		t.Fatalf("migration operators=%d waitlist=%d historical balance=%d", operators, waitlisted, balance)
	}
	// Repeated startup is idempotent and does not sign out a verified operator.
	token, err := f.db.CreateSession(f.ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.EnsureSuperAdmin(f.ctx, "Verified administrator"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.SessionUser(f.ctx, token); err != nil {
		t.Fatal("idempotent bootstrap invalidated a verified session")
	}
}

func TestRemovedPaymentsRoutesReturnNotFound(t *testing.T) {
	// No DB, Redis or payment object is needed to reject removed capabilities.
	s := &Server{Relay: http.NotFoundHandler(), AppURL: "https://relaypine.test"}
	f := &apiFixture{s: s, h: s.Handler()}
	for _, path := range []string{"/api/billing/checkout", "/api/admin/refunds", "/api/payments/stripe/webhook"} {
		for _, origin := range []string{"", s.AppURL} {
			expectStatus(t, f.call(t, "POST", path, nil, origin, map[string]any{}), 404)
		}
	}
}

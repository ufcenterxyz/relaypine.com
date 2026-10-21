package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
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
	// Keep integration administrator limiter counters away from the development service.
	f.s.Redis = redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD"), DB: 15})
	if err := f.s.Redis.Ping(f.ctx).Err(); err != nil {
		t.Fatal(err)
	}
	f.remote = "[fd00:" + f.prefix[5:9] + ":" + f.prefix[9:13] + "::1]:12345"
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	adminCounters := []string{"rp:otp-send:email:" + hash(f.db.SuperAdminEmail()), "rp:otp-verify:email:" + hash(f.db.SuperAdminEmail()), challengeKey(f.db.SuperAdminEmail()), cooldownKey(f.db.SuperAdminEmail()), "rp:otp:{" + emailHash(f.db.SuperAdminEmail()) + "}:hour"}
	f.s.Redis.Del(f.ctx, adminCounters...)
	t.Cleanup(func() { f.s.Redis.Del(context.Background(), adminCounters...); f.s.Redis.Close() })
}

func TestLegacyAuthenticationRoutesCannotBypassVerification(t *testing.T) {
	f := newAPIFixture(t)
	for _, path := range []string{"/api/waitlist", "/api/auth/register", "/api/auth/login"} {
		for _, email := range []string{f.db.SuperAdminEmail(), f.prefix + "new@example.test"} {
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
		forged.Email, forged.Operator = f.db.SuperAdminEmail(), true
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
	if admin.ID != f.operator.ID || !f.db.IsSuperAdmin(admin) {
		t.Fatal("bootstrap replaced identity or failed promotion")
	}
	if _, err = f.db.EmailAdministrator(f.ctx, f.db.SuperAdminEmail()); err != nil {
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

func TestAdministratorChangePreservesTenantOwnershipAndRevokesFormerAccess(t *testing.T) {
	f := newAPIFixture(t)
	t.Cleanup(func() {
		f.db.DB.ExecContext(context.Background(), "DELETE FROM waitlist_entries WHERE email=?", f.operator.Email)
	})
	if err := f.db.Credit(f.ctx, f.operator.TenantID, 12345, "owner-change:"+f.prefix, "Existing administrator balance"); err != nil {
		t.Fatal(err)
	}
	_, oldSecret, err := f.db.CreateKey(f.ctx, f.operator, "Former administrator key", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	master := os.Getenv("MASTER_KEY")
	if master == "" {
		master = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	newEmail := f.prefix + "new-owner@example.test"
	config := store.Config{SuperAdminEmail: "  " + strings.ToUpper(newEmail) + "  "}
	replacement, err := store.Open(f.ctx, os.Getenv("MYSQL_TEST_DSN"), master, config)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	config.SuperAdminEmail = "changed-after-open@example.test"
	if replacement.SuperAdminEmail() != newEmail || f.db.SuperAdminEmail() != testAdminEmail {
		t.Fatal("administrator configuration was not normalized and copied per instance")
	}
	// Before bootstrap, the replacement policy cannot authenticate the old
	// administrator simply because that user still has operator=true in MySQL.
	if _, err = replacement.SessionUser(f.ctx, f.operatorCookie.Value); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("new administrator policy accepted a former administrator session")
	}
	admin, err := replacement.EnsureSuperAdmin(f.ctx, "Replacement administrator")
	if err != nil {
		t.Fatal(err)
	}
	f.users = append(f.users, admin)
	if admin.ID == f.operator.ID || admin.TenantID == f.operator.TenantID || admin.Email != newEmail {
		t.Fatal("administrator change reassigned the former user's identity or tenant")
	}
	var priorEmail string
	var priorOperator bool
	var priorBalance, newBalance, priorLedger int64
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT email,operator FROM users WHERE id=?", f.operator.ID).Scan(&priorEmail, &priorOperator); err != nil {
		t.Fatal(err)
	}
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros FROM wallets WHERE tenant_id=?", f.operator.TenantID).Scan(&priorBalance); err != nil {
		t.Fatal(err)
	}
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT balance_micros FROM wallets WHERE tenant_id=?", admin.TenantID).Scan(&newBalance); err != nil {
		t.Fatal(err)
	}
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM ledger_entries WHERE tenant_id=?", f.operator.TenantID).Scan(&priorLedger); err != nil {
		t.Fatal(err)
	}
	if priorEmail != f.operator.Email || priorOperator || priorBalance != 12345 || newBalance != 0 || priorLedger != 1 {
		t.Fatalf("ownership/history changed: old operator=%v old balance=%d new balance=%d old ledger=%d", priorOperator, priorBalance, newBalance, priorLedger)
	}
	for _, policy := range []*store.Store{f.db, replacement} {
		if _, err = policy.SessionUser(f.ctx, f.operatorCookie.Value); !errors.Is(err, store.ErrUnauthorized) {
			t.Fatal("former administrator session survived administrator change")
		}
		if _, err = policy.AuthenticateKey(f.ctx, oldSecret); !errors.Is(err, store.ErrUnauthorized) {
			t.Fatal("former administrator API key survived administrator change")
		}
	}
	if _, err = replacement.EmailAdministrator(f.ctx, newEmail); err != nil {
		t.Fatal("configured replacement was not provisioned")
	}
	if _, err = replacement.EmailAdministrator(f.ctx, f.operator.Email); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("former email remains eligible for administrator sign-in")
	}
	// A second identity which already exists retains its own historical tenant
	// rather than receiving either administrator's tenant or wallet.
	existing, err := store.Open(f.ctx, os.Getenv("MYSQL_TEST_DSN"), master, store.Config{SuperAdminEmail: f.customer.Email})
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	promoted, err := existing.EnsureSuperAdmin(f.ctx, "Existing account administrator")
	if err != nil || promoted.ID != f.customer.ID || promoted.TenantID != f.customer.TenantID {
		t.Fatalf("existing address lost its own tenant: %v", err)
	}
	// This test backfills the original fixture administrator into the waitlist.
	t.Cleanup(func() {
		f.db.DB.ExecContext(context.Background(), "DELETE FROM waitlist_entries WHERE email=?", f.operator.Email)
	})
}

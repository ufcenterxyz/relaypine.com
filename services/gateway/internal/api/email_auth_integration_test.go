package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

type sentCode struct{ email, code string }
type fakeCodeSender struct {
	mu      sync.Mutex
	sent    []sentCode
	failure bool
	hook    func(string, string) error
}

func (s *fakeCodeSender) SendCode(_ context.Context, email, code string) error {
	s.mu.Lock()
	s.sent = append(s.sent, sentCode{email, code})
	failure, hook := s.failure, s.hook
	s.mu.Unlock()
	if hook != nil {
		return hook(email, code)
	}
	if failure {
		return errors.New("delivery failed")
	}
	return nil
}
func (s *fakeCodeSender) last(t *testing.T) sentCode {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		t.Fatal("no code delivered")
	}
	return s.sent[len(s.sent)-1]
}
func emailFixture(t *testing.T) (*apiFixture, *fakeCodeSender) {
	t.Helper()
	f := newAPIFixture(t)
	f.enablePublicRequests(t)
	sender := &fakeCodeSender{}
	var err error
	f.s.EmailAuth, err = NewEmailAuth(f.s.Redis, sender, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return f, sender
}
func issue(t *testing.T, f *apiFixture, email string) {
	t.Helper()
	expectStatus(t, f.call(t, "POST", "/api/auth/code", nil, f.s.AppURL, map[string]string{"email": email, "name": "Verified visitor"}), 202)
}
func verify(t *testing.T, f *apiFixture, email, code string) int {
	t.Helper()
	return f.call(t, "POST", "/api/auth/verify", nil, f.s.AppURL, map[string]string{"email": email, "code": code}).Code
}
func clearCooldown(t *testing.T, f *apiFixture, email string) {
	t.Helper()
	if err := f.s.Redis.Del(f.ctx, cooldownKey(email)).Err(); err != nil {
		t.Fatal(err)
	}
}

func TestEmailOTPAdministratorAndVerifiedWaitlist(t *testing.T) {
	f, sender := emailFixture(t)
	email := f.prefix + "mail@example.test"
	var usersBefore, keysBefore int
	f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM users").Scan(&usersBefore)
	f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM access_keys").Scan(&keysBefore)
	issue(t, f, "  "+strings.ToUpper(email)+"  ")
	if sender.last(t).email != email {
		t.Fatal("delivery address was not normalized")
	}
	var entries int
	f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM waitlist_entries WHERE email=?", email).Scan(&entries)
	if entries != 0 {
		t.Fatal("unverified address joined waitlist")
	}
	code := sender.last(t).code
	stored, err := f.s.Redis.HGetAll(f.ctx, challengeKey(email)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if stored["digest"] == code || stored["code"] != "" || len(stored["digest"]) != 64 {
		t.Fatal("challenge is not keyed-hash protected")
	}
	if ttl := f.s.Redis.TTL(f.ctx, challengeKey(email)).Val(); ttl > 600*time.Second || ttl < 590*time.Second {
		t.Fatalf("unexpected TTL %v", ttl)
	}
	w := f.call(t, "POST", "/api/auth/verify", nil, f.s.AppURL, map[string]string{"email": email, "code": code})
	expectStatus(t, w, 202)
	if len(w.Result().Cookies()) != 0 || !strings.Contains(w.Body.String(), "waitlisted") {
		t.Fatal("ordinary mailbox got session or incorrect status")
	}
	if verify(t, f, email, code) != 401 {
		t.Fatal("OTP replay accepted")
	}
	clearCooldown(t, f, email)
	issue(t, f, email)
	expectStatus(t, f.call(t, "POST", "/api/auth/verify", nil, f.s.AppURL, map[string]string{"email": email, "code": sender.last(t).code}), 202)
	var usersAfter, keysAfter int
	f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM users").Scan(&usersAfter)
	f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM access_keys").Scan(&keysAfter)
	f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM waitlist_entries WHERE email=?", email).Scan(&entries)
	if usersBefore != usersAfter || keysBefore != keysAfter || entries != 1 {
		t.Fatal("waitlist provisioned identity, keys or duplicates")
	}
	issue(t, f, strings.ToUpper(f.db.SuperAdminEmail()))
	w = f.call(t, "POST", "/api/auth/verify", nil, f.s.AppURL, map[string]string{"email": f.db.SuperAdminEmail(), "code": sender.last(t).code})
	expectStatus(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie is missing security attributes")
	}
	state := f.call(t, "GET", "/api/state", cookies[0], "", nil)
	expectStatus(t, state, 200)
	snap := decodeResponse[domain.Snapshot](t, state)
	found := false
	for _, entry := range snap.Waitlist {
		if entry.Email == email {
			found = entry.Name == "Verified visitor"
		}
	}
	if !found {
		t.Fatal("verified waitlist missing from administrator view")
	}
	if verify(t, f, f.db.SuperAdminEmail(), sender.last(t).code) != 401 {
		t.Fatal("administrator code replay succeeded")
	}
}
func TestEmailOTPWrongExpiredAndSupersededCodes(t *testing.T) {
	f, sender := emailFixture(t)
	email := f.prefix + "attempts@example.test"
	issue(t, f, email)
	first := sender.last(t).code
	wrong := "000000"
	if wrong == first {
		wrong = "000001"
	}
	for i := 0; i < 5; i++ {
		if verify(t, f, email, wrong) != 401 {
			t.Fatal("wrong code accepted")
		}
	}
	if verify(t, f, email, first) != 401 {
		t.Fatal("attempt-exhausted challenge accepted")
	}
	clearCooldown(t, f, email)
	issue(t, f, email)
	expired := sender.last(t).code
	if err := f.s.Redis.PExpire(f.ctx, challengeKey(email), time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if verify(t, f, email, expired) != 401 {
		t.Fatal("expired code accepted")
	}
	clearCooldown(t, f, email)
	issue(t, f, email)
	previousID := f.s.Redis.HGet(f.ctx, challengeKey(email), "id").Val()
	previousCode := sender.last(t).code
	clearCooldown(t, f, email)
	issue(t, f, email)
	currentID := f.s.Redis.HGet(f.ctx, challengeKey(email), "id").Val()
	if currentID == previousID {
		t.Fatal("resend reused challenge")
	}
	if previousCode != sender.last(t).code && verify(t, f, email, previousCode) != 401 {
		t.Fatal("previous code survived resend")
	}
	oldMAC := f.s.EmailAuth.digest(email, previousID, sender.last(t).code)
	result, err := f.s.Redis.Eval(f.ctx, verifyScript, []string{challengeKey(email)}, previousID, oldMAC).Slice()
	if err != nil || result[0].(int64) != -1 {
		t.Fatal("stale verifier raced through a resend")
	}
	if verify(t, f, email, sender.last(t).code) != 202 {
		t.Fatal("fresh challenge failed")
	}
}
func TestEmailOTPConcurrentVerificationConsumesOnce(t *testing.T) {
	f, sender := emailFixture(t)
	email := f.db.SuperAdminEmail()
	issue(t, f, email)
	code := sender.last(t).code
	var success, cookies atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := f.call(t, "POST", "/api/auth/verify", nil, f.s.AppURL, map[string]string{"email": email, "code": code})
			if w.Code == 200 {
				success.Add(1)
			}
			cookies.Add(int32(len(w.Result().Cookies())))
		}()
	}
	wg.Wait()
	if success.Load() != 1 || cookies.Load() != 1 {
		t.Fatalf("OTP authenticated %d times and issued %d cookies", success.Load(), cookies.Load())
	}
}
func TestEmailOTPDeliveryAndRedisFailClosed(t *testing.T) {
	f, sender := emailFixture(t)
	email := f.prefix + "failure@example.test"
	sender.failure = true
	expectStatus(t, f.call(t, "POST", "/api/auth/code", nil, f.s.AppURL, map[string]string{"email": email}), 503)
	if verify(t, f, email, sender.last(t).code) != 401 {
		t.Fatal("failed delivery left valid OTP")
	}
	clearCooldown(t, f, email)
	sender.failure = false
	sender.hook = func(email, code string) error {
		if verify(t, f, email, code) != 401 {
			t.Error("code accepted before sender confirmed delivery")
		}
		return nil
	}
	issue(t, f, email)
	if verify(t, f, email, sender.last(t).code) != 202 {
		t.Fatal("delivered challenge did not activate")
	}
	f.s.Redis.Close()
	expectStatus(t, f.call(t, "POST", "/api/auth/code", nil, f.s.AppURL, map[string]string{"email": email}), 429)
	expectStatus(t, f.call(t, "POST", "/api/auth/verify", nil, f.s.AppURL, map[string]string{"email": email, "code": "123456"}), 429)
}
func TestEmailOTPRateLimitsOriginAndValidation(t *testing.T) {
	f, sender := emailFixture(t)
	email := f.prefix + "limits@example.test"
	for _, path := range []string{"/api/auth/code", "/api/auth/verify"} {
		expectStatus(t, f.call(t, "POST", path, nil, "https://attacker.test", map[string]string{"email": email}), 403)
		expectStatus(t, f.call(t, "GET", path, nil, "", nil), 405)
		expectStatus(t, f.call(t, "POST", path, nil, f.s.AppURL, map[string]any{"email": email, "operator": true}), 400)
	}
	issue(t, f, email)
	w := f.call(t, "POST", "/api/auth/code", nil, f.s.AppURL, map[string]string{"email": email})
	expectStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("cooldown has no retry guidance")
	}
	sender.mu.Lock()
	sent := len(sender.sent)
	sender.mu.Unlock()
	if sent != 1 {
		t.Fatal("cooldown sent another email")
	}
	for i := 1; i < 10; i++ {
		clearCooldown(t, f, email)
		issue(t, f, email)
	}
	clearCooldown(t, f, email)
	expectStatus(t, f.call(t, "POST", "/api/auth/code", nil, f.s.AppURL, map[string]string{"email": email}), 429)
	// Email-wide verification throttling remains across challenge replacement.
	for i := 0; i < 30; i++ {
		verify(t, f, email, "999999")
	}
	expectStatus(t, f.call(t, "POST", "/api/auth/verify", nil, f.s.AppURL, map[string]string{"email": email, "code": "999999"}), 429)
}
func TestOldAdministratorPasswordSessionRejected(t *testing.T) {
	f := newAPIFixture(t)
	if _, err := f.db.DB.ExecContext(f.ctx, "UPDATE sessions SET auth_method='legacy' WHERE user_id=?", f.operator.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.call(t, "GET", "/api/state", f.operatorCookie, "", nil), 401)
	expectStatus(t, f.call(t, "POST", "/api/auth/login", nil, f.s.AppURL, map[string]string{"email": f.db.SuperAdminEmail(), "password": "test-password-strong"}), 410)
}

func TestEmailOTPFailedOlderDeliveryCannotDeleteResend(t *testing.T) {
	f, sender := emailFixture(t)
	email := f.prefix + "send-race@example.test"
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	sender.hook = func(email, code string) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return errors.New("old delivery failed")
		}
		return nil
	}
	done := make(chan int, 1)
	go func() {
		done <- f.call(t, "POST", "/api/auth/code", nil, f.s.AppURL, map[string]string{"email": email}).Code
	}()
	<-entered
	// Model elapsed cooldown while the first mail provider call is still pending.
	clearCooldown(t, f, email)
	issue(t, f, email)
	newCode := sender.last(t).code
	close(release)
	if status := <-done; status != 503 {
		t.Fatalf("old failed send status=%d", status)
	}
	if verify(t, f, email, newCode) != 202 {
		t.Fatal("failed older sender deleted the new challenge")
	}
}
func TestEmailOTPMissingSenderDoesNotPretendToSend(t *testing.T) {
	f, _ := emailFixture(t)
	f.s.EmailAuth.sender = nil
	email := f.prefix + "unconfigured@example.test"
	w := f.call(t, "POST", "/api/auth/code", nil, f.s.AppURL, map[string]string{"email": email})
	expectStatus(t, w, 503)
	if !strings.Contains(w.Body.String(), "email_unavailable") || f.s.Redis.Exists(f.ctx, challengeKey(email)).Val() != 0 {
		t.Fatal("unconfigured sender issued a usable challenge")
	}
}

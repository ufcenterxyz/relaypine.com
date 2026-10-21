package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

func (f *apiFixture) verifiedCustomer(t *testing.T, suffix string) (domain.User, *http.Cookie) {
	t.Helper()
	u, err := f.db.EmailUser(f.ctx, f.prefix+suffix+"@example.test", "Verified customer")
	if err != nil {
		t.Fatal(err)
	}
	f.users = append(f.users, u)
	token, err := f.db.CreateSurfaceSession(f.ctx, u.ID, "customer")
	if err != nil {
		t.Fatal(err)
	}
	return u, &http.Cookie{Name: "rp_session", Value: token}
}
func TestCustomerKeysAndSnapshotsAreTenantScoped(t *testing.T) {
	f := newAPIFixture(t)
	first, firstCookie := f.verifiedCustomer(t, "first")
	second, secondCookie := f.verifiedCustomer(t, "second")
	if first.TenantID == second.TenantID || first.Operator || second.Operator {
		t.Fatal("customer ownership collision")
	}
	modelID := f.prefix + "public"
	f.models = append(f.models, modelID)
	model, err := f.db.SaveModel(f.ctx, domain.Model{ID: modelID, Name: "Published model", Provider: "openai", UpstreamModel: "hidden-upstream-model", Enabled: true, InputRate: 1000000, OutputRate: 2000000, CostInputRate: 333333, CostOutputRate: 444444, MaxOutputTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	firstKey := f.call(t, "POST", "/api/keys", firstCookie, f.s.AppURL, map[string]any{"name": "First secret", "rpm": 20, "models": []string{modelID}})
	expectStatus(t, firstKey, 201)
	created := decodeResponse[struct {
		Key    domain.AccessKey
		Secret string
	}](t, firstKey)
	if created.Key.TenantID != first.TenantID {
		t.Fatal("key tenant was not derived from session")
	}
	if key, err := f.db.AuthenticateKey(f.ctx, created.Secret); err != nil || key.TenantID != first.TenantID {
		t.Fatal("verified customer relay key rejected")
	}
	expectStatus(t, f.call(t, "DELETE", "/api/keys/"+created.Key.ID, secondCookie, f.s.AppURL, nil), 404)
	if _, err := f.db.AuthenticateKey(f.ctx, created.Secret); err != nil {
		t.Fatal("cross-tenant revoke succeeded")
	}
	if err := f.db.Credit(f.ctx, first.TenantID, 123456, "access-test:"+f.prefix, "Tenant balance isolation"); err != nil {
		t.Fatal(err)
	}
	credential, err := f.db.SaveCredential(f.ctx, domain.Credential{Name: "Private routing credential", EndpointID: "openai-official", Secret: "test-upstream", QuotaGroup: f.prefix, Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 10})
	if err != nil {
		t.Fatal(err)
	}
	f.credentials = append(f.credentials, credential.ID)
	requestID := f.prefix + "request"
	if err = f.db.Reserve(f.ctx, domain.Reservation{Request: domain.Request{ID: requestID, TenantID: first.TenantID, ProjectID: created.Key.ProjectID, AccessKeyID: created.Key.ID, CredentialID: credential.ID, Provider: model.Provider, Model: model.ID, ReservedMicros: 10}, Model: model, CredentialID: credential.ID, CostReserve: 5}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		u       domain.User
		cookie  *http.Cookie
		balance int64
		keys    int
	}{{first, firstCookie, 123456, 1}, {second, secondCookie, 0, 0}} {
		w := f.call(t, "GET", "/api/state", tc.cookie, f.s.AppURL, nil)
		expectStatus(t, w, 200)
		snap := decodeResponse[domain.Snapshot](t, w)
		if snap.User.ID != tc.u.ID || snap.User.Operator || snap.Wallet.TenantID != tc.u.TenantID || snap.Wallet.BalanceMicros != tc.balance || len(snap.Keys) != tc.keys {
			t.Fatal("tenant snapshot not isolated")
		}
		if len(snap.Requests) != tc.keys {
			t.Fatal("another tenant's requests leaked")
		}
		for _, request := range snap.Requests {
			if request.TenantID != tc.u.TenantID || request.CredentialID != "" || request.CostMicros != 0 {
				t.Fatal("request procurement data leaked")
			}
		}
		if len(snap.Credentials) != 0 || len(snap.Endpoints) != 0 || len(snap.Customers) != 0 || len(snap.Proxies) != 0 || len(snap.Waitlist) != 0 {
			t.Fatal("customer received operator resources")
		}
		for _, model := range snap.Models {
			if model.CostInputRate != 0 || model.CostOutputRate != 0 || model.UpstreamModel != "" || model.PricingReference != nil || model.PricingNote != "" {
				t.Fatal("customer received procurement information")
			}
		}
		if strings.Contains(w.Body.String(), "hidden-upstream-model") {
			t.Fatal("upstream model leaked")
		}
		expectStatus(t, f.call(t, "POST", "/api/admin/credits", tc.cookie, f.s.AppURL, map[string]any{}), 404)
		expectStatus(t, f.call(t, "GET", "/api/state", tc.cookie, f.s.AdminAppURL, nil), 401)
	}
	// Even a superadmin's valid public session cannot expose the operator snapshot.
	publicToken, err := f.db.CreateSurfaceSession(f.ctx, f.operator.ID, "customer")
	if err != nil {
		t.Fatal(err)
	}
	publicCookie := &http.Cookie{Name: "rp_session", Value: publicToken}
	adminPublic := decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", publicCookie, f.s.AppURL, nil))
	if adminPublic.User.Operator || len(adminPublic.Customers) != 0 || len(adminPublic.Credentials) != 0 {
		t.Fatal("public host elevated superadmin view")
	}
	expectStatus(t, f.call(t, "POST", "/api/admin/models", publicCookie, f.s.AppURL, map[string]any{}), 404)
	expectStatus(t, f.call(t, "GET", "/api/state", publicCookie, f.s.AdminAppURL, nil), 401)
	if _, err = f.db.CreateSurfaceSession(f.ctx, first.ID, "admin"); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("customer created admin session")
	}
	// Restarts retain verified customer access without granting or moving money.
	if _, err = f.db.EnsureSuperAdmin(f.ctx, "API administrator"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.call(t, "GET", "/api/state", firstCookie, f.s.AppURL, nil), 200)
	if _, err = f.db.AuthenticateKey(f.ctx, created.Secret); err != nil {
		t.Fatal("bootstrap retired verified customer key")
	}
	expectStatus(t, f.call(t, "DELETE", "/api/keys/"+created.Key.ID, firstCookie, f.s.AppURL, nil), 200)
	if _, err = f.db.AuthenticateKey(f.ctx, created.Secret); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("owner revoke ineffective")
	}
}

func TestOTPIsBoundToConsoleSurfaceAndAdminRole(t *testing.T) {
	f, sender := emailFixture(t)
	email := f.db.SuperAdminEmail()
	issue(t, f, email)
	code := sender.last(t).code
	expectStatus(t, f.call(t, "POST", "/api/auth/verify", nil, f.s.AdminAppURL, map[string]string{"email": email, "code": code}), 401)
	expectStatus(t, f.call(t, "POST", "/api/auth/verify", nil, f.s.AppURL, map[string]string{"email": email, "code": code}), 200)
	ordinary := f.prefix + "no-admin@example.test"
	expectStatus(t, f.call(t, "POST", "/api/auth/code", nil, f.s.AdminAppURL, map[string]string{"email": ordinary}), 202)
	denied := f.call(t, "POST", "/api/auth/verify", nil, f.s.AdminAppURL, map[string]string{"email": ordinary, "code": sender.last(t).code})
	expectStatus(t, denied, 403)
	if len(denied.Result().Cookies()) != 0 {
		t.Fatal("ordinary mailbox received admin cookie")
	}
	var n int
	if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM users WHERE email=?", ordinary).Scan(&n); err != nil || n != 0 {
		t.Fatal("denied admin sign-in provisioned customer")
	}
}

func TestVerifiedLegacyMailboxKeepsOwnershipAndRevokesOldCredentials(t *testing.T) {
	f := newAPIFixture(t)
	_, oldSecret := f.legacyKey(t, f.customer)
	if err := f.db.Credit(f.ctx, f.customer.TenantID, 54321, "legacy-owner:"+f.prefix, "Existing verified owner balance"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.JoinWaitlist(f.ctx, f.customer.Email, "Legacy applicant"); err != nil {
		t.Fatal(err)
	}
	user, err := f.db.EmailUser(f.ctx, strings.ToUpper(f.customer.Email), "")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != f.customer.ID || user.TenantID != f.customer.TenantID || user.Operator {
		t.Fatal("verification reassigned historical identity")
	}
	if _, err = f.db.AuthenticateKey(f.ctx, oldSecret); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("old unverified key reactivated")
	}
	if _, err = f.db.SurfaceSessionUser(f.ctx, f.customerCookie.Value, "customer"); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("old password session reactivated")
	}
	token, err := f.db.CreateSurfaceSession(f.ctx, user.ID, "customer")
	if err != nil {
		t.Fatal(err)
	}
	snap := decodeResponse[domain.Snapshot](t, f.call(t, "GET", "/api/state", &http.Cookie{Name: "rp_session", Value: token}, f.s.AppURL, nil))
	if snap.Wallet.BalanceMicros != 54321 || snap.Wallet.TenantID != user.TenantID {
		t.Fatal("existing balance lost on verification")
	}
	_, newSecret, err := f.db.CreateKey(f.ctx, user, "Verified replacement", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.DB.ExecContext(f.ctx, "UPDATE tenants SET status='suspended' WHERE id=?", user.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.AuthenticateKey(f.ctx, newSecret); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("suspended tenant relay key authenticated")
	}
	if _, err = f.db.SurfaceSessionUser(f.ctx, token, "customer"); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("suspended tenant session authenticated")
	}
	if _, err = f.db.EmailUser(f.ctx, user.Email, ""); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("OTP reactivated suspended tenant")
	}
	if _, _, err = f.db.CreateKey(f.ctx, user, "Suspended key", nil, 10); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("suspended tenant created key")
	}
}

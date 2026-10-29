package api

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"os"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

func TestRequestDetailIsTenantScopedEncryptedAndKept(t *testing.T) {
	f := newAPIFixture(t)
	owner, ownerCookie := f.verifiedCustomer(t, "detail-owner")
	_, otherCookie := f.verifiedCustomer(t, "detail-other")
	modelID := f.prefix + "detail"
	f.models = append(f.models, modelID)
	model, err := f.db.SaveModel(f.ctx, domain.Model{ID: modelID, Name: "Detail model", Provider: "openai", UpstreamModel: "hidden-upstream-model", Enabled: true, InputRate: 1000000, OutputRate: 2000000, CostInputRate: 333333, CostOutputRate: 444444, MaxOutputTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := f.db.SaveCredential(f.ctx, domain.Credential{Name: "Detail credential", EndpointID: "openai-official", Secret: "test-upstream", QuotaGroup: f.prefix, Enabled: true, RPM: 60, TPM: 100000, MaxConcurrency: 10})
	if err != nil {
		t.Fatal(err)
	}
	f.credentials = append(f.credentials, credential.ID)
	issued := f.call(t, "POST", "/api/keys", ownerCookie, f.s.AppURL, map[string]any{"name": "Detail key", "rpm": 20, "models": []string{modelID}})
	expectStatus(t, issued, 201)
	key := decodeResponse[struct{ Key domain.AccessKey }](t, issued).Key
	if err = f.db.Credit(f.ctx, owner.TenantID, 1000, "detail-test:"+f.prefix, "Request detail test"); err != nil {
		t.Fatal(err)
	}
	reserve := func(id string) {
		t.Helper()
		if err := f.db.Reserve(f.ctx, domain.Reservation{Request: domain.Request{ID: id, TenantID: owner.TenantID, ProjectID: key.ProjectID, AccessKeyID: key.ID, CredentialID: credential.ID, Provider: model.Provider, Model: model.ID, ReservedMicros: 10}, Model: model, CredentialID: credential.ID, CostReserve: 5}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(id string, cookie *http.Cookie, origin string) domain.RequestDetail {
		t.Helper()
		w := f.call(t, "GET", "/api/requests/"+id, cookie, origin, nil)
		expectStatus(t, w, 200)
		return decodeResponse[domain.RequestDetail](t, w)
	}
	requestID := f.prefix + "detail"
	reserve(requestID)
	// A running request has no body yet.
	if running := read(requestID, ownerCookie, f.s.AppURL); running.PayloadStatus != domain.PayloadPending || running.Payload != nil {
		t.Fatalf("running request %+v", running)
	}
	body := `{"model":"` + modelID + `","messages":[{"role":"user","content":[{"type":"text","text":"Describe the private diagram"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`
	sent := domain.RequestPayload{
		Request:  domain.CapturedRequest{Method: "POST", Path: "/openai/v1/chat/completions", Headers: map[string]string{"user-agent": "sdk-test/1.0"}, Body: body, Bytes: len(body)},
		Response: domain.CapturedResponse{Status: 400, ContentType: "application/json", Body: `{"error":{"type":"upstream_error"}}`, Bytes: 35},
		Upstream: &domain.CapturedResponse{Status: 400, ContentType: "application/json", Body: `{"error":{"message":"organization org-private"}}`, Bytes: 47},
	}
	if err = f.db.SavePayload(f.ctx, requestID, sent); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	var location string
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT location,payload FROM request_payloads WHERE request_id=?", requestID).Scan(&location, &stored); err != nil {
		t.Fatal(err)
	}
	if location != "database" || bytes.Contains(stored, []byte("private diagram")) || bytes.Contains(stored, []byte("org-private")) {
		t.Fatalf("captured exchange stored in plaintext or elsewhere: %s", location)
	}
	detail := read(requestID, ownerCookie, f.s.AppURL)
	if detail.Request.ID != requestID || detail.Request.CredentialID != "" || detail.RetentionDays != 0 || detail.PayloadStatus != domain.PayloadStored || detail.Payload == nil {
		t.Fatalf("customer detail %+v", detail)
	}
	if got := detail.Payload; got.Request.Body != body || got.Request.Headers["user-agent"] != "sdk-test/1.0" || got.Response != sent.Response || got.Upstream != nil {
		t.Fatalf("customer payload %+v", got)
	}
	expectStatus(t, f.call(t, "GET", "/api/requests/"+requestID, otherCookie, f.s.AppURL, nil), 404)
	expectStatus(t, f.call(t, "GET", "/api/requests/"+f.prefix+"missing", ownerCookie, f.s.AppURL, nil), 404)
	expectStatus(t, f.call(t, "GET", "/api/requests/"+requestID, nil, f.s.AppURL, nil), 401)
	admin := read(requestID, f.operatorCookie, f.s.AdminAppURL)
	if admin.Payload == nil || admin.Payload.Upstream == nil || *admin.Payload.Upstream != *sent.Upstream || admin.Request.CredentialID != credential.ID {
		t.Fatalf("administrator detail %+v", admin)
	}
	// Without a retention period, bodies are kept indefinitely.
	if _, err = f.db.DB.ExecContext(f.ctx, "UPDATE request_payloads SET created_at=? WHERE request_id=?", time.Now().UTC().AddDate(-2, 0, 0), requestID); err != nil {
		t.Fatal(err)
	}
	if err = f.db.MaintainPayloads(f.ctx); err != nil {
		t.Fatal(err)
	}
	if kept := read(requestID, ownerCookie, f.s.AppURL); kept.PayloadStatus != domain.PayloadStored || kept.Payload == nil {
		t.Fatalf("old body not kept: %+v", kept.PayloadStatus)
	}
	// A finished request without a body was made before recording started, or
	// its save failed.
	unrecorded := f.prefix + "unrecorded"
	reserve(unrecorded)
	if _, err = f.db.DB.ExecContext(f.ctx, "UPDATE requests SET status='completed',updated_at=? WHERE id=?", time.Now().UTC().Add(-time.Hour), unrecorded); err != nil {
		t.Fatal(err)
	}
	if missing := read(unrecorded, ownerCookie, f.s.AppURL); missing.PayloadStatus != domain.PayloadNotRecorded || missing.Payload != nil {
		t.Fatalf("unrecorded request %+v", missing.PayloadStatus)
	}

	// Another replica configured with a retention period and with recording off.
	master := os.Getenv("MASTER_KEY")
	if master == "" {
		master = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	limited, err := store.Open(f.ctx, os.Getenv("MYSQL_TEST_DSN"), master, store.Config{SuperAdminEmail: testAdminEmail, PayloadRetention: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	if off, err := limited.RequestDetail(f.ctx, owner, unrecorded); err != nil || off.PayloadStatus != domain.PayloadDisabled || off.RetentionDays != 30 {
		t.Fatalf("recording off: %+v %v", off, err)
	}
	// Retention applies at read time, before cleanup removes the body.
	if expired, err := limited.RequestDetail(f.ctx, owner, requestID); err != nil || expired.PayloadStatus != domain.PayloadExpired || expired.Payload != nil {
		t.Fatalf("expired body readable: %+v %v", expired.PayloadStatus, err)
	}
	if err = limited.MaintainPayloads(f.ctx); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM request_payloads WHERE request_id=?", requestID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("expired body was not deleted")
	}
}

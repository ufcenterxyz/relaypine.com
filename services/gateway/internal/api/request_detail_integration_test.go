package api

import (
	"bytes"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

func TestRequestDetailIsTenantScopedEncryptedAndExpires(t *testing.T) {
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
	requestID := f.prefix + "detail"
	if err = f.db.Reserve(f.ctx, domain.Reservation{Request: domain.Request{ID: requestID, TenantID: owner.TenantID, ProjectID: key.ProjectID, AccessKeyID: key.ID, CredentialID: credential.ID, Provider: model.Provider, Model: model.ID, ReservedMicros: 10}, Model: model, CredentialID: credential.ID, CostReserve: 5}); err != nil {
		t.Fatal(err)
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
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT payload FROM request_payloads WHERE request_id=?", requestID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("private diagram")) || bytes.Contains(stored, []byte("org-private")) {
		t.Fatal("captured exchange stored in plaintext")
	}
	path := "/api/requests/" + requestID
	w := f.call(t, "GET", path, ownerCookie, f.s.AppURL, nil)
	expectStatus(t, w, 200)
	detail := decodeResponse[domain.RequestDetail](t, w)
	if detail.Request.ID != requestID || detail.Request.CredentialID != "" || detail.RetentionDays != 7 || detail.Payload == nil {
		t.Fatalf("customer detail %+v", detail)
	}
	if got := detail.Payload; got.Request.Body != body || got.Request.Headers["user-agent"] != "sdk-test/1.0" || got.Response != sent.Response || got.Upstream != nil {
		t.Fatalf("customer payload %+v", got)
	}
	expectStatus(t, f.call(t, "GET", path, otherCookie, f.s.AppURL, nil), 404)
	expectStatus(t, f.call(t, "GET", "/api/requests/"+f.prefix+"missing", ownerCookie, f.s.AppURL, nil), 404)
	expectStatus(t, f.call(t, "GET", path, nil, f.s.AppURL, nil), 401)
	w = f.call(t, "GET", path, f.operatorCookie, f.s.AdminAppURL, nil)
	expectStatus(t, w, 200)
	admin := decodeResponse[domain.RequestDetail](t, w)
	if admin.Payload == nil || admin.Payload.Upstream == nil || *admin.Payload.Upstream != *sent.Upstream || admin.Request.CredentialID != credential.ID {
		t.Fatalf("administrator detail %+v", admin)
	}
	// Retention applies at read time, before the cleanup task removes the row.
	if _, err = f.db.DB.ExecContext(f.ctx, "UPDATE request_payloads SET created_at=? WHERE request_id=?", time.Now().UTC().Add(-8*24*time.Hour), requestID); err != nil {
		t.Fatal(err)
	}
	w = f.call(t, "GET", path, ownerCookie, f.s.AppURL, nil)
	expectStatus(t, w, 200)
	if expired := decodeResponse[domain.RequestDetail](t, w); expired.Payload != nil || expired.Request.ID != requestID {
		t.Fatalf("expired payload still readable: %+v", expired.Payload)
	}
	if err = f.db.PrunePayloads(f.ctx); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM request_payloads WHERE request_id=?", requestID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("expired payload was not pruned")
	}
}

package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

type mockStripe struct {
	mu           sync.Mutex
	sessions     map[string]session
	intents      map[string]map[string]any
	refunds      map[string]refundResult
	posts        map[string]refundResult
	refundStatus string
	refundPosts  int
}

func (m *mockStripe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if user, _, _ := r.BasicAuth(); user != "sk_test_mock" {
		http.Error(w, "bad auth", 401)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions" {
		_ = r.ParseForm()
		var cents int64
		_, _ = fmt.Sscan(r.Form.Get("line_items[0][price_data][unit_amount]"), &cents)
		sid := id("cs_")
		pi := id("pi_")
		v := session{ID: sid, URL: "https://checkout.stripe.com/c/pay/" + sid, Mode: "payment", Currency: "usd", AmountTotal: cents, Status: "open", PaymentStatus: "unpaid", PaymentIntent: pi, ClientReference: r.Form.Get("client_reference_id"), Metadata: map[string]string{"order_id": r.Form.Get("metadata[order_id]"), "tenant_id": r.Form.Get("metadata[tenant_id]")}}
		m.sessions[sid] = v
		m.intents[pi] = map[string]any{"status": "succeeded", "currency": "usd", "amount_received": cents, "livemode": false, "latest_charge": map[string]any{"paid": true, "disputed": false, "refunded": false, "amount_refunded": 0}}
		_ = json.NewEncoder(w).Encode(v)
		return
	}
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
		_ = json.NewEncoder(w).Encode(m.sessions[strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/")])
		return
	}
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/payment_intents/") {
		_ = json.NewEncoder(w).Encode(m.intents[strings.TrimPrefix(r.URL.Path, "/v1/payment_intents/")])
		return
	}
	if r.Method == "POST" && r.URL.Path == "/v1/refunds" {
		m.refundPosts++
		idem := r.Header.Get("Idempotency-Key")
		if old, ok := m.posts[idem]; ok {
			_ = json.NewEncoder(w).Encode(old)
			return
		}
		_ = r.ParseForm()
		var amount int64
		_, _ = fmt.Sscan(r.Form.Get("amount"), &amount)
		v := refundResult{ID: id("re_"), Status: m.refundStatus, Currency: "usd", Amount: amount, PaymentIntent: r.Form.Get("payment_intent"), Metadata: map[string]string{"order_id": r.Form.Get("metadata[order_id]")}}
		m.posts[idem] = v
		m.refunds[v.ID] = v
		_ = json.NewEncoder(w).Encode(v)
		return
	}
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/refunds/") {
		_ = json.NewEncoder(w).Encode(m.refunds[strings.TrimPrefix(r.URL.Path, "/v1/refunds/")])
		return
	}
	if r.Method == "GET" && r.URL.Path == "/v1/refunds" {
		items := []refundResult{}
		for _, v := range m.refunds {
			if v.PaymentIntent == r.URL.Query().Get("payment_intent") {
				items = append(items, v)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items, "has_more": false})
		return
	}
	http.Error(w, "unexpected mock path", 404)
}

type paymentFixture struct {
	s      *Stripe
	store  *store.Store
	mock   *mockStripe
	ctx    context.Context
	user   domain.User
	events []string
}

func paymentIntegration(t *testing.T) *paymentFixture {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN not set; real MySQL payment integration skipped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	master := os.Getenv("MASTER_KEY")
	if master == "" {
		master = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	db, err := store.Open(ctx, dsn, master)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := db.Register(ctx, id("pay_test_")+"@example.test", "Payment integration", "test-password-strong", false)
	if err != nil {
		t.Fatal(err)
	}
	m := &mockStripe{sessions: map[string]session{}, intents: map[string]map[string]any{}, refunds: map[string]refundResult{}, posts: map[string]refundResult{}, refundStatus: "pending"}
	server := httptest.NewServer(m)
	t.Cleanup(server.Close)
	s := New(db, "sk_test_mock", "whsec_mock", "https://relaypine.com", "2026-04-22.dahlia")
	s.APIBase = server.URL
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := &paymentFixture{s: s, store: db, mock: m, ctx: ctx, user: user}
	t.Cleanup(func() {
		for _, event := range f.events {
			_, _ = db.DB.ExecContext(context.Background(), "DELETE FROM payment_events WHERE id=?", event)
		}
		for _, q := range []string{"DELETE r FROM payment_refunds r JOIN topup_orders o ON o.id=r.order_id WHERE o.tenant_id=?", "DELETE FROM topup_orders WHERE tenant_id=?", "DELETE FROM ledger_entries WHERE tenant_id=?", "DELETE s FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.tenant_id=?", "DELETE FROM users WHERE tenant_id=?", "DELETE FROM projects WHERE tenant_id=?", "DELETE FROM wallets WHERE tenant_id=?", "DELETE FROM tenants WHERE id=?"} {
			if _, err := db.DB.ExecContext(context.Background(), q, user.TenantID); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
	})
	return f
}
func (f *paymentFixture) checkout(t *testing.T) (string, session) {
	t.Helper()
	u, err := f.s.Checkout(f.ctx, f.user, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "https://checkout.stripe.com/") {
		t.Fatal(u)
	}
	var order, sid string
	if err = f.store.DB.QueryRowContext(f.ctx, "SELECT id,session_id FROM topup_orders WHERE tenant_id=? ORDER BY created_at DESC LIMIT 1", f.user.TenantID).Scan(&order, &sid); err != nil {
		t.Fatal(err)
	}
	f.mock.mu.Lock()
	v := f.mock.sessions[sid]
	v.Status = "complete"
	v.PaymentStatus = "paid"
	f.mock.sessions[sid] = v
	f.mock.mu.Unlock()
	return order, v
}
func (f *paymentFixture) event(t *testing.T, kind string, obj any) string {
	t.Helper()
	eid := id("evt_test_")
	f.events = append(f.events, eid)
	body, _ := json.Marshal(map[string]any{"id": eid, "type": kind, "livemode": false, "data": map[string]any{"object": obj}})
	f.deliver(t, body)
	return eid
}
func (f *paymentFixture) deliver(t *testing.T, body []byte) {
	t.Helper()
	stamp := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte("whsec_mock"))
	_, _ = fmt.Fprintf(mac, "%d.", stamp)
	_, _ = mac.Write(body)
	r := httptest.NewRequest("POST", "/api/payments/stripe/webhook", strings.NewReader(string(body)))
	r.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", stamp, hex.EncodeToString(mac.Sum(nil))))
	w := httptest.NewRecorder()
	f.s.Webhook(w, r)
	if w.Code != 200 {
		t.Fatalf("webhook %d %s", w.Code, w.Body)
	}
}
func (f *paymentFixture) wallet(t *testing.T) (int64, int64) {
	t.Helper()
	var balance, held int64
	if err := f.store.DB.QueryRowContext(f.ctx, "SELECT balance_micros,held_micros FROM wallets WHERE tenant_id=?", f.user.TenantID).Scan(&balance, &held); err != nil {
		t.Fatal(err)
	}
	return balance, held
}
func (f *paymentFixture) orderStatus(t *testing.T, order string) string {
	t.Helper()
	var status string
	if err := f.store.DB.QueryRowContext(f.ctx, "SELECT status FROM topup_orders WHERE id=?", order).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}
func (f *paymentFixture) tenantStatus(t *testing.T) string {
	t.Helper()
	var status string
	if err := f.store.DB.QueryRowContext(f.ctx, "SELECT status FROM tenants WHERE id=?", f.user.TenantID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestMySQLStripeDuplicateWebhookAndPaymentIntent(t *testing.T) {
	f := paymentIntegration(t)
	order, v := f.checkout(t)
	eid := f.event(t, "checkout.session.completed", map[string]string{"id": v.ID})
	duplicate, _ := json.Marshal(map[string]any{"id": eid, "type": "checkout.session.completed", "livemode": false, "data": map[string]any{"object": map[string]string{"id": v.ID}}})
	f.deliver(t, duplicate)
	f.event(t, "checkout.session.async_payment_succeeded", map[string]string{"id": v.ID})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.s.ProcessPending(f.ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := f.s.ProcessPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	balance, held := f.wallet(t)
	if balance != 10000000 || held != 0 || f.orderStatus(t, order) != "paid" {
		t.Fatalf("wallet balance=%d held=%d", balance, held)
	}
	var count int
	if err := f.store.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM ledger_entries WHERE tenant_id=? AND reference=?", f.user.TenantID, "stripe:"+v.PaymentIntent).Scan(&count); err != nil || count != 1 {
		t.Fatalf("credit count=%d err=%v", count, err)
	}
	// A second local order cannot reuse an already credited PaymentIntent.
	other, v2 := f.checkout(t)
	f.mock.mu.Lock()
	v2.PaymentIntent = v.PaymentIntent
	f.mock.sessions[v2.ID] = v2
	f.mock.mu.Unlock()
	f.event(t, "checkout.session.completed", map[string]string{"id": v2.ID})
	_ = f.s.ProcessPending(f.ctx)
	balance, _ = f.wallet(t)
	if balance != 10000000 || f.orderStatus(t, other) == "paid" {
		t.Fatal("same PaymentIntent credited twice")
	}
}

func TestMySQLStripeReconcilePaidAndRefundedSessions(t *testing.T) {
	for _, refunded := range []bool{false, true} {
		t.Run(fmt.Sprint(refunded), func(t *testing.T) {
			f := paymentIntegration(t)
			order, v := f.checkout(t)
			if refunded {
				f.mock.mu.Lock()
				f.mock.intents[v.PaymentIntent]["latest_charge"] = map[string]any{"paid": true, "refunded": true, "amount_refunded": 1000, "disputed": false}
				f.mock.mu.Unlock()
			}
			if err := f.s.Reconcile(f.ctx); err != nil {
				t.Fatal(err)
			}
			balance, held := f.wallet(t)
			expected := int64(10000000)
			status := "paid"
			if refunded {
				expected = 0
				status = "review_required"
			}
			if balance != expected || held != 0 || f.orderStatus(t, order) != status {
				t.Fatalf("balance=%d held=%d status=%s", balance, held, f.orderStatus(t, order))
			}
		})
	}
}

func TestMySQLStripeRefundPollingAndNormalRefundDoesNotSuspend(t *testing.T) {
	f := paymentIntegration(t)
	order, v := f.checkout(t)
	_ = f.s.Reconcile(f.ctx)
	state, err := f.s.Refund(f.ctx, order, "operator")
	if err != nil || state != "pending" {
		t.Fatalf("refund %s %v", state, err)
	}
	balance, held := f.wallet(t)
	if balance != 10000000 || held != 10000000 {
		t.Fatalf("missing refund hold %d %d", balance, held)
	}
	f.mock.mu.Lock()
	for rid, refund := range f.mock.refunds {
		refund.Status = "succeeded"
		f.mock.refunds[rid] = refund
	}
	f.mock.mu.Unlock()
	if err = f.s.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	balance, held = f.wallet(t)
	if balance != 0 || held != 0 || f.orderStatus(t, order) != "refunded" {
		t.Fatalf("refund not reconciled %d %d %s", balance, held, f.orderStatus(t, order))
	}
	if f.mock.refundPosts != 1 {
		t.Fatal("reconciliation replayed cached refund POST instead of retrieving current state")
	}
	f.event(t, "charge.refunded", map[string]any{"id": "ch_test", "payment_intent": v.PaymentIntent, "amount_refunded": 1000})
	_ = f.s.ProcessPending(f.ctx)
	if f.tenantStatus(t) == "suspended" {
		t.Fatal("normal unused-credit refund suspended tenant")
	}
}

func TestMySQLStripeFailedRefundReleasesHold(t *testing.T) {
	f := paymentIntegration(t)
	order, _ := f.checkout(t)
	_ = f.s.Reconcile(f.ctx)
	_, err := f.s.Refund(f.ctx, order, "operator")
	if err != nil {
		t.Fatal(err)
	}
	f.mock.mu.Lock()
	var rid string
	for id, v := range f.mock.refunds {
		v.Status = "failed"
		f.mock.refunds[id] = v
		rid = id
	}
	f.mock.mu.Unlock()
	f.event(t, "refund.failed", map[string]string{"id": rid})
	_ = f.s.ProcessPending(f.ctx)
	balance, held := f.wallet(t)
	if balance != 10000000 || held != 0 || f.orderStatus(t, order) != "refund_failed" {
		t.Fatalf("failed refund %d %d %s", balance, held, f.orderStatus(t, order))
	}
	f.event(t, "refund.updated", map[string]string{"id": rid})
	_ = f.s.ProcessPending(f.ctx)
	balance, held = f.wallet(t)
	if balance != 10000000 || held != 0 {
		t.Fatal("duplicate failed refund released twice")
	}
}

func TestMySQLStripeDisputeAndExternalRefundFreeze(t *testing.T) {
	for _, kind := range []string{"charge.dispute.created", "charge.refunded"} {
		t.Run(kind, func(t *testing.T) {
			f := paymentIntegration(t)
			_, v := f.checkout(t)
			_ = f.s.Reconcile(f.ctx)
			f.event(t, kind, map[string]string{"payment_intent": v.PaymentIntent})
			_ = f.s.ProcessPending(f.ctx)
			if f.tenantStatus(t) != "suspended" {
				t.Fatal("disputed or externally refunded account was not suspended")
			}
			balance, held := f.wallet(t)
			if balance != 10000000 || held != 0 {
				t.Fatal("external event silently changed wallet balance")
			}
		})
	}
}

func TestMySQLStripeLostRefundResponseRecoveredBeforeReplay(t *testing.T) {
	f := paymentIntegration(t)
	order, _ := f.checkout(t)
	_ = f.s.Reconcile(f.ctx)
	_, err := f.s.Refund(f.ctx, order, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.DB.ExecContext(f.ctx, "UPDATE payment_refunds SET refund_id=NULL WHERE order_id=?", order); err != nil {
		t.Fatal(err)
	}
	f.mock.mu.Lock()
	for rid, v := range f.mock.refunds {
		v.Status = "succeeded"
		f.mock.refunds[rid] = v
	}
	f.mock.mu.Unlock()
	if err = f.s.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	balance, held := f.wallet(t)
	if balance != 0 || held != 0 || f.mock.refundPosts != 1 {
		t.Fatalf("lost response replayed or hold not settled %d %d posts=%d", balance, held, f.mock.refundPosts)
	}
}

func TestMySQLStripeRefundEventBeforeRefundWebhook(t *testing.T) {
	f := paymentIntegration(t)
	order, v := f.checkout(t)
	_ = f.s.Reconcile(f.ctx)
	if _, err := f.s.Refund(f.ctx, order, "operator"); err != nil {
		t.Fatal(err)
	}
	f.mock.mu.Lock()
	for rid, refund := range f.mock.refunds {
		refund.Status = "succeeded"
		f.mock.refunds[rid] = refund
	}
	f.mock.mu.Unlock()
	f.event(t, "charge.refunded", map[string]any{"payment_intent": v.PaymentIntent, "amount_refunded": 1000})
	_ = f.s.ProcessPending(f.ctx)
	balance, held := f.wallet(t)
	if f.tenantStatus(t) == "suspended" || balance != 0 || held != 0 || f.orderStatus(t, order) != "refunded" {
		t.Fatalf("out-of-order normal refund %d %d status=%s", balance, held, f.tenantStatus(t))
	}
}

func TestMySQLStripePendingRefundDoesNotHideExternalPartialRefund(t *testing.T) {
	f := paymentIntegration(t)
	order, v := f.checkout(t)
	_ = f.s.Reconcile(f.ctx)
	if _, err := f.s.Refund(f.ctx, order, "operator"); err != nil {
		t.Fatal(err)
	}
	f.event(t, "charge.refunded", map[string]any{"payment_intent": v.PaymentIntent, "amount_refunded": 100})
	_ = f.s.ProcessPending(f.ctx)
	if f.tenantStatus(t) != "suspended" {
		t.Fatal("pending local refund hid external partial refund")
	}
}

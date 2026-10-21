package payments

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

type Stripe struct {
	Store                                  *store.Store
	Secret, WebhookSecret, AppURL, Version string
	Client                                 *http.Client
	APIBase                                string
}

func New(s *store.Store, secret, webhook, appURL, version string) *Stripe {
	return &Stripe{Store: s, Secret: secret, WebhookSecret: webhook, AppURL: appURL, Version: version, APIBase: "https://api.stripe.com", Client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (s *Stripe) Enabled() bool { return s.Secret != "" && s.WebhookSecret != "" }
func id(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random source unavailable")
	}
	return prefix + hex.EncodeToString(b[:])
}
func (s *Stripe) Migrate(ctx context.Context) error {
	_, e := s.Store.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS payment_refunds(order_id VARCHAR(64) PRIMARY KEY, refund_id VARCHAR(255) NULL UNIQUE, status VARCHAR(32) NOT NULL, amount_micros BIGINT NOT NULL, actor_id VARCHAR(64) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6))`)
	return e
}
func (s *Stripe) call(ctx context.Context, method, path string, form url.Values, idem string, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, e := http.NewRequestWithContext(ctx, method, s.APIBase+path, body)
	if e != nil {
		return e
	}
	req.SetBasicAuth(s.Secret, "")
	req.Header.Set("Stripe-Version", s.Version)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	res, e := s.Client.Do(req)
	if e != nil {
		return errors.New("payment provider unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return fmt.Errorf("payment provider status %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out)
}

type session struct {
	ID, URL, Mode, Currency, Status string
	AmountTotal                     int64  `json:"amount_total"`
	PaymentStatus                   string `json:"payment_status"`
	PaymentIntent                   string `json:"payment_intent"`
	ClientReference                 string `json:"client_reference_id"`
	Metadata                        map[string]string
	LiveMode                        bool `json:"livemode"`
}

func (s *Stripe) Checkout(ctx context.Context, user domain.User, cents int64) (string, error) {
	if !s.Enabled() || cents < 500 || cents > 100000 {
		return "", errors.New("invalid checkout")
	}
	orderID := id("top_")
	_, e := s.Store.DB.ExecContext(ctx, `INSERT INTO topup_orders(id,tenant_id,amount_micros,currency,status,created_at,updated_at) VALUES(?,?,?,'usd','pending',NOW(6),NOW(6))`, orderID, user.TenantID, cents*10000)
	if e != nil {
		return "", e
	}
	form := url.Values{"mode": {"payment"}, "success_url": {s.AppURL + "/app/billing?checkout=success"}, "cancel_url": {s.AppURL + "/app/billing?checkout=cancelled"}, "client_reference_id": {orderID}, "customer_email": {user.Email}, "line_items[0][quantity]": {"1"}, "line_items[0][price_data][currency]": {"usd"}, "line_items[0][price_data][unit_amount]": {strconv.FormatInt(cents, 10)}, "line_items[0][price_data][product_data][name]": {"RelayPine API credit"}, "metadata[order_id]": {orderID}, "metadata[tenant_id]": {user.TenantID}, "payment_intent_data[metadata][order_id]": {orderID}, "payment_intent_data[metadata][tenant_id]": {user.TenantID}}
	var result session
	if e = s.call(ctx, "POST", "/v1/checkout/sessions", form, "checkout:"+orderID, &result); e != nil {
		return "", e
	}
	u, e := url.Parse(result.URL)
	if e != nil || result.ID == "" || u.Scheme != "https" || u.Hostname() != "checkout.stripe.com" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return "", errors.New("invalid checkout URL")
	}
	_, e = s.Store.DB.ExecContext(ctx, `UPDATE topup_orders SET session_id=?,updated_at=NOW(6) WHERE id=? AND (session_id IS NULL OR session_id=?)`, result.ID, orderID, result.ID)
	return result.URL, e
}
func VerifySignature(payload []byte, header, secret string, now time.Time) bool {
	if secret == "" {
		return false
	}
	var stamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if kv[0] == "t" {
			stamp = kv[1]
		}
		if kv[0] == "v1" {
			signatures = append(signatures, kv[1])
		}
	}
	t, e := strconv.ParseInt(stamp, 10, 64)
	if e != nil || t < now.Unix()-300 || t > now.Unix()+300 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp + "."))
	mac.Write(payload)
	expected := mac.Sum(nil)
	for _, sig := range signatures {
		raw, e := hex.DecodeString(sig)
		if e == nil && hmac.Equal(raw, expected) {
			return true
		}
	}
	return false
}

type event struct {
	ID, Type string
	LiveMode bool `json:"livemode"`
	Data     struct{ Object json.RawMessage }
}

func (s *Stripe) live() bool {
	return strings.HasPrefix(s.Secret, "sk_live_") || strings.HasPrefix(s.Secret, "rk_live_")
}
func (s *Stripe) Webhook(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled() {
		http.Error(w, "Payments unavailable", 503)
		return
	}
	payload, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if e != nil || !VerifySignature(payload, r.Header.Get("Stripe-Signature"), s.WebhookSecret, time.Now()) {
		http.Error(w, "Invalid signature", 400)
		return
	}
	var ev event
	if json.Unmarshal(payload, &ev) != nil || ev.ID == "" || len(ev.ID) > 255 || ev.LiveMode != s.live() {
		http.Error(w, "Invalid event", 400)
		return
	}
	_, e = s.Store.DB.ExecContext(r.Context(), `INSERT INTO payment_events(id,event_type,payload,status,created_at,updated_at) VALUES(?,?,?,'pending',NOW(6),NOW(6)) ON DUPLICATE KEY UPDATE id=id`, ev.ID, ev.Type, string(payload))
	if e != nil {
		http.Error(w, "Unable to persist event", 503)
		return
	}
	// Durable inbox is acknowledged. Worker retries any processing failure.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"received":true}`))
}
func (s *Stripe) ProcessPending(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	rows, e := s.Store.DB.QueryContext(ctx, `SELECT id,payload FROM payment_events WHERE status='pending' ORDER BY updated_at LIMIT 50`)
	if e != nil {
		return e
	}
	type item struct {
		id      string
		payload []byte
	}
	items := []item{}
	for rows.Next() {
		var it item
		if e = rows.Scan(&it.id, &it.payload); e != nil {
			rows.Close()
			return e
		}
		items = append(items, it)
	}
	rows.Close()
	for _, it := range items {
		var ev event
		if json.Unmarshal(it.payload, &ev) != nil {
			continue
		}
		e = s.process(ctx, ev)
		if e != nil {
			_, _ = s.Store.DB.ExecContext(ctx, `UPDATE payment_events SET attempts=attempts+1,updated_at=NOW(6),last_error='Processing failed; will retry' WHERE id=? AND status='pending'`, it.id)
			continue
		}
	}
	return nil
}
func (s *Stripe) process(ctx context.Context, ev event) error {
	var verified *session
	if ev.Type == "checkout.session.completed" || ev.Type == "checkout.session.async_payment_succeeded" {
		var obj session
		if json.Unmarshal(ev.Data.Object, &obj) != nil || obj.ID == "" {
			return errors.New("invalid session")
		}
		var current session
		if e := s.call(ctx, "GET", "/v1/checkout/sessions/"+url.PathEscape(obj.ID), nil, "", &current); e != nil {
			return e
		}
		verified = &current
	}
	tx, e := s.Store.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var status string
	if e = tx.QueryRowContext(ctx, `SELECT status FROM payment_events WHERE id=? FOR UPDATE`, ev.ID).Scan(&status); e != nil {
		return e
	}
	if status == "processed" {
		return nil
	}
	if verified != nil {
		if e = s.creditSession(ctx, tx, *verified); e != nil {
			return e
		}
	}
	if ev.Type == "charge.dispute.created" || ev.Type == "charge.dispute.closed" || ev.Type == "charge.refunded" {
		// Freeze affected accounts for reviewed reconciliation, never silently claw back spent credit.
		var obj struct {
			PaymentIntent  string `json:"payment_intent"`
			Charge         string `json:"charge"`
			AmountRefunded int64  `json:"amount_refunded"`
		}
		if e = json.Unmarshal(ev.Data.Object, &obj); e != nil {
			return e
		}
		if obj.PaymentIntent == "" && obj.Charge != "" {
			var charge struct {
				PaymentIntent string `json:"payment_intent"`
			}
			if e = s.call(ctx, "GET", "/v1/charges/"+url.PathEscape(obj.Charge), nil, "", &charge); e != nil {
				return e
			}
			obj.PaymentIntent = charge.PaymentIntent
		}
		if obj.PaymentIntent != "" {
			// A refund issued through this service already holds the corresponding wallet funds.
			expected := false
			if ev.Type == "charge.refunded" {
				expected, e = s.expectedRefund(ctx, tx, obj.PaymentIntent, obj.AmountRefunded)
				if e != nil {
					return e
				}
			}
			if !expected {
				_, e = tx.ExecContext(ctx, `UPDATE tenants SET status='suspended' WHERE id IN(SELECT tenant_id FROM topup_orders WHERE payment_id=?)`, obj.PaymentIntent)
			}
			if e != nil {
				return e
			}
		}
	}
	if ev.Type == "refund.created" || ev.Type == "refund.updated" || ev.Type == "refund.failed" {
		var obj refundResult
		if json.Unmarshal(ev.Data.Object, &obj) != nil {
			return errors.New("invalid refund")
		}
		var current refundResult
		if e = s.call(ctx, "GET", "/v1/refunds/"+url.PathEscape(obj.ID), nil, "", &current); e != nil {
			return e
		}
		if e = s.finishRefund(ctx, tx, current); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE payment_events SET status='processed',attempts=attempts+1,last_error=NULL,updated_at=NOW(6) WHERE id=?`, ev.ID); e != nil {
		return e
	}
	return tx.Commit()
}

// A local pending refund alone is insufficient evidence that a charge.refunded
// event was initiated here. Match the amount and current Stripe refund identity
// before exempting it from external-refund review.
func (s *Stripe) expectedRefund(ctx context.Context, tx *sql.Tx, payment string, refundedCents int64) (bool, error) {
	var order, state string
	var amount int64
	err := tx.QueryRowContext(ctx, `SELECT r.order_id,r.amount_micros,r.status FROM payment_refunds r JOIN topup_orders o ON o.id=r.order_id WHERE o.payment_id=? AND r.status NOT IN('failed','canceled')`, payment).Scan(&order, &amount, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if refundedCents <= 0 || amount%10000 != 0 || refundedCents != amount/10000 {
		return false, nil
	}
	if state == "succeeded" {
		return true, nil
	}
	var list struct {
		Data    []refundResult
		HasMore bool `json:"has_more"`
	}
	if err = s.call(ctx, "GET", "/v1/refunds?payment_intent="+url.QueryEscape(payment)+"&limit=100", nil, "", &list); err != nil {
		return false, err
	}
	if list.HasMore {
		return false, nil
	}
	var found *refundResult
	for _, refund := range list.Data {
		if refund.Status == "failed" || refund.Status == "canceled" {
			continue
		}
		if refund.Metadata["order_id"] != order || refund.PaymentIntent != payment || refund.Amount != refundedCents || found != nil {
			return false, nil
		}
		copy := refund
		found = &copy
	}
	if found == nil {
		return false, nil
	}
	if err = s.finishRefund(ctx, tx, *found); err != nil {
		return false, err
	}
	return true, nil
}
func (s *Stripe) creditSession(ctx context.Context, tx *sql.Tx, v session) error {
	if v.PaymentStatus != "paid" {
		if v.Status == "expired" {
			_, err := tx.ExecContext(ctx, `UPDATE topup_orders SET status='expired',updated_at=NOW(6) WHERE session_id=? AND status='pending'`, v.ID)
			return err
		}
		return nil
	}
	if v.LiveMode != s.live() || v.Mode != "payment" || v.Currency != "usd" || v.PaymentIntent == "" {
		return errors.New("payment mismatch")
	}
	orderID := v.Metadata["order_id"]
	var tenant, status string
	var amount int64
	var existing sql.NullString
	if e := tx.QueryRowContext(ctx, `SELECT tenant_id,amount_micros,status,session_id FROM topup_orders WHERE id=? FOR UPDATE`, orderID).Scan(&tenant, &amount, &status, &existing); e != nil {
		return e
	}
	if tenant != v.Metadata["tenant_id"] || v.ClientReference != orderID || amount <= 0 || amount%10000 != 0 || v.AmountTotal != amount/10000 || (existing.Valid && existing.String != v.ID) {
		return errors.New("order mismatch")
	}
	if status != "pending" {
		return nil
	}
	// Checkout remains paid after a refund. Retrieve the current charge before granting credit.
	var intent struct {
		Status         string
		Currency       string
		AmountReceived int64 `json:"amount_received"`
		LiveMode       bool  `json:"livemode"`
		LatestCharge   struct {
			Paid           bool
			Disputed       bool
			Refunded       bool
			AmountRefunded int64 `json:"amount_refunded"`
		} `json:"latest_charge"`
	}
	if err := s.call(ctx, "GET", "/v1/payment_intents/"+url.PathEscape(v.PaymentIntent)+"?expand[]=latest_charge", nil, "", &intent); err != nil {
		return err
	}
	if intent.Status != "succeeded" || intent.Currency != "usd" || intent.AmountReceived != v.AmountTotal || intent.LiveMode != s.live() || !intent.LatestCharge.Paid {
		return errors.New("payment intent mismatch")
	}
	if intent.LatestCharge.Disputed || intent.LatestCharge.Refunded || intent.LatestCharge.AmountRefunded > 0 {
		_, err := tx.ExecContext(ctx, `UPDATE topup_orders SET status='review_required',session_id=?,payment_id=?,updated_at=NOW(6) WHERE id=?`, v.ID, v.PaymentIntent, orderID)
		return err
	}
	if e := s.Store.CreditTx(ctx, tx, tenant, amount, "stripe:"+v.PaymentIntent, "Stripe prepaid API credit"); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, `UPDATE topup_orders SET status='paid',session_id=?,payment_id=?,updated_at=NOW(6) WHERE id=?`, v.ID, v.PaymentIntent, orderID)
	return e
}

type refundResult struct {
	ID, Status    string
	Amount        int64
	Currency      string
	PaymentIntent string `json:"payment_intent"`
	Metadata      map[string]string
}

func (s *Stripe) Refund(ctx context.Context, orderID, actor string) (string, error) {
	if !s.Enabled() {
		return "", errors.New("payments unavailable")
	}
	tx, e := s.Store.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	var tenant, status, payment string
	var amount int64
	var created time.Time
	var existingRefund sql.NullString
	if e = tx.QueryRowContext(ctx, `SELECT tenant_id,status,payment_id,amount_micros,created_at FROM topup_orders WHERE id=? FOR UPDATE`, orderID).Scan(&tenant, &status, &payment, &amount, &created); e != nil {
		return "", e
	}
	if status != "paid" && status != "refund_pending" {
		return status, errors.New("order not refundable")
	}
	if status == "paid" {
		var balance, held int64
		if e = tx.QueryRowContext(ctx, `SELECT balance_micros,held_micros FROM wallets WHERE tenant_id=? FOR UPDATE`, tenant).Scan(&balance, &held); e != nil {
			return "", e
		}
		var used int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ledger_entries WHERE tenant_id=? AND amount_micros<0 AND created_at>=?`, tenant, created).Scan(&used); e != nil {
			return "", e
		}
		if held != 0 || balance < amount || used > 0 {
			return "", errors.New("topup already used or held")
		}
		if _, e = tx.ExecContext(ctx, `UPDATE wallets SET held_micros=held_micros+? WHERE tenant_id=?`, amount, tenant); e != nil {
			return "", e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO payment_refunds(order_id,status,amount_micros,actor_id) VALUES(?,'pending',?,?)`, orderID, amount, actor); e != nil {
			return "", e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE topup_orders SET status='refund_pending',updated_at=NOW(6) WHERE id=?`, orderID); e != nil {
			return "", e
		}
	} else {
		if e = tx.QueryRowContext(ctx, `SELECT refund_id FROM payment_refunds WHERE order_id=? FOR UPDATE`, orderID).Scan(&existingRefund); e != nil {
			return "", e
		}
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	var result refundResult
	if existingRefund.Valid && existingRefund.String != "" {
		// Stripe replays the original idempotent POST response. Poll the current
		// object so a pending refund can complete even when its webhook was lost.
		e = s.call(ctx, "GET", "/v1/refunds/"+url.PathEscape(existingRefund.String), nil, "", &result)
	} else {
		if status == "refund_pending" {
			// The first POST may have succeeded before its response was lost. Find
			// that refund before retrying, including after Stripe's 24h key expiry.
			var list struct {
				Data    []refundResult
				HasMore bool `json:"has_more"`
			}
			e = s.call(ctx, "GET", "/v1/refunds?payment_intent="+url.QueryEscape(payment)+"&limit=100", nil, "", &list)
			if e != nil {
				return "pending", e
			}
			if list.HasMore {
				return "pending", errors.New("refund history requires manual review")
			}
			for _, v := range list.Data {
				if v.Metadata["order_id"] == orderID {
					if result.ID != "" && result.ID != v.ID {
						return "pending", errors.New("multiple refunds require manual review")
					}
					result = v
				}
			}
		}
		if result.ID == "" {
			e = s.call(ctx, "POST", "/v1/refunds", url.Values{"payment_intent": {payment}, "amount": {strconv.FormatInt(amount/10000, 10)}, "metadata[order_id]": {orderID}}, "refund:"+orderID, &result)
		}
	}
	if e != nil {
		return "pending", e
	}
	tx, e = s.Store.DB.BeginTx(ctx, nil)
	if e != nil {
		return "pending", e
	}
	defer tx.Rollback()
	if e = s.finishRefund(ctx, tx, result); e != nil {
		return "pending", e
	}
	if e = tx.Commit(); e != nil {
		return "pending", e
	}
	return result.Status, nil
}
func (s *Stripe) finishRefund(ctx context.Context, tx *sql.Tx, v refundResult) error {
	orderID := v.Metadata["order_id"]
	if orderID == "" {
		return nil
	}
	var tenant, payment string
	var amount int64
	if e := tx.QueryRowContext(ctx, `SELECT tenant_id,payment_id,amount_micros FROM topup_orders WHERE id=? FOR UPDATE`, orderID).Scan(&tenant, &payment, &amount); e != nil {
		return e
	}
	if v.ID == "" || v.PaymentIntent != payment || v.Currency != "usd" || amount <= 0 || amount%10000 != 0 || v.Amount != amount/10000 {
		return errors.New("refund mismatch")
	}
	switch v.Status {
	case "pending", "requires_action", "succeeded", "failed", "canceled":
	default:
		return errors.New("invalid refund status")
	}
	var state string
	if e := tx.QueryRowContext(ctx, `SELECT status FROM payment_refunds WHERE order_id=? FOR UPDATE`, orderID).Scan(&state); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		return e
	}
	if state == "succeeded" || state == "failed" || state == "canceled" {
		return nil
	}
	if v.Status == "succeeded" {
		res, e := tx.ExecContext(ctx, `UPDATE wallets SET balance_micros=balance_micros-?,held_micros=held_micros-? WHERE tenant_id=? AND balance_micros>=? AND held_micros>=?`, amount, amount, tenant, amount, amount)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return errors.New("refund hold missing")
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO ledger_entries(id,tenant_id,kind,amount_micros,reference,reason,created_at) VALUES(?,?,'refund',?,?,'Stripe full unused-credit refund',NOW(6))`, id("led_"), tenant, -amount, "refund:"+orderID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE topup_orders SET status='refunded',updated_at=NOW(6) WHERE id=?`, orderID); e != nil {
			return e
		}
	} else if v.Status == "failed" || v.Status == "canceled" {
		res, e := tx.ExecContext(ctx, `UPDATE wallets SET held_micros=held_micros-? WHERE tenant_id=? AND held_micros>=?`, amount, tenant, amount)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("refund hold missing")
		}
		if _, e := tx.ExecContext(ctx, `UPDATE topup_orders SET status='refund_failed',updated_at=NOW(6) WHERE id=?`, orderID); e != nil {
			return e
		}
	}
	_, e := tx.ExecContext(ctx, `UPDATE payment_refunds SET refund_id=?,status=? WHERE order_id=?`, v.ID, v.Status, orderID)
	return e
}
func (s *Stripe) Reconcile(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	rows, e := s.Store.DB.QueryContext(ctx, `SELECT id,session_id,status FROM topup_orders WHERE (status='pending' AND session_id IS NOT NULL) OR status='refund_pending' ORDER BY updated_at LIMIT 50`)
	if e != nil {
		return e
	}
	type item struct{ id, session, status string }
	items := []item{}
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.id, &v.session, &v.status); e != nil {
			rows.Close()
			return e
		}
		items = append(items, v)
	}
	rows.Close()
	for _, v := range items {
		_, _ = s.Store.DB.ExecContext(ctx, `UPDATE topup_orders SET updated_at=NOW(6) WHERE id=?`, v.id)
		if v.status == "refund_pending" {
			_, _ = s.Refund(ctx, v.id, "reconciliation")
			continue
		}
		var result session
		if s.call(ctx, "GET", "/v1/checkout/sessions/"+url.PathEscape(v.session), nil, "", &result) != nil {
			continue
		}
		tx, e := s.Store.DB.BeginTx(ctx, nil)
		if e != nil {
			continue
		}
		if s.creditSession(ctx, tx, result) == nil {
			_ = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	return nil
}

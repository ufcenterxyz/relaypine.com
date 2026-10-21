package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

type Server struct {
	Store        *store.Store
	Redis        *redis.Client
	Relay        http.Handler
	AppURL       string
	AllowPrivate bool
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/openai/", s.Relay)
	mux.Handle("/anthropic/", s.Relay)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"status": "ok", "service": "relaypine"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second*2)
		defer cancel()
		if s.Store.DB.PingContext(ctx) != nil || s.Redis.Ping(ctx).Err() != nil {
			fail(w, 503, "unavailable", "Service dependencies are unavailable.")
			return
		}
		write(w, 200, map[string]any{"status": "ready"})
	})
	mux.HandleFunc("/api/", s.console)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func read(r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON required")
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(payload) > 1<<20 {
		return errors.New("request body too large")
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("one JSON object required")
	}
	return nil
}
func (s *Server) report(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		fail(w, 401, "unauthorized", "Your session has expired. Sign in again.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, 404, "not_found", "This item was not found.")
	case errors.Is(err, store.ErrInsufficientFunds), errors.Is(err, store.ErrBudgetExceeded):
		fail(w, 409, "insufficient_balance", "There is not enough available balance for this operation.")
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "conflict", "This operation conflicts with the current state.")
	default:
		fail(w, 400, "invalid_request", "The request could not be completed. Check the values and try again.")
	}
}
func (s *Server) cookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: "rp_session", Value: token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.AppURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (s *Server) authLimit(ctx context.Context, r *http.Request, email string) bool {
	return s.publicLimit(ctx, r, email, "auth")
}
func (s *Server) publicLimit(ctx context.Context, r *http.Request, email, scope string) bool {
	if s.Redis == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	hash := func(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
	email = store.NormalizeEmail(email)
	prefix := "rp:" + scope + ":"
	keys := []string{prefix + "pair:" + hash(host+"|"+email), prefix + "ip:" + hash(host), prefix + "email:" + hash(email)}
	v, e := s.Redis.Eval(ctx, `local allowed=1; local limits={30,100,40}; for i,k in ipairs(KEYS) do local n=redis.call('INCR',k); if n==1 then redis.call('EXPIRE',k,600) end; if n>limits[i] then allowed=0 end end; return allowed`, keys).Int()
	return e == nil && v == 1
}
func (s *Server) console(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/billing/checkout" || path == "/api/admin/refunds" || strings.HasPrefix(path, "/api/payments/") {
		fail(w, 404, "not_found", "API route not found.")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != s.AppURL {
		fail(w, 403, "origin_rejected", "This request must come from the RelayPine console.")
		return
	}
	if path == "/api/waitlist" || path == "/api/auth/register" {
		if r.Method != "POST" {
			fail(w, 405, "method_not_allowed", "Use POST.")
			return
		}
		var v struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			// Legacy clients may send a password. It is never stored or used.
			Password string `json:"password"`
		}
		if read(r, &v) != nil || !store.ValidEmail(store.NormalizeEmail(v.Email)) || len(strings.TrimSpace(v.Name)) > 100 {
			fail(w, 400, "invalid_waitlist", "Enter a valid email and an optional name up to 100 bytes.")
			return
		}
		if !s.publicLimit(r.Context(), r, v.Email, "waitlist") {
			fail(w, 429, "rate_limited", "Too many requests. Try again later.")
			return
		}
		if err := s.Store.JoinWaitlist(r.Context(), v.Email, v.Name); err != nil {
			s.report(w, err)
			return
		}
		write(w, 202, map[string]string{"status": "waitlisted"})
		return
	}
	if path == "/api/auth/login" {
		if r.Method != "POST" {
			fail(w, 405, "method_not_allowed", "Use POST.")
			return
		}
		var v struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if read(r, &v) != nil || !store.ValidEmail(store.NormalizeEmail(v.Email)) || len(v.Password) < 10 || len(v.Password) > 72 {
			fail(w, 400, "invalid_credentials", "Use a valid email and a password between 10 and 72 bytes.")
			return
		}
		if !s.authLimit(r.Context(), r, v.Email) {
			fail(w, 429, "rate_limited", "Too many sign-in attempts. Try again later.")
			return
		}
		if store.NormalizeEmail(v.Email) != store.SuperAdminEmail {
			fail(w, 403, "waitlist_required", "RelayPine is in private preview. Join the waitlist for access.")
			return
		}
		user, e := s.Store.Authenticate(r.Context(), v.Email, v.Password)
		if e != nil || !store.IsSuperAdmin(user) {
			fail(w, 401, "authentication_failed", "Unable to sign in. Check your administrator credentials. Public access is by waitlist.")
			return
		}
		token, e := s.Store.CreateSession(r.Context(), user.ID)
		if e != nil {
			s.report(w, e)
			return
		}
		s.cookie(w, token, 60*60*24*7)
		write(w, 200, map[string]any{"user": user})
		return
	}
	ck, e := r.Cookie("rp_session")
	if e != nil {
		fail(w, 401, "unauthorized", "Sign in to continue.")
		return
	}
	user, e := s.Store.SessionUser(r.Context(), ck.Value)
	if e != nil {
		s.report(w, e)
		return
	}
	switch {
	case path == "/api/auth/logout" && r.Method == "POST":
		if e = s.Store.DeleteSession(r.Context(), ck.Value); e != nil {
			s.report(w, e)
			return
		}
		s.cookie(w, "", -1)
		write(w, 200, map[string]bool{"ok": true})
	case path == "/api/auth/me" && r.Method == "GET":
		write(w, 200, map[string]any{"user": user})
	case path == "/api/state" && r.Method == "GET":
		snap, e := s.Store.Snapshot(r.Context(), user)
		if e != nil {
			s.report(w, e)
			return
		}
		write(w, 200, snap)
	case path == "/api/keys" && r.Method == "POST":
		var v struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
			RPM    int      `json:"rpm"`
		}
		if read(r, &v) != nil || strings.TrimSpace(v.Name) == "" || len(v.Name) > 100 || v.RPM < 1 || v.RPM > 10000 || len(v.Models) > 100 {
			fail(w, 400, "invalid_key", "Enter a name and a request limit between 1 and 10,000.")
			return
		}
		key, secret, e := s.Store.CreateKey(r.Context(), user, v.Name, v.Models, v.RPM)
		if e != nil {
			s.report(w, e)
			return
		}
		write(w, 201, map[string]any{"key": key, "secret": secret})
	case strings.HasPrefix(path, "/api/keys/") && r.Method == "DELETE":
		e = s.Store.RevokeKey(r.Context(), user.TenantID, strings.TrimPrefix(path, "/api/keys/"))
		if e != nil {
			s.report(w, e)
			return
		}
		write(w, 200, map[string]bool{"ok": true})
	case strings.HasPrefix(path, "/api/admin/"):
		if !store.IsSuperAdmin(user) {
			fail(w, 403, "forbidden", "Operator access is required.")
			return
		}
		if r.Method != "POST" {
			fail(w, 405, "method_not_allowed", "Use POST.")
			return
		}
		s.admin(w, r, user, path)
	default:
		fail(w, 404, "not_found", "API route not found.")
	}
}
func validProvider(p string) bool { return p == "openai" || p == "anthropic" }
func nameOK(s string) bool        { return strings.TrimSpace(s) != "" && len(s) <= 120 }
func (s *Server) admin(w http.ResponseWriter, r *http.Request, user domain.User, path string) {
	ctx := r.Context()
	switch path {
	case "/api/admin/endpoints":
		var v domain.Endpoint
		if read(r, &v) != nil || !nameOK(v.Name) || !validProvider(v.Provider) || len(v.Models) > 200 {
			fail(w, 400, "invalid_endpoint", "Enter a name, provider and supported models.")
			return
		}
		if v.BaseURL == "" {
			if v.Provider == "openai" {
				v.BaseURL = "https://api.openai.com/v1"
			} else {
				v.BaseURL = "https://api.anthropic.com/v1"
			}
		}
		u, e := url.Parse(v.BaseURL)
		if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(s.AllowPrivate && u.Scheme == "http")) {
			fail(w, 400, "invalid_url", "Use an HTTPS API base URL without credentials or query parameters.")
			return
		}
		if v.AuthMode == "" {
			v.AuthMode = "bearer"
			if v.Provider == "anthropic" {
				v.AuthMode = "x-api-key"
			}
		}
		if v.AuthMode != "bearer" && v.AuthMode != "x-api-key" {
			fail(w, 400, "invalid_auth", "Choose Bearer or x-api-key authentication.")
			return
		}
		value, e := s.Store.SaveEndpoint(ctx, v)
		if e != nil {
			s.report(w, e)
			return
		}
		write(w, 200, value)
	case "/api/admin/credentials":
		var v struct {
			domain.Credential
			SecretValue string `json:"secret"`
		}
		if read(r, &v) != nil || !nameOK(v.Name) || v.EndpointID == "" || v.RPM < 1 || v.RPM > 100000 || v.TPM < 1 || v.TPM > 100000000 || v.MaxConcurrency < 1 || v.MaxConcurrency > 1000 || v.BudgetMicros < 0 || v.BudgetMicros > 1000000000000 || v.Priority < 0 || v.Priority > 1000 || !nameOK(v.QuotaGroup) || len(v.SecretValue) > 4096 {
			fail(w, 400, "invalid_credential", "Enter a name, endpoint, quota group and valid capacity limits.")
			return
		}
		v.Secret = v.SecretValue
		value, e := s.Store.SaveCredential(ctx, v.Credential)
		if e != nil {
			s.report(w, e)
			return
		}
		write(w, 200, value)
	case "/api/admin/proxies":
		var v struct {
			domain.Proxy
			URLValue string `json:"url"`
		}
		if read(r, &v) != nil || !nameOK(v.Name) {
			fail(w, 400, "invalid_proxy", "Enter a proxy name and SOCKS5 URL.")
			return
		}
		if v.URLValue != "" {
			u, e := url.Parse(v.URLValue)
			if e != nil || (u.Scheme != "socks5" && u.Scheme != "socks5h") || u.Hostname() == "" || u.Port() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
				fail(w, 400, "invalid_proxy", "Use socks5://user:password@host:port.")
				return
			}
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				fail(w, 400, "invalid_proxy", "Proxy port must be between 1 and 65,535.")
				return
			}
		} else if v.ID == "" {
			fail(w, 400, "invalid_proxy", "Enter a SOCKS5 URL.")
			return
		}
		v.URL = v.URLValue
		value, e := s.Store.SaveProxy(ctx, v.Proxy)
		if e != nil {
			s.report(w, e)
			return
		}
		write(w, 200, value)
	case "/api/admin/models":
		var v domain.Model
		if read(r, &v) != nil || (!nameOK(v.ID) || len(v.ID) > 64) || !nameOK(v.Name) || !validProvider(v.Provider) || !nameOK(v.UpstreamModel) || v.MaxOutputTokens < 1 || v.MaxOutputTokens > 65536 {
			fail(w, 400, "invalid_model", "Enter a model ID, provider and output cap between 1 and 65,536.")
			return
		}
		rates := []int64{v.InputRate, v.OutputRate, v.CachedRate, v.CacheWriteRate, v.CostInputRate, v.CostOutputRate, v.CostCachedRate, v.CostCacheWriteRate}
		for _, rate := range rates {
			if rate < 0 || rate > 1000000000 {
				fail(w, 400, "invalid_price", "Rates must be between 0 and $1,000 per million tokens.")
				return
			}
		}
		if v.Enabled && (v.InputRate == 0 || v.OutputRate == 0) {
			fail(w, 400, "invalid_price", "Enabled models require nonzero input and output sales rates.")
			return
		}
		value, e := s.Store.SaveModel(ctx, v)
		if e != nil {
			s.report(w, e)
			return
		}
		write(w, 200, value)
	case "/api/admin/routing":
		var v domain.RouteConfig
		if read(r, &v) != nil || (v.Strategy != "least_load" && v.Strategy != "highest_balance" && v.Strategy != "balanced") || v.TenantRPM < 1 || v.TenantRPM > 100000 || v.TenantConcurrency < 1 || v.TenantConcurrency > 1000 {
			fail(w, 400, "invalid_routing", "Choose a strategy and positive tenant capacity limits.")
			return
		}
		if e := s.Store.SaveRouting(ctx, v); e != nil {
			s.report(w, e)
			return
		}
		write(w, 200, v)
	case "/api/admin/credits":
		var v struct {
			TenantID  string `json:"tenant_id"`
			Amount    int64  `json:"amount_micros"`
			Reason    string `json:"reason"`
			Reference string `json:"idempotency_key"`
		}
		if read(r, &v) != nil || v.TenantID == "" || v.Amount <= 0 || v.Amount > 1000000000 || len(strings.TrimSpace(v.Reason)) < 5 || len(v.Reason) > 300 || len(v.Reference) < 16 || len(v.Reference) > 80 {
			fail(w, 400, "invalid_credit", "Enter a positive credit up to $1,000, a reason, and an idempotency key.")
			return
		}
		if e := s.Store.Credit(ctx, v.TenantID, v.Amount, "adjustment:"+v.Reference, "Operator "+user.ID+": "+v.Reason); e != nil {
			s.report(w, e)
			return
		}
		write(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 404, "not_found", "Operator API route not found.")
	}
}

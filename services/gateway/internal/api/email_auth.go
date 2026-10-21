package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/store"
)

// CodeSender must deliver only to the normalized recipient supplied by the server.
// Codes must never be returned by the API, written to logs or persisted in plaintext.
type CodeSender interface {
	SendCode(context.Context, string, string) error
}

type EmailAuth struct {
	redis  *redis.Client
	sender CodeSender
	key    []byte
}

func NewEmailAuth(client *redis.Client, sender CodeSender, masterKey []byte) (*EmailAuth, error) {
	if client == nil || len(masterKey) != 32 {
		return nil, errors.New("email authentication requires Redis and a 32-byte key")
	}
	mac := hmac.New(sha256.New, masterKey)
	mac.Write([]byte("relaypine/email-otp/v1"))
	return &EmailAuth{redis: client, sender: sender, key: mac.Sum(nil)}, nil
}
func emailHash(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:])
}
func challengeKey(email string) string { return "rp:otp:{" + emailHash(email) + "}:challenge" }
func cooldownKey(email string) string  { return "rp:otp:{" + emailHash(email) + "}:cooldown" }
func (a *EmailAuth) digest(email, id, code string, surfaces ...string) string {
	surface := ""
	if len(surfaces) > 0 {
		surface = surfaces[0]
	}
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(email + "\x00" + id + "\x00" + code + "\x00" + surface))
	return hex.EncodeToString(mac.Sum(nil))
}
func rateLimited(w http.ResponseWriter, seconds int) {
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	write(w, 429, map[string]any{"error": map[string]any{"code": "rate_limited", "message": "Too many requests. Try again after the countdown.", "retry_after": seconds}})
}

const issueScript = `
 local remaining=redis.call('TTL',KEYS[2]);if remaining>=0 then return math.max(remaining,1) end
 local count=redis.call('INCR',KEYS[3]);if count==1 then redis.call('EXPIRE',KEYS[3],3600) end
 if count>10 then return math.max(redis.call('TTL',KEYS[3]),1) end
 redis.call('SET',KEYS[2],ARGV[1],'EX',60)
 redis.call('DEL',KEYS[1])
 redis.call('HSET',KEYS[1],'id',ARGV[1],'digest',ARGV[2],'name',ARGV[3],'attempts',0,'ready',0,'surface',ARGV[4])
 redis.call('EXPIRE',KEYS[1],600)
 return 0`
const finishSendScript = `
 if redis.call('HGET',KEYS[1],'id')~=ARGV[1] then return 0 end
 if ARGV[2]=='sent' then redis.call('HSET',KEYS[1],'ready',1) else redis.call('DEL',KEYS[1]) end
 return 1`
const verifyScript = `
 if redis.call('HGET',KEYS[1],'id')~=ARGV[1] or redis.call('HGET',KEYS[1],'ready')~='1' or redis.call('HGET',KEYS[1],'surface')~=ARGV[3] then return {-1,''} end
 local attempts=redis.call('HINCRBY',KEYS[1],'attempts',1)
 if attempts>5 then redis.call('DEL',KEYS[1]);return {0,''} end
 if redis.call('HGET',KEYS[1],'digest')~=ARGV[2] then
  if attempts>=5 then redis.call('DEL',KEYS[1]) end
  return {0,''}
 end
 local name=redis.call('HGET',KEYS[1],'name') or ''
 redis.call('DEL',KEYS[1]);return {1,name}`

func (s *Server) requestCode(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if read(r, &v) != nil {
		fail(w, 400, "invalid_email", "Enter a valid email address and optional name.")
		return
	}
	v.Email, v.Name = store.NormalizeEmail(v.Email), strings.TrimSpace(v.Name)
	if !store.ValidEmail(v.Email) || len(v.Name) > 100 {
		fail(w, 400, "invalid_email", "Enter a valid email address and optional name up to 100 bytes.")
		return
	}
	if !s.publicLimit(r.Context(), r, v.Email, "otp-send") {
		rateLimited(w, 600)
		return
	}
	a := s.EmailAuth
	if a == nil || a.sender == nil {
		fail(w, 503, "email_unavailable", "Email delivery is not available. Please try again later.")
		return
	}
	codeValue, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		fail(w, 503, "auth_unavailable", "Email verification is temporarily unavailable.")
		return
	}
	code := fmt.Sprintf("%06d", codeValue.Int64())
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		fail(w, 503, "auth_unavailable", "Email verification is temporarily unavailable.")
		return
	}
	id := hex.EncodeToString(random)
	surface, _ := s.surface(r)
	keys := []string{challengeKey(v.Email), cooldownKey(v.Email), "rp:otp:{" + emailHash(v.Email) + "}:hour"}
	remaining, err := a.redis.Eval(r.Context(), issueScript, keys, id, a.digest(v.Email, id, code, surface), v.Name, surface).Int()
	if err != nil {
		fail(w, 503, "auth_unavailable", "Email verification is temporarily unavailable.")
		return
	}
	if remaining > 0 {
		rateLimited(w, remaining)
		return
	}
	sendCtx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	err = a.sender.SendCode(sendCtx, v.Email, code)
	cancel()
	outcome := "sent"
	if err != nil {
		outcome = "failed"
	}
	// A disconnected caller must not leave a delivered code in a pending state;
	// a failed send can remove only its own challenge, never a later resend.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
	finished, finishErr := a.redis.Eval(cleanupCtx, finishSendScript, []string{keys[0]}, id, outcome).Int()
	cleanupCancel()
	if err != nil || finishErr != nil || finished != 1 {
		fail(w, 503, "email_unavailable", "The code could not be sent. Please try again later.")
		return
	}
	write(w, 202, map[string]any{"status": "code_sent", "expires_in": 600, "resend_after": 60})
}
func (s *Server) verifyCode(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if read(r, &v) != nil {
		fail(w, 400, "invalid_code", "Enter the six-digit code from your email.")
		return
	}
	v.Email = store.NormalizeEmail(v.Email)
	if !store.ValidEmail(v.Email) {
		fail(w, 400, "invalid_email", "Enter a valid email address.")
		return
	}
	if !s.publicLimit(r.Context(), r, v.Email, "otp-verify") {
		rateLimited(w, 600)
		return
	}
	validCode := len(v.Code) == 6
	for _, c := range v.Code {
		if c < '0' || c > '9' {
			validCode = false
		}
	}
	if !validCode {
		fail(w, 400, "invalid_code", "Enter the six-digit code from your email.")
		return
	}
	a := s.EmailAuth
	if a == nil {
		fail(w, 503, "auth_unavailable", "Email verification is temporarily unavailable.")
		return
	}
	id, err := a.redis.HGet(r.Context(), challengeKey(v.Email), "id").Result()
	if errors.Is(err, redis.Nil) {
		fail(w, 401, "expired_code", "This code has expired or was already used. Request a new code.")
		return
	}
	if err != nil {
		fail(w, 503, "auth_unavailable", "Email verification is temporarily unavailable.")
		return
	}
	surface, _ := s.surface(r)
	result, err := a.redis.Eval(r.Context(), verifyScript, []string{challengeKey(v.Email)}, id, a.digest(v.Email, id, v.Code, surface), surface).Slice()
	if err != nil || len(result) != 2 {
		fail(w, 503, "auth_unavailable", "Email verification is temporarily unavailable.")
		return
	}
	status, ok := result[0].(int64)
	if !ok || status != 1 {
		fail(w, 401, "invalid_code", "The code is incorrect or no longer valid. Request a new code if needed.")
		return
	}
	name, _ := result[1].(string)
	if surface == "admin" && v.Email != s.Store.SuperAdminEmail() {
		fail(w, 403, "forbidden", "This account does not have access to this console.")
		return
	}
	user, err := s.Store.EmailUser(r.Context(), v.Email, name)
	if err != nil {
		fail(w, 503, "auth_unavailable", "Sign-in could not be completed. Please request a new code and try again.")
		return
	}
	token, err := s.Store.CreateSurfaceSession(r.Context(), user.ID, surface)
	if err != nil {
		fail(w, 503, "auth_unavailable", "Sign-in could not be completed. Please request a new code and try again.")
		return
	}
	s.cookie(w, r, token, 60*60*24*7)
	if surface == "customer" {
		user.Operator = false
	}
	response := s.surfaceInfo(surface)
	response["status"] = "authenticated"
	response["user"] = user
	write(w, 200, response)
}

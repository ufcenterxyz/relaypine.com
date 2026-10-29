// Package store owns the durable MySQL state and wallet transactions. Every
// instance uses the same database; no financial or authentication state is kept
// only in memory.
package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"relaypine.com/gateway/internal/domain"
)

var (
	ErrUnauthorized      = errors.New("unauthorized")
	ErrNotFound          = errors.New("not found")
	ErrInsufficientFunds = errors.New("insufficient available balance")
	ErrConflict          = errors.New("conflict")
	ErrBudgetExceeded    = errors.New("upstream budget exceeded")
)

//go:embed schema.sql
var schema string

// Config contains instance-specific policy. Every replica using the same
// database must use the same administrator identity and body retention.
type Config struct {
	SuperAdminEmail string
	// PayloadRetention is how long request and response bodies are kept.
	// Zero disables body capture and removes bodies that are already stored.
	PayloadRetention time.Duration
}

type Store struct {
	DB               *sql.DB
	aead             cipher.AEAD
	superAdminEmail  string
	payloadRetention time.Duration
}

func Open(ctx context.Context, dsn, masterKey string, config Config) (*Store, error) {
	adminEmail := NormalizeEmail(config.SuperAdminEmail)
	if !ValidEmail(adminEmail) {
		return nil, errors.New("SUPERADMIN_EMAIL must be a valid email address")
	}
	if config.PayloadRetention < 0 {
		return nil, errors.New("request body retention cannot be negative")
	}
	key, err := base64.StdEncoding.DecodeString(masterKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("MASTER_KEY must be base64 encoding of exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid MySQL DSN")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.MultiStatements = false
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["charset"] = "utf8mb4"
	cfg.Params["time_zone"] = "'+00:00'"
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	s := &Store{DB: db, aead: aead, superAdminEmail: adminEmail, payloadRetention: config.PayloadRetention}
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect MySQL: %w", err)
	}
	// MySQL DDL is auto-committed. IF NOT EXISTS permits a safe restart after any
	// interrupted creation, and an advisory lock serializes multiple instances.
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('relaypine_schema_v1',30)").Scan(&locked); err != nil || locked != 1 {
		db.Close()
		return nil, errors.New("cannot acquire schema migration lock")
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('relaypine_schema_v1')")
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err = conn.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize database schema: %w", err)
		}
	}
	// Existing installations default every old session to legacy. Only newly
	// verified mailbox sessions can pass the session lookup policy.
	var authColumn int
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='sessions' AND column_name='auth_method'").Scan(&authColumn); err != nil {
		db.Close()
		return nil, err
	}
	if authColumn == 0 {
		if _, err = conn.ExecContext(ctx, "ALTER TABLE sessions ADD COLUMN auth_method VARCHAR(32) NOT NULL DEFAULT 'legacy'"); err != nil {
			db.Close()
			return nil, err
		}
	}
	defaults := []domain.Endpoint{{ID: "openai-official", Name: "OpenAI official", Provider: "openai", BaseURL: "https://api.openai.com", AuthMode: "bearer", Enabled: true, Models: []string{}}, {ID: "anthropic-official", Name: "Anthropic official", Provider: "anthropic", BaseURL: "https://api.anthropic.com", AuthMode: "x-api-key", Enabled: true, Models: []string{}}}
	for _, endpoint := range defaults {
		raw, _ := json.Marshal(endpoint)
		if _, err = conn.ExecContext(ctx, "INSERT IGNORE INTO endpoints(id,config) VALUES(?,?)", endpoint.ID, raw); err != nil {
			db.Close()
			return nil, err
		}
	}
	raw, _ := json.Marshal(domain.RouteConfig{Strategy: "least_load", TenantRPM: 120, TenantConcurrency: 10})
	if _, err = conn.ExecContext(ctx, "INSERT IGNORE INTO settings(id,config) VALUES('routing',?)", raw); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }
func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("secure random source unavailable")
	}
	return prefix + hex.EncodeToString(b)
}
func newToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func tokenHash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func dbError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var e *mysql.MySQLError
	if errors.As(err, &e) && e.Number == 1062 {
		return ErrConflict
	}
	return err
}
func (s *Store) encrypt(value, scope string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(value), []byte(scope)), nil
}
func (s *Store) decrypt(value []byte, scope string) (string, error) {
	n := s.aead.NonceSize()
	if len(value) < n {
		return "", errors.New("invalid encrypted configuration")
	}
	p, err := s.aead.Open(nil, value[:n], value[n:], []byte(scope))
	if err != nil {
		return "", errors.New("configuration decryption failed")
	}
	return string(p), nil
}
func validID(id string) bool     { return len(id) > 0 && len(id) <= 64 }
func validName(name string) bool { return len(strings.TrimSpace(name)) > 0 && len(name) <= 200 }

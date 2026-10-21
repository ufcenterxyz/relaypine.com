package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"relaypine.com/gateway/internal/domain"
)

func (s *Store) register(ctx context.Context, email, name, password string, operator bool) (domain.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 254 || !validName(name) || len(password) < 10 || len(password) > 72 {
		return domain.User{}, errors.New("valid email, name, and password of 10–72 bytes required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return domain.User{}, err
	}
	user := domain.User{ID: newID("usr_"), TenantID: newID("ten_"), Email: email, Name: name, Operator: operator, PasswordHash: string(hash)}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, "INSERT INTO tenants(id,name,status,created_at) VALUES(?,?,'active',?)", user.TenantID, name, now); err != nil {
		return domain.User{}, dbError(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO users(id,tenant_id,email,name,password_hash,operator,created_at) VALUES(?,?,?,?,?,?,?)", user.ID, user.TenantID, email, name, string(hash), operator, now); err != nil {
		return domain.User{}, dbError(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO projects(id,tenant_id,name,created_at) VALUES(?,?,'Default project',?)", newID("prj_"), user.TenantID, now); err != nil {
		return domain.User{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO wallets(tenant_id,balance_micros,held_micros,currency) VALUES(?,0,0,'usd')", user.TenantID); err != nil {
		return domain.User{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.User{}, err
	}
	user.PasswordHash = ""
	return user, nil
}

func (s *Store) Authenticate(ctx context.Context, email, password string) (domain.User, error) {
	var user domain.User
	if NormalizeEmail(email) != SuperAdminEmail {
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO5u5DFX7IxtKxbmiQNsHq1bk.qKkTBza"), []byte(password))
		return user, ErrUnauthorized
	}
	err := s.DB.QueryRowContext(ctx, "SELECT u.id,u.tenant_id,u.email,u.name,u.operator,u.password_hash FROM users u JOIN tenants t ON t.id=u.tenant_id WHERE BINARY u.email=? AND u.operator=TRUE AND t.status='active'", strings.ToLower(strings.TrimSpace(email))).Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Operator, &user.PasswordHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { // Equalize missing-account password work.
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO5u5DFX7IxtKxbmiQNsHq1bk.qKkTBza"), []byte(password))
			return domain.User{}, ErrUnauthorized
		}
		return domain.User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return domain.User{}, ErrUnauthorized
	}
	user.PasswordHash = ""
	return user, nil
}

func (s *Store) CreateSession(ctx context.Context, userID string) (string, error) {
	token, err := newToken("rps_")
	if err != nil {
		return "", err
	}
	res, err := s.DB.ExecContext(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) SELECT ?,u.id,? FROM users u JOIN tenants t ON t.id=u.tenant_id WHERE u.id=? AND BINARY u.email=? AND u.operator=TRUE AND t.status='active'", tokenHash(token), time.Now().UTC().Add(7*24*time.Hour), userID, SuperAdminEmail)
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return "", ErrUnauthorized
	}
	return token, nil
}
func (s *Store) SessionUser(ctx context.Context, token string) (domain.User, error) {
	var u domain.User
	if !strings.HasPrefix(token, "rps_") || len(token) > 128 {
		return u, ErrUnauthorized
	}
	err := s.DB.QueryRowContext(ctx, "SELECT u.id,u.tenant_id,u.email,u.name,u.operator FROM sessions se JOIN users u ON u.id=se.user_id JOIN tenants t ON t.id=u.tenant_id WHERE se.token_hash=? AND se.expires_at>? AND BINARY u.email=? AND u.operator=TRUE AND t.status='active'", tokenHash(token), time.Now().UTC(), SuperAdminEmail).Scan(&u.ID, &u.TenantID, &u.Email, &u.Name, &u.Operator)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrUnauthorized
	}
	return u, err
}
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", tokenHash(token))
	return err
}

func (s *Store) CreateKey(ctx context.Context, user domain.User, name string, models []string, rpm int) (domain.AccessKey, string, error) {
	var key domain.AccessKey
	if !IsSuperAdmin(user) {
		return key, "", ErrUnauthorized
	}
	if !validName(name) || rpm < 1 || rpm > 1000000 || len(models) > 200 {
		return key, "", errors.New("invalid access key configuration")
	}
	for _, m := range models {
		if !validID(m) {
			return key, "", errors.New("invalid model permission")
		}
	}
	token, err := newToken("rp_live_")
	if err != nil {
		return key, "", err
	}
	if models == nil {
		models = []string{}
	}
	key = domain.AccessKey{ID: newID("key_"), TenantID: user.TenantID, Name: strings.TrimSpace(name), Prefix: token[:16], Hash: tokenHash(token), Enabled: true, RPM: rpm, Models: models, CreatedAt: time.Now().UTC()}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return key, "", err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, "SELECT p.id FROM projects p JOIN tenants t ON t.id=p.tenant_id JOIN users u ON u.tenant_id=t.id WHERE p.tenant_id=? AND u.id=? AND BINARY u.email=? AND u.operator=TRUE AND t.status='active' ORDER BY p.created_at,p.id LIMIT 1", user.TenantID, user.ID, SuperAdminEmail).Scan(&key.ProjectID)
	if err != nil {
		return domain.AccessKey{}, "", dbError(err)
	}
	raw, _ := json.Marshal(models)
	_, err = tx.ExecContext(ctx, "INSERT INTO access_keys(id,tenant_id,project_id,name,prefix,token_hash,enabled,rpm,models,created_at) VALUES(?,?,?,?,?,?,TRUE,?,?,?)", key.ID, key.TenantID, key.ProjectID, key.Name, key.Prefix, key.Hash, key.RPM, raw, key.CreatedAt)
	if err != nil {
		return domain.AccessKey{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return domain.AccessKey{}, "", err
	}
	key.Hash = ""
	return key, token, nil
}
func (s *Store) RevokeKey(ctx context.Context, tenantID, id string) error {
	result, err := s.DB.ExecContext(ctx, "UPDATE access_keys SET enabled=FALSE WHERE id=? AND tenant_id=?", id, tenantID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		var found string
		err = s.DB.QueryRowContext(ctx, "SELECT id FROM access_keys WHERE id=? AND tenant_id=?", id, tenantID).Scan(&found)
		return dbError(err)
	}
	return nil
}
func (s *Store) AuthenticateKey(ctx context.Context, secret string) (domain.AccessKey, error) {
	var key domain.AccessKey
	var raw []byte
	if !strings.HasPrefix(secret, "rp_live_") || len(secret) > 128 {
		return key, ErrUnauthorized
	}
	err := s.DB.QueryRowContext(ctx, "SELECT k.id,k.tenant_id,k.project_id,k.name,k.prefix,k.enabled,k.rpm,k.models,k.created_at FROM access_keys k JOIN tenants t ON t.id=k.tenant_id WHERE k.token_hash=? AND k.enabled=TRUE AND t.status='active' AND EXISTS (SELECT 1 FROM users u WHERE u.tenant_id=t.id AND BINARY u.email=? AND u.operator=TRUE)", tokenHash(secret), SuperAdminEmail).Scan(&key.ID, &key.TenantID, &key.ProjectID, &key.Name, &key.Prefix, &key.Enabled, &key.RPM, &raw, &key.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return key, ErrUnauthorized
	}
	if err != nil {
		return key, err
	}
	if err = json.Unmarshal(raw, &key.Models); err != nil {
		return domain.AccessKey{}, err
	}
	return key, nil
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"relaypine.com/gateway/internal/domain"
)

// createAdministrator provisions an identity, never an authentication credential.
func (s *Store) createAdministrator(ctx context.Context, name string) (domain.User, error) {
	user := domain.User{ID: newID("usr_"), TenantID: newID("ten_"), Email: s.superAdminEmail, Name: name, Operator: true}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO tenants(id,name,status,created_at) VALUES(?,?,'active',?)", []any{user.TenantID, name, now}},
		{"INSERT INTO users(id,tenant_id,email,name,password_hash,operator,created_at) VALUES(?,?,?,?,?,TRUE,?)", []any{user.ID, user.TenantID, user.Email, name, "!email-otp", now}},
		{"INSERT INTO projects(id,tenant_id,name,created_at) VALUES(?,?,'Default project',?)", []any{newID("prj_"), user.TenantID, now}},
		{"INSERT INTO wallets(tenant_id,balance_micros,held_micros,currency) VALUES(?,0,0,'usd')", []any{user.TenantID}},
	} {
		if _, err = tx.ExecContext(ctx, q.sql, q.args...); err != nil {
			return domain.User{}, dbError(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// EmailAdministrator is called only after mailbox ownership has been verified.
func (s *Store) EmailAdministrator(ctx context.Context, email string) (domain.User, error) {
	var user domain.User
	if s.superAdminEmail == "" || NormalizeEmail(email) != s.superAdminEmail {
		return user, ErrUnauthorized
	}
	err := s.DB.QueryRowContext(ctx, "SELECT u.id,u.tenant_id,u.email,u.name,u.operator FROM users u JOIN tenants t ON t.id=u.tenant_id WHERE BINARY u.email=? AND u.operator=TRUE AND u.password_hash='!email-otp' AND t.status='active'", s.superAdminEmail).Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Operator)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrUnauthorized
	}
	return user, err
}

// EmailUser provisions or restores only an identity whose mailbox has just been
// verified. Existing balances and ownership survive; legacy credentials do not.
func (s *Store) EmailUser(ctx context.Context, email, name string) (domain.User, error) {
	email, name = NormalizeEmail(email), strings.TrimSpace(name)
	if s.superAdminEmail == "" || !ValidEmail(email) || len(name) > 100 {
		return domain.User{}, ErrUnauthorized
	}
	if email == s.superAdminEmail {
		return s.EmailAdministrator(ctx, email)
	}
	if name == "" {
		name = email
		for len(name) > 100 {
			_, size := utf8.DecodeLastRuneInString(name)
			name = name[:len(name)-size]
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		user, err := s.emailUser(ctx, email, name)
		if !errors.Is(err, ErrConflict) {
			return user, err
		}
	}
	return domain.User{}, ErrConflict
}
func (s *Store) emailUser(ctx context.Context, email, name string) (domain.User, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()
	var user domain.User
	var status, marker string
	err = tx.QueryRowContext(ctx, "SELECT u.id,u.tenant_id,u.email,u.name,u.operator,u.password_hash,t.status FROM users u JOIN tenants t ON t.id=u.tenant_id WHERE BINARY LOWER(TRIM(u.email))=? FOR UPDATE", email).Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Operator, &marker, &status)
	if errors.Is(err, sql.ErrNoRows) {
		user = domain.User{ID: newID("usr_"), TenantID: newID("ten_"), Email: email, Name: name}
		now := time.Now().UTC()
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO tenants(id,name,status,created_at) VALUES(?,?,'active',?)", []any{user.TenantID, name, now}},
			{"INSERT INTO users(id,tenant_id,email,name,password_hash,operator,created_at) VALUES(?,?,?,?,'!email-otp',FALSE,?)", []any{user.ID, user.TenantID, email, name, now}},
			{"INSERT INTO projects(id,tenant_id,name,created_at) VALUES(?,?,'Default project',?)", []any{newID("prj_"), user.TenantID, now}},
			{"INSERT INTO wallets(tenant_id,balance_micros,held_micros,currency) VALUES(?,0,0,'usd')", []any{user.TenantID}},
		} {
			if _, err = tx.ExecContext(ctx, q.sql, q.args...); err != nil {
				return domain.User{}, dbError(err)
			}
		}
	} else if err != nil {
		return domain.User{}, err
	} else {
		if status != "active" {
			return domain.User{}, ErrUnauthorized
		}
		if marker != "!email-otp" || user.Operator {
			if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", user.ID); err != nil {
				return domain.User{}, err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE access_keys SET enabled=FALSE WHERE tenant_id=?", user.TenantID); err != nil {
				return domain.User{}, err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE users SET email=?,password_hash='!email-otp',operator=FALSE WHERE id=?", email, user.ID); err != nil {
			return domain.User{}, err
		}
		user.Email, user.Operator = email, false
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM waitlist_entries WHERE email=?", email); err != nil {
		return domain.User{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// Session audience is stored with the OTP authentication method. Even a copied
// cookie cannot promote a customer-site session to administrative access.
func sessionMethod(surface string) string {
	if surface == "admin" {
		return "email_otp_admin"
	}
	if surface == "customer" {
		return "email_otp_customer"
	}
	return ""
}
func (s *Store) CreateSession(ctx context.Context, userID string) (string, error) {
	return s.CreateSurfaceSession(ctx, userID, "admin")
}
func (s *Store) CreateSurfaceSession(ctx context.Context, userID, surface string) (string, error) {
	method := sessionMethod(surface)
	if method == "" {
		return "", ErrUnauthorized
	}
	token, err := newToken("rps_")
	if err != nil {
		return "", err
	}
	query := "INSERT INTO sessions(token_hash,user_id,expires_at,auth_method) SELECT ?,u.id,?,? FROM users u JOIN tenants t ON t.id=u.tenant_id WHERE u.id=? AND u.password_hash='!email-otp' AND t.status='active' AND (u.operator=FALSE OR BINARY u.email=?)"
	args := []any{tokenHash(token), time.Now().UTC().Add(7 * 24 * time.Hour), method, userID, s.superAdminEmail}
	if surface == "admin" {
		query += " AND u.operator=TRUE AND BINARY u.email=?"
		args = append(args, s.superAdminEmail)
	}
	res, err := s.DB.ExecContext(ctx, query, args...)
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
	return s.SurfaceSessionUser(ctx, token, "admin")
}
func (s *Store) SurfaceSessionUser(ctx context.Context, token, surface string) (domain.User, error) {
	var u domain.User
	method := sessionMethod(surface)
	if !strings.HasPrefix(token, "rps_") || len(token) > 128 || method == "" {
		return u, ErrUnauthorized
	}
	query := "SELECT u.id,u.tenant_id,u.email,u.name,u.operator FROM sessions se JOIN users u ON u.id=se.user_id JOIN tenants t ON t.id=u.tenant_id WHERE se.token_hash=? AND se.auth_method=? AND se.expires_at>? AND u.password_hash='!email-otp' AND t.status='active' AND (u.operator=FALSE OR BINARY u.email=?)"
	args := []any{tokenHash(token), method, time.Now().UTC(), s.superAdminEmail}
	if surface == "admin" {
		query += " AND u.operator=TRUE AND BINARY u.email=?"
		args = append(args, s.superAdminEmail)
	}
	err := s.DB.QueryRowContext(ctx, query, args...).Scan(&u.ID, &u.TenantID, &u.Email, &u.Name, &u.Operator)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrUnauthorized
	}
	if surface == "customer" {
		u.Operator = false
	}
	return u, err
}
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", tokenHash(token))
	return err
}

func (s *Store) CreateKey(ctx context.Context, user domain.User, name string, models []string, rpm int) (domain.AccessKey, string, error) {
	var key domain.AccessKey
	if user.ID == "" || user.TenantID == "" || !ValidEmail(NormalizeEmail(user.Email)) {
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
	err = tx.QueryRowContext(ctx, "SELECT p.id FROM projects p JOIN tenants t ON t.id=p.tenant_id JOIN users u ON u.tenant_id=t.id WHERE p.tenant_id=? AND u.id=? AND BINARY u.email=? AND u.password_hash='!email-otp' AND (u.operator=FALSE OR BINARY u.email=?) AND t.status='active' ORDER BY p.created_at,p.id LIMIT 1", user.TenantID, user.ID, NormalizeEmail(user.Email), s.superAdminEmail).Scan(&key.ProjectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AccessKey{}, "", ErrUnauthorized
		}
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
	err := s.DB.QueryRowContext(ctx, "SELECT k.id,k.tenant_id,k.project_id,k.name,k.prefix,k.enabled,k.rpm,k.models,k.created_at FROM access_keys k JOIN tenants t ON t.id=k.tenant_id WHERE k.token_hash=? AND k.enabled=TRUE AND t.status='active' AND EXISTS (SELECT 1 FROM users u WHERE u.tenant_id=t.id AND u.password_hash='!email-otp' AND (u.operator=FALSE OR BINARY u.email=?))", tokenHash(secret), s.superAdminEmail).Scan(&key.ID, &key.TenantID, &key.ProjectID, &key.Name, &key.Prefix, &key.Enabled, &key.RPM, &raw, &key.CreatedAt)
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

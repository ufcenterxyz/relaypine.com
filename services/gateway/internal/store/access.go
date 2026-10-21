package store

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"relaypine.com/gateway/internal/domain"
)

func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// SuperAdminEmail returns this instance's sole allowed administrator identity.
// An email match is an authorization policy, never proof of mailbox ownership.
func (s *Store) SuperAdminEmail() string { return s.superAdminEmail }
func (s *Store) IsSuperAdmin(user domain.User) bool {
	return s.superAdminEmail != "" && user.Operator && NormalizeEmail(user.Email) == s.superAdminEmail
}
func ValidEmail(email string) bool {
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email && len(email) <= 254
}

// EnsureSuperAdmin provisions the sole allowed identity and retires all password
// credentials. Knowing the allowlisted email never proves mailbox ownership.
func (s *Store) EnsureSuperAdmin(ctx context.Context, name string) (domain.User, error) {
	if !ValidEmail(s.superAdminEmail) {
		return domain.User{}, errors.New("SUPERADMIN_EMAIL must be a valid email address")
	}
	if !validName(name) {
		return domain.User{}, errors.New("valid administrator name required")
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return domain.User{}, err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('relaypine_private_bootstrap',30)").Scan(&locked); err != nil || locked != 1 {
		return domain.User{}, errors.New("cannot acquire private-access bootstrap lock")
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('relaypine_private_bootstrap')")
	var count int
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE BINARY LOWER(TRIM(email))=?", s.superAdminEmail).Scan(&count); err != nil {
		return domain.User{}, err
	}
	if count == 0 {
		if _, err = s.createAdministrator(ctx, name); err != nil {
			return domain.User{}, err
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()
	var user domain.User
	err = tx.QueryRowContext(ctx, "SELECT id,tenant_id,email,name,operator,password_hash FROM users WHERE BINARY LOWER(TRIM(email))=? FOR UPDATE", s.superAdminEmail).Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Operator, &user.PasswordHash)
	if err != nil {
		return domain.User{}, err
	}
	// Retire old API keys as well as sessions when migrating a password account
	// or promoting a previously unverified identity. Subsequent restarts preserve
	// keys and sessions issued under the mailbox-verification policy.
	migrating := !user.Operator || user.PasswordHash != "!email-otp"
	if migrating {
		if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", user.ID); err != nil {
			return domain.User{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE access_keys SET enabled=FALSE WHERE tenant_id=?", user.TenantID); err != nil {
			return domain.User{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET password_hash='!email-otp' WHERE id=?", user.ID); err != nil {
		return domain.User{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET operator=(id=?)", user.ID); err != nil {
		return domain.User{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET email=?,name=? WHERE id=?", s.superAdminEmail, strings.TrimSpace(name), user.ID); err != nil {
		return domain.User{}, err
	}
	// Preserve previous accounts, balances and records, while retaining their place
	// on the waitlist and ending all access through their old credentials.
	if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO waitlist_entries(id,email,name,created_at) SELECT CONCAT('wl_',MD5(id)),LOWER(TRIM(email)),name,created_at FROM users WHERE id<>?", user.ID); err != nil {
		return domain.User{}, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE se FROM sessions se JOIN users u ON u.id=se.user_id WHERE u.id<>?", user.ID); err != nil {
		return domain.User{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE access_keys SET enabled=FALSE WHERE tenant_id<>?", user.TenantID); err != nil {
		return domain.User{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.User{}, err
	}
	user.Name, user.Email, user.Operator, user.PasswordHash = strings.TrimSpace(name), s.superAdminEmail, true, ""
	return user, nil
}

func (s *Store) JoinWaitlist(ctx context.Context, email, name string) error {
	email, name = NormalizeEmail(email), strings.TrimSpace(name)
	if !ValidEmail(email) || len(name) > 100 {
		return errors.New("valid email and optional name up to 100 bytes required")
	}
	// Identical response for every address; the admin never becomes a waiting user.
	if email == s.superAdminEmail {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, "INSERT INTO waitlist_entries(id,email,name,created_at) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE email=VALUES(email)", newID("wl_"), email, name, time.Now().UTC())
	return err
}
func (s *Store) listWaitlist(ctx context.Context) ([]domain.WaitlistEntry, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,name,email,created_at FROM waitlist_entries WHERE email<>? ORDER BY created_at DESC,id DESC", s.superAdminEmail)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.WaitlistEntry{}
	for rows.Next() {
		var entry domain.WaitlistEntry
		if err = rows.Scan(&entry.ID, &entry.Name, &entry.Email, &entry.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

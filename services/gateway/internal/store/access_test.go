package store

import (
	"context"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestOpenRejectsMissingOrInvalidAdministratorBeforeDatabaseAccess(t *testing.T) {
	for _, email := range []string{"", " \t\n", "not-an-email", "Owner <owner@example.com>", "first@example.com,second@example.com", "owner@example.com\nother@example.com", strings.Repeat("a", 245) + "@example.com"} {
		t.Run(email, func(t *testing.T) {
			s, err := Open(context.Background(), "intentionally invalid DSN", "invalid master key", Config{SuperAdminEmail: email})
			if s != nil || err == nil || err.Error() != "SUPERADMIN_EMAIL must be a valid email address" {
				t.Fatalf("invalid administrator reached other initialization: store=%v err=%v", s, err)
			}
		})
	}
}

func TestAdministratorPolicyIsInstanceScopedAndFailsClosed(t *testing.T) {
	first := &Store{superAdminEmail: "first@example.com"}
	second := &Store{superAdminEmail: "second@example.com"}
	user := domain.User{Email: " FIRST@EXAMPLE.COM ", Operator: true}
	if !first.IsSuperAdmin(user) || second.IsSuperAdmin(user) || (&Store{}).IsSuperAdmin(user) {
		t.Fatal("administrator policy leaked across store instances")
	}
	user.Operator = false
	if first.IsSuperAdmin(user) {
		t.Fatal("matching address without durable operator role was accepted")
	}
	if _, err := (&Store{}).EnsureSuperAdmin(context.Background(), "Owner"); err == nil {
		t.Fatal("unconfigured store attempted administrator bootstrap")
	}
	if _, err := (&Store{}).EmailAdministrator(context.Background(), ""); err != ErrUnauthorized {
		t.Fatal("unconfigured store accepted empty identity")
	}
}

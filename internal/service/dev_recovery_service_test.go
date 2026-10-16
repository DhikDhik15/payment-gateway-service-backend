package service

// Unit tests for development-only account recovery (no SMTP, no database).
// Repository doubles only; the configured key is a test value.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
)

const testDevRecoveryKey = "0123456789abcdef0123456789abcdef"

type devRecoveryFixture struct {
	users     *mockMerchantUserRepo
	merchants *mockMerchantRepo
	sessions  *mockSessionRepo
	merchant  *model.Merchant
	target    *model.MerchantUser
	other     *model.MerchantUser
	oldHash   string
}

func newDevRecoveryFixture(t *testing.T) *devRecoveryFixture {
	t.Helper()
	users := newMockMerchantUserRepo()
	merchants := newMockMerchantRepo()
	sessions := newMockSessionRepo()

	merchantID := uuid.New()
	merchant := &model.Merchant{ID: merchantID, Name: "Toko", Code: "TKO", Status: model.MerchantStatusActive}
	merchants.merchants[merchantID] = merchant

	oldHash, err := hashPassword("old-password-1")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	target := &model.MerchantUser{
		ID:           uuid.New(),
		MerchantID:   merchantID,
		Email:        "owner@example.com",
		PasswordHash: oldHash,
		Role:         model.DashboardUserRoleOwner,
		Status:       model.DashboardUserStatusActive,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	users.users[target.Email] = target
	users.usersByID[target.ID] = target

	otherHash, err := hashPassword("other-password-1")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	other := &model.MerchantUser{
		ID:           uuid.New(),
		MerchantID:   merchantID,
		Email:        "admin@example.com",
		PasswordHash: otherHash,
		Role:         model.DashboardUserRoleAdmin,
		Status:       model.DashboardUserStatusActive,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	users.users[other.Email] = other
	users.usersByID[other.ID] = other

	// Active refresh session for the target (must be revoked on recovery).
	if err := sessions.Create(context.Background(), &model.DashboardSession{
		ID:               uuid.New(),
		MerchantUserID:   target.ID,
		RefreshTokenHash: "target-session-hash",
		ExpiresAt:        time.Now().UTC().Add(24 * time.Hour),
		CreatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// Active refresh session for the other user (must survive).
	if err := sessions.Create(context.Background(), &model.DashboardSession{
		ID:               uuid.New(),
		MerchantUserID:   other.ID,
		RefreshTokenHash: "other-session-hash",
		ExpiresAt:        time.Now().UTC().Add(24 * time.Hour),
		CreatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed other session: %v", err)
	}

	return &devRecoveryFixture{
		users:     users,
		merchants: merchants,
		sessions:  sessions,
		merchant:  merchant,
		target:    target,
		other:     other,
		oldHash:   oldHash,
	}
}

func newEnabledDevRecoverySvc(f *devRecoveryFixture) DevRecoveryService {
	return NewDevRecoveryService(f.users, f.merchants, f.sessions, true, testDevRecoveryKey, "development")
}

func TestDevRecovery_DisabledByDefault(t *testing.T) {
	f := newDevRecoveryFixture(t)
	svc := NewDevRecoveryService(f.users, f.merchants, f.sessions, false, "", "development")
	if svc.Enabled() {
		t.Fatal("Enabled() = true, want false by default")
	}
	_, err := svc.Recover(context.Background(), testDevRecoveryKey, "owner@example.com", "new-strong-pass-1", "new-strong-pass-1")
	if !errors.Is(err, ErrDevRecoveryDisabled) {
		t.Fatalf("disabled recover = %v, want ErrDevRecoveryDisabled", err)
	}
}

func TestDevRecovery_RejectedInProduction(t *testing.T) {
	f := newDevRecoveryFixture(t)
	svc := NewDevRecoveryService(f.users, f.merchants, f.sessions, true, testDevRecoveryKey, "production")
	if svc.Enabled() {
		t.Fatal("Enabled() must be false in production")
	}
	_, err := svc.Recover(context.Background(), testDevRecoveryKey, "owner@example.com", "new-strong-pass-1", "new-strong-pass-1")
	if !errors.Is(err, ErrDevRecoveryDisabled) {
		t.Fatalf("production recover = %v, want ErrDevRecoveryDisabled", err)
	}
}

func TestDevRecovery_InvalidKey(t *testing.T) {
	f := newDevRecoveryFixture(t)
	svc := newEnabledDevRecoverySvc(f)
	for name, key := range map[string]string{"empty": "", "wrong": "ffffffffffffffffffffffffffffffff"} {
		if _, err := svc.Recover(context.Background(), key, "owner@example.com", "new-strong-pass-1", "new-strong-pass-1"); !errors.Is(err, ErrDevRecoveryUnauthorized) {
			t.Errorf("%s key: got %v, want ErrDevRecoveryUnauthorized", name, err)
		}
	}
}

func TestDevRecovery_EmailAloneIsNotEnough(t *testing.T) {
	f := newDevRecoveryFixture(t)
	svc := newEnabledDevRecoverySvc(f)
	// No key supplied at all: even the correct account email must not reset.
	if _, err := svc.Recover(context.Background(), "", "owner@example.com", "new-strong-pass-1", "new-strong-pass-1"); !errors.Is(err, ErrDevRecoveryUnauthorized) {
		t.Fatalf("missing key = %v, want ErrDevRecoveryUnauthorized", err)
	}
	if !verifyPassword("old-password-1", f.target.PasswordHash) {
		t.Fatal("password must be unchanged after unauthorized attempt")
	}
}

func TestDevRecovery_UnknownEmailNotFound(t *testing.T) {
	f := newDevRecoveryFixture(t)
	svc := newEnabledDevRecoverySvc(f)
	if _, err := svc.Recover(context.Background(), testDevRecoveryKey, "nobody@example.com", "new-strong-pass-1", "new-strong-pass-1"); !errors.Is(err, ErrDevRecoveryNotFound) {
		t.Fatalf("unknown email = %v, want ErrDevRecoveryNotFound", err)
	}
}

func TestDevRecovery_DisabledUserNotFound(t *testing.T) {
	f := newDevRecoveryFixture(t)
	f.target.Status = model.DashboardUserStatusDisabled
	svc := newEnabledDevRecoverySvc(f)
	if _, err := svc.Recover(context.Background(), testDevRecoveryKey, "owner@example.com", "new-strong-pass-1", "new-strong-pass-1"); !errors.Is(err, ErrDevRecoveryNotFound) {
		t.Fatalf("disabled user = %v, want ErrDevRecoveryNotFound", err)
	}
}

func TestDevRecovery_PasswordPolicy(t *testing.T) {
	f := newDevRecoveryFixture(t)
	svc := newEnabledDevRecoverySvc(f)
	if _, err := svc.Recover(context.Background(), testDevRecoveryKey, "owner@example.com", "short", "short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("short password = %v, want ErrInvalidPassword", err)
	}
	if _, err := svc.Recover(context.Background(), testDevRecoveryKey, "owner@example.com", "new-strong-pass-1", "different-pass-2"); !errors.Is(err, ErrDevRecoveryPasswordMismatch) {
		t.Fatalf("mismatch = %v, want ErrDevRecoveryPasswordMismatch", err)
	}
}

func TestDevRecovery_SuccessOnlyIntendedAccount(t *testing.T) {
	f := newDevRecoveryFixture(t)
	svc := newEnabledDevRecoverySvc(f)

	const newPassword = "brand-new-password-1"
	resp, err := svc.Recover(context.Background(), testDevRecoveryKey, "  OWNER@example.com ", newPassword, newPassword)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Email != "owner@example.com" {
		t.Errorf("response email = %q, want owner@example.com", resp.Email)
	}

	// New password verifies with the existing hashing primitives; old does not.
	updated, err := f.users.GetByID(context.Background(), f.target.ID)
	if err != nil {
		t.Fatalf("get updated user: %v", err)
	}
	if !verifyPassword(newPassword, updated.PasswordHash) {
		t.Fatal("new password must verify against the stored hash")
	}
	if verifyPassword("old-password-1", updated.PasswordHash) {
		t.Fatal("old password must not verify after recovery")
	}

	// The other account is untouched.
	other, err := f.users.GetByID(context.Background(), f.other.ID)
	if err != nil {
		t.Fatalf("get other user: %v", err)
	}
	if !verifyPassword("other-password-1", other.PasswordHash) {
		t.Fatal("other account password must be unchanged")
	}

	// Refresh sessions: target revoked, other survives.
	if got := len(mustSessionsFor(t, f.sessions, f.target.ID)); got != 0 {
		t.Errorf("target sessions = %d, want 0 (revoked)", got)
	}
	if got := len(mustSessionsFor(t, f.sessions, f.other.ID)); got != 1 {
		t.Errorf("other sessions = %d, want 1 (untouched)", got)
	}
}

func TestDevRecovery_LoginSucceedsWithNewPassword(t *testing.T) {
	f := newDevRecoveryFixture(t)
	svc := newEnabledDevRecoverySvc(f)

	const newPassword = "brand-new-password-2"
	if _, err := svc.Recover(context.Background(), testDevRecoveryKey, "owner@example.com", newPassword, newPassword); err != nil {
		t.Fatalf("recover: %v", err)
	}
	// Existing login behavior: the same AuthService primitives accept the new
	// password and reject the old one.
	authSvc := newTestAuthService(f.users, f.sessions)
	if _, _, err := authSvc.Login(context.Background(), model.LoginRequest{Email: "owner@example.com", Password: newPassword}); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
	if _, _, err := authSvc.Login(context.Background(), model.LoginRequest{Email: "owner@example.com", Password: "old-password-1"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login with old password = %v, want ErrInvalidCredentials", err)
	}
}

func TestDevRecovery_ResponseCarriesNoSecrets(t *testing.T) {
	f := newDevRecoveryFixture(t)
	svc := newEnabledDevRecoverySvc(f)
	resp, err := svc.Recover(context.Background(), testDevRecoveryKey, "owner@example.com", "brand-new-password-3", "brand-new-password-3")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	// DashboardUserResponse has no password/hash field by construction; assert
	// the identifiable fields only.
	if resp.ID == uuid.Nil || resp.Email == "" {
		t.Fatalf("response must identify the account, got %+v", resp)
	}
	if strings.Contains(resp.Email, testDevRecoveryKey) {
		t.Fatal("response must never contain the recovery key")
	}
}

func mustSessionsFor(t *testing.T, repo *mockSessionRepo, userID uuid.UUID) []*model.DashboardSession {
	t.Helper()
	out, err := repo.GetByUserID(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	return out
}

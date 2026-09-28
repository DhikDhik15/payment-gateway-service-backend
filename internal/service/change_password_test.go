package service

// change_password_test.go — Phase 8A self-service password change tests.
//
// Covers spec §20:
//  1. correct current password succeeds
//  2. wrong current password rejected
//  3. empty new password rejected
//  4. invalid password rejected
//  5. password hash is never returned
//  6. plaintext password never logged
//  7. password change does not affect other merchant users
//  8. password change is tenant-safe
//  9. disabled user cannot change password
// 10. user cannot change another user's password (identity from session only)

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

func newChangePasswordFixture(t *testing.T) (DashboardUserService, *mockMerchantUserRepo, *mockSessionRepo, *model.Merchant, *model.MerchantUser) {
	t.Helper()
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	sessionRepo := newMockSessionRepo()
	svc := NewDashboardUserService(userRepo, sessionRepo, merchantRepo)
	merchant := seedMerchant(merchantRepo)

	hash, err := hashPassword("current-password-1")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	user := &model.MerchantUser{
		ID:           uuid.New(),
		MerchantID:   merchant.ID,
		Email:        "owner@example.com",
		PasswordHash: hash,
		Role:         model.DashboardUserRoleOwner,
		Status:       model.DashboardUserStatusActive,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := userRepo.Create(context.Background(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return svc, userRepo, sessionRepo, merchant, user
}

// ─── 1. Correct current password succeeds ────────────────────────────────────

func TestChangePassword_CorrectCurrentPasswordSucceeds(t *testing.T) {
	svc, userRepo, _, _, user := newChangePasswordFixture(t)

	resp, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     "brand-new-password",
	})
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// The stored hash must verify against the NEW password and reject the old.
	stored := mustGetUser(t, userRepo, user.ID)
	if !verifyPassword("brand-new-password", stored.PasswordHash) {
		t.Error("stored hash must verify against the new password")
	}
	if verifyPassword("current-password-1", stored.PasswordHash) {
		t.Error("stored hash must no longer verify against the old password")
	}
}

// ─── 2. Wrong current password rejected ──────────────────────────────────────

func TestChangePassword_WrongCurrentPasswordRejected(t *testing.T) {
	svc, userRepo, _, _, user := newChangePasswordFixture(t)

	_, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "totally-wrong-password",
		NewPassword:     "brand-new-password",
	})
	if !errors.Is(err, ErrInvalidCurrentPassword) {
		t.Fatalf("expected ErrInvalidCurrentPassword, got %v", err)
	}

	// Hash must be untouched after the rejected attempt.
	stored := mustGetUser(t, userRepo, user.ID)
	if !verifyPassword("current-password-1", stored.PasswordHash) {
		t.Error("password hash must be unchanged after failed attempt")
	}
}

// ─── 3. Empty new password rejected ──────────────────────────────────────────

func TestChangePassword_EmptyNewPasswordRejected(t *testing.T) {
	svc, _, _, _, user := newChangePasswordFixture(t)

	_, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     "",
	})
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("empty new password: expected ErrInvalidPassword, got %v", err)
	}
}

// ─── 4. Invalid (too short / too long) password rejected ─────────────────────

func TestChangePassword_InvalidPasswordRejected(t *testing.T) {
	svc, _, _, _, user := newChangePasswordFixture(t)

	_, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     "short", // < 8 chars
	})
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("short new password: expected ErrInvalidPassword, got %v", err)
	}

	_, err = svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     strings.Repeat("a", 129), // > 128 chars
	})
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("overlong new password: expected ErrInvalidPassword, got %v", err)
	}
}

// ─── 5. Password hash is never returned ──────────────────────────────────────

func TestChangePassword_HashNeverReturned(t *testing.T) {
	svc, _, _, _, user := newChangePasswordFixture(t)

	resp, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     "brand-new-password",
	})
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// Structural guarantee: DashboardUserResponse carries no password field.
	// Serialise to JSON and assert no hash material leaks.
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "password") || strings.Contains(body, "argon2") {
		t.Errorf("response must never contain password material: %s", body)
	}
}

// ─── 6. Plaintext password never logged ──────────────────────────────────────

func TestChangePassword_PlaintextNeverLogged(t *testing.T) {
	svc, _, _, _, user := newChangePasswordFixture(t)

	const current = "current-password-1"
	const newPw = "brand-new-password-secret"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	if _, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: current,
		NewPassword:     newPw,
	}); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	// Also log a failure path (wrong current password).
	_, _ = svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "wrong-password-xyz",
		NewPassword:     newPw,
	})

	logged := buf.String()
	if strings.Contains(logged, current) {
		t.Error("current plaintext password must never be logged")
	}
	if strings.Contains(logged, newPw) {
		t.Error("new plaintext password must never be logged")
	}
	if strings.Contains(logged, "wrong-password-xyz") {
		t.Error("wrong plaintext password must never be logged")
	}
}

// ─── 7. Other users of the same merchant are unaffected ──────────────────────

func TestChangePassword_DoesNotAffectOtherMerchantUsers(t *testing.T) {
	svc, userRepo, _, merchant, user := newChangePasswordFixture(t)

	otherHash, err := hashPassword("other-user-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	other := &model.MerchantUser{
		ID:           uuid.New(),
		MerchantID:   merchant.ID,
		Email:        "colleague@example.com",
		PasswordHash: otherHash,
		Role:         model.DashboardUserRoleAdmin,
		Status:       model.DashboardUserStatusActive,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := userRepo.Create(context.Background(), other); err != nil {
		t.Fatalf("create other: %v", err)
	}

	if _, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     "brand-new-password",
	}); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	colleague := mustGetUser(t, userRepo, other.ID)
	if !verifyPassword("other-user-password", colleague.PasswordHash) {
		t.Error("colleague's password must be unaffected")
	}
}

// ─── 8. Tenant-safe: other merchants are unaffected ──────────────────────────

func TestChangePassword_TenantSafe(t *testing.T) {
	svc, userRepo, _, _, user := newChangePasswordFixture(t)

	otherMerchant := seedMerchant(newMockMerchantRepo()) // separate repo seed, distinct ID
	tenantHash, err := hashPassword("other-tenant-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	tenantUser := &model.MerchantUser{
		ID:           uuid.New(),
		MerchantID:   otherMerchant.ID,
		Email:        "other-tenant@example.com",
		PasswordHash: tenantHash,
		Role:         model.DashboardUserRoleOwner,
		Status:       model.DashboardUserStatusActive,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := userRepo.Create(context.Background(), tenantUser); err != nil {
		t.Fatalf("create tenant user: %v", err)
	}

	if _, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     "brand-new-password",
	}); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// The other tenant's password must be untouched, and the caller identity
	// came from the session — the method accepts no merchant/target input.
	other := mustGetUser(t, userRepo, tenantUser.ID)
	if !verifyPassword("other-tenant-password", other.PasswordHash) {
		t.Error("cross-tenant password must be unaffected")
	}
	if other.MerchantID == user.MerchantID {
		t.Fatal("test setup error: expected different merchants")
	}
}

// ─── 9. Disabled user cannot change password ─────────────────────────────────

func TestChangePassword_DisabledUserRejected(t *testing.T) {
	svc, userRepo, _, _, user := newChangePasswordFixture(t)

	if err := userRepo.UpdateStatus(context.Background(), user.ID, model.DashboardUserStatusDisabled); err != nil {
		t.Fatalf("disable: %v", err)
	}

	_, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     "brand-new-password",
	})
	if !errors.Is(err, ErrUserDisabled) {
		t.Errorf("expected ErrUserDisabled, got %v", err)
	}
}

// ─── 10. Identity comes from the session only ────────────────────────────────

func TestChangePassword_UnknownCallerRejected(t *testing.T) {
	svc, _, _, _, _ := newChangePasswordFixture(t)

	// A caller ID that does not exist can never be targeted via the body —
	// the only input is the authenticated caller's ID.
	_, err := svc.ChangePassword(context.Background(), uuid.New(), model.ChangePasswordRequest{
		CurrentPassword: "whatever",
		NewPassword:     "brand-new-password",
	})
	if !errors.Is(err, ErrDashboardUserNotFound) {
		t.Errorf("expected ErrDashboardUserNotFound, got %v", err)
	}
}

// ─── Sessions are revoked after a successful password change ────────────────

func TestChangePassword_FailsClosedWhenSessionRevocationFails(t *testing.T) {
	svc, _, sessionRepo, _, user := newChangePasswordFixture(t)
	seedSessionFor(t, sessionRepo, user.ID, "hash-pw-session-failure")
	sessionRepo.deleteByUserErr = errors.New("session store unavailable")

	_, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     "brand-new-password",
	})
	if err == nil || !strings.Contains(err.Error(), "revoke sessions") {
		t.Fatalf("ChangePassword error = %v, want explicit revocation failure", err)
	}
}

func TestChangePassword_InvalidatesAllSessions(t *testing.T) {
	svc, _, sessionRepo, _, user := newChangePasswordFixture(t)

	seedSessionFor(t, sessionRepo, user.ID, "hash-pw-session-1")
	seedSessionFor(t, sessionRepo, user.ID, "hash-pw-session-2")

	if _, err := svc.ChangePassword(context.Background(), user.ID, model.ChangePasswordRequest{
		CurrentPassword: "current-password-1",
		NewPassword:     "brand-new-password",
	}); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	sessions, err := sessionRepo.GetByUserID(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("all sessions must be revoked after password change, got %d", len(sessions))
	}

	// No new session is auto-created — the user must log in again.
	if _, err := sessionRepo.GetByRefreshTokenHash(context.Background(), "hash-pw-session-1"); !errors.Is(err, repository.ErrSessionNotFound) {
		t.Errorf("expected old refresh session gone, got %v", err)
	}
}

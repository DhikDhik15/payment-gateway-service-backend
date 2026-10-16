package service

// Unit tests for the forgot-password flow (Phase 8E). Repository doubles only;
// no database, no SMTP, no network.

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Stub reset store ─────────────────────────────────────────────────────────

type stubResetStore struct {
	createErr    error
	created      *model.PasswordResetToken
	createdEntry *model.EmailOutbox
	consumeErr   error
	consumeUser  uuid.UUID
	lastHash     string
	// lastPasswordHash captures the Argon2id hash handed to the store so tests
	// can prove the new password verifies (and the old one does not) using
	// the same primitives as login.
	lastPasswordHash string
}

func (s *stubResetStore) CreateWithEmailOutbox(_ context.Context, token *model.PasswordResetToken, entry *model.EmailOutbox) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created = token
	s.createdEntry = entry
	return nil
}

func (s *stubResetStore) ConsumeWithPasswordUpdate(_ context.Context, tokenHash, passwordHash string) (uuid.UUID, error) {
	s.lastHash = tokenHash
	s.lastPasswordHash = passwordHash
	if s.consumeErr != nil {
		return uuid.Nil, s.consumeErr
	}
	return s.consumeUser, nil
}

func (s *stubResetStore) DeleteExpired(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// ─── Fixture ─────────────────────────────────────────────────────────────────

type resetFixture struct {
	users     *mockMerchantUserRepo
	merchants *mockMerchantRepo
	store     *stubResetStore
	svc       PasswordResetService
	merchant  *model.Merchant
	user      *model.MerchantUser
}

func newResetFixture(t *testing.T) *resetFixture {
	t.Helper()
	users := newMockMerchantUserRepo()
	merchants := newMockMerchantRepo()
	merchantID := uuid.New()
	merchant := &model.Merchant{ID: merchantID, Name: "Toko", Code: "TKO", Status: model.MerchantStatusActive}
	merchants.merchants[merchantID] = merchant
	user := &model.MerchantUser{
		ID:         uuid.New(),
		MerchantID: merchantID,
		Email:      "owner@example.com",
		Role:       model.DashboardUserRoleOwner,
		Status:     model.DashboardUserStatusActive,
	}
	users.users[user.Email] = user
	users.usersByID[user.ID] = user
	store := &stubResetStore{consumeUser: user.ID}
	svc := NewPasswordResetService(users, merchants, store, time.Hour, "https://dash.example")
	return &resetFixture{users: users, merchants: merchants, store: store, svc: svc, merchant: merchant, user: user}
}

// ─── Request ─────────────────────────────────────────────────────────────────

func TestRequestPasswordReset_UnknownEmailIsGenericSuccess(t *testing.T) {
	f := newResetFixture(t)
	if err := f.svc.RequestPasswordReset(context.Background(), "nobody@example.com"); err != nil {
		t.Fatalf("unknown email must succeed generically, got %v", err)
	}
	if f.store.created != nil || f.store.createdEntry != nil {
		t.Fatalf("unknown email must not create a token or email")
	}
}

func TestRequestPasswordReset_DisabledUserGetsNoEmail(t *testing.T) {
	f := newResetFixture(t)
	f.user.Status = model.DashboardUserStatusDisabled
	if err := f.svc.RequestPasswordReset(context.Background(), "owner@example.com"); err != nil {
		t.Fatalf("ineligible account must succeed generically, got %v", err)
	}
	if f.store.created != nil {
		t.Fatalf("disabled user must not receive a token row")
	}
}

func TestRequestPasswordReset_InactiveMerchantGetsNoEmail(t *testing.T) {
	f := newResetFixture(t)
	f.merchant.Status = model.MerchantStatusSuspended
	if err := f.svc.RequestPasswordReset(context.Background(), "owner@example.com"); err != nil {
		t.Fatalf("inactive merchant must succeed generically, got %v", err)
	}
	if f.store.created != nil {
		t.Fatalf("inactive merchant must not receive a token row")
	}
}

func TestRequestPasswordReset_EligibleAccountQueuesResetEmail(t *testing.T) {
	f := newResetFixture(t)
	if err := f.svc.RequestPasswordReset(context.Background(), "  OWNER@example.com "); err != nil {
		t.Fatalf("request: %v", err)
	}
	tok := f.store.created
	if tok == nil {
		t.Fatalf("expected a token row")
	}
	if tok.UserID != f.user.ID {
		t.Errorf("token user = %v, want %v", tok.UserID, f.user.ID)
	}
	if time.Until(tok.ExpiresAt) <= 50*time.Minute || time.Until(tok.ExpiresAt) > time.Hour {
		t.Errorf("expiry not ~1h: %v", tok.ExpiresAt)
	}
	if len(tok.TokenHash) != 64 {
		t.Errorf("token hash must be SHA-256 hex, got %q", tok.TokenHash)
	}
	entry := f.store.createdEntry
	if entry == nil {
		t.Fatalf("expected an outbox entry")
	}
	if entry.Type != model.EmailOutboxTypePasswordReset {
		t.Errorf("outbox type = %q, want PASSWORD_RESET", entry.Type)
	}
	if entry.Recipient != "owner@example.com" {
		t.Errorf("recipient = %q", entry.Recipient)
	}
	if !strings.Contains(entry.TextBody, "https://dash.example/reset-password?token=") {
		t.Errorf("text body must contain the frontend reset URL")
	}
	if !strings.Contains(entry.HTMLBody, "https://dash.example/reset-password?token=") {
		t.Errorf("html body must contain the frontend reset URL")
	}
}

func TestRequestPasswordReset_StoreFailureSurfaces(t *testing.T) {
	f := newResetFixture(t)
	f.store.createErr = errors.New("db down")
	if err := f.svc.RequestPasswordReset(context.Background(), "owner@example.com"); err == nil {
		t.Fatalf("infrastructure failure must surface (handler maps it to generic 500)")
	}
}

func TestRequestPasswordReset_PendingRaceIsGenericSuccess(t *testing.T) {
	f := newResetFixture(t)
	f.store.createErr = repository.ErrPasswordResetAlreadyPending
	if err := f.svc.RequestPasswordReset(context.Background(), "owner@example.com"); err != nil {
		t.Fatalf("pending race must be generic success, got %v", err)
	}
}

// ─── Confirm ─────────────────────────────────────────────────────────────────

func TestConfirmPasswordReset_ValidTokenResetsPassword(t *testing.T) {
	f := newResetFixture(t)
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := f.svc.ConfirmPasswordReset(context.Background(), token, "new-strong-pass"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	raw, err := hex.DecodeString(f.store.lastHash)
	if err != nil || len(raw) != 32 {
		t.Fatalf("store must receive the SHA-256 hex of the token, got %q", f.store.lastHash)
	}
}

func TestConfirmPasswordReset_PolicyRejected(t *testing.T) {
	f := newResetFixture(t)
	if err := f.svc.ConfirmPasswordReset(context.Background(), "token", "short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("short password must fail policy, got %v", err)
	}
}

func TestConfirmPasswordReset_ErrorMapping(t *testing.T) {
	cases := []struct {
		name  string
		store error
		want  error
	}{
		{"unknown", repository.ErrPasswordResetNotFound, ErrPasswordResetInvalid},
		{"expired", repository.ErrPasswordResetExpired, ErrPasswordResetExpired},
		{"consumed", repository.ErrPasswordResetConsumed, ErrPasswordResetConsumed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newResetFixture(t)
			f.store.consumeErr = tc.store
			err := f.svc.ConfirmPasswordReset(context.Background(), "token0123456789abcdef", "new-strong-pass")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestConfirmPasswordReset_NewPasswordVerifies(t *testing.T) {
	f := newResetFixture(t)
	const password = "brand-new-password-1"
	if err := f.svc.ConfirmPasswordReset(context.Background(), "token0123456789abcdef", password); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	// The stored hash must verify the new password (same primitives as login)
	// and reject anything else.
	if !verifyPassword(password, f.store.lastPasswordHash) {
		t.Fatalf("new password must verify against the stored hash")
	}
	if verifyPassword("old-password", f.store.lastPasswordHash) {
		t.Fatalf("old password must not verify against the stored hash")
	}
}

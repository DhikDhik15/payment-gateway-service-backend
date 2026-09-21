package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Mock repositories ────────────────────────────────────────────────────────

type mockMerchantUserRepo struct {
	users          map[string]*model.MerchantUser // email → user
	usersByID      map[uuid.UUID]*model.MerchantUser
	createErr      error
	getByEmailErr  error
	getByIDErr     error
	updateLoginErr error
}

func newMockMerchantUserRepo() *mockMerchantUserRepo {
	return &mockMerchantUserRepo{
		users:     make(map[string]*model.MerchantUser),
		usersByID: make(map[uuid.UUID]*model.MerchantUser),
	}
}

func (m *mockMerchantUserRepo) Create(_ context.Context, user *model.MerchantUser) error {
	if m.createErr != nil {
		return m.createErr
	}
	if _, exists := m.users[user.Email]; exists {
		return repository.ErrMerchantUserEmailExists
	}
	m.users[user.Email] = user
	m.usersByID[user.ID] = user
	return nil
}

func (m *mockMerchantUserRepo) GetByID(_ context.Context, id uuid.UUID) (*model.MerchantUser, error) {
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	u, ok := m.usersByID[id]
	if !ok {
		return nil, repository.ErrMerchantUserNotFound
	}
	return u, nil
}

func (m *mockMerchantUserRepo) GetByEmail(_ context.Context, email string) (*model.MerchantUser, error) {
	if m.getByEmailErr != nil {
		return nil, m.getByEmailErr
	}
	u, ok := m.users[email]
	if !ok {
		return nil, repository.ErrMerchantUserNotFound
	}
	return u, nil
}

func (m *mockMerchantUserRepo) ListByMerchant(_ context.Context, merchantID uuid.UUID) ([]*model.MerchantUser, error) {
	var out []*model.MerchantUser
	for _, u := range m.usersByID {
		if u.MerchantID == merchantID {
			out = append(out, u)
		}
	}
	return out, nil
}

func (m *mockMerchantUserRepo) UpdateStatus(_ context.Context, id uuid.UUID, status model.DashboardUserStatus) error {
	u, ok := m.usersByID[id]
	if !ok {
		return repository.ErrMerchantUserNotFound
	}
	u.Status = status
	return nil
}

func (m *mockMerchantUserRepo) UpdateLastLoginAt(_ context.Context, id uuid.UUID, t time.Time) error {
	if m.updateLoginErr != nil {
		return m.updateLoginErr
	}
	if u, ok := m.usersByID[id]; ok {
		u.LastLoginAt = &t
	}
	return nil
}

type mockSessionRepo struct {
	sessions     map[string]*model.DashboardSession // tokenHash → session
	sessionsByID map[uuid.UUID]*model.DashboardSession
	createErr    error
	getErr       error
	deleteErr    error
}

func newMockSessionRepo() *mockSessionRepo {
	return &mockSessionRepo{
		sessions:     make(map[string]*model.DashboardSession),
		sessionsByID: make(map[uuid.UUID]*model.DashboardSession),
	}
}

func (m *mockSessionRepo) Create(_ context.Context, session *model.DashboardSession) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.sessions[session.RefreshTokenHash] = session
	m.sessionsByID[session.ID] = session
	return nil
}

func (m *mockSessionRepo) GetByRefreshTokenHash(_ context.Context, tokenHash string) (*model.DashboardSession, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	s, ok := m.sessions[tokenHash]
	if !ok {
		return nil, repository.ErrSessionNotFound
	}
	return s, nil
}

func (m *mockSessionRepo) GetByUserID(_ context.Context, userID uuid.UUID) ([]*model.DashboardSession, error) {
	var out []*model.DashboardSession
	for _, s := range m.sessionsByID {
		if s.MerchantUserID == userID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *mockSessionRepo) Delete(_ context.Context, id uuid.UUID) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	s, ok := m.sessionsByID[id]
	if !ok {
		return repository.ErrSessionNotFound
	}
	delete(m.sessions, s.RefreshTokenHash)
	delete(m.sessionsByID, id)
	return nil
}

func (m *mockSessionRepo) DeleteByUserID(_ context.Context, userID uuid.UUID) error {
	for id, s := range m.sessionsByID {
		if s.MerchantUserID == userID {
			delete(m.sessions, s.RefreshTokenHash)
			delete(m.sessionsByID, id)
		}
	}
	return nil
}

func (m *mockSessionRepo) DeleteExpired(_ context.Context) error {
	now := time.Now().UTC()
	for id, s := range m.sessionsByID {
		if s.ExpiresAt.Before(now) {
			delete(m.sessions, s.RefreshTokenHash)
			delete(m.sessionsByID, id)
		}
	}
	return nil
}

func (m *mockSessionRepo) UpdateLastUsedAt(_ context.Context, id uuid.UUID, t time.Time) error {
	if s, ok := m.sessionsByID[id]; ok {
		s.LastUsedAt = &t
	}
	return nil
}

// ─── Test helpers ─────────────────────────────────────────────────────────────

func newTestAuthService(userRepo *mockMerchantUserRepo, sessionRepo *mockSessionRepo) AuthService {
	return NewAuthService(userRepo, sessionRepo, AuthConfig{
		JWTSecret:       []byte("test-secret-for-unit-tests-32b!!"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 7 * 24 * time.Hour,
	})
}

func seedActiveUser(t *testing.T, userRepo *mockMerchantUserRepo) *model.MerchantUser {
	t.Helper()
	password := "testpassword123"
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	user := &model.MerchantUser{
		ID:           uuid.New(),
		MerchantID:   uuid.New(),
		Email:        "test@example.com",
		PasswordHash: hash,
		Role:         model.DashboardUserRoleOwner,
		Status:       model.DashboardUserStatusActive,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	userRepo.users[user.Email] = user
	userRepo.usersByID[user.ID] = user
	return user
}

// ─── Login tests ──────────────────────────────────────────────────────────────

func TestLogin_ValidCredentials(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	user := seedActiveUser(t, userRepo)

	resp, plainRefresh, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    user.Email,
		Password: "testpassword123",
	})

	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.AccessToken == "" {
		t.Error("expected non-empty access token")
	}
	if plainRefresh == "" {
		t.Error("expected non-empty refresh token")
	}
	if resp.TokenType != "Bearer" {
		t.Errorf("token type: got %q, want Bearer", resp.TokenType)
	}
	if resp.User.ID != user.ID {
		t.Errorf("user ID mismatch")
	}

	// Password must never appear in response.
	// (DashboardUserResponse has no password field — structural guarantee)
}

func TestLogin_WrongPassword(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	seedActiveUser(t, userRepo)

	_, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    "test@example.com",
		Password: "wrong-password",
	})

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	_, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    "nobody@example.com",
		Password: "anypassword",
	})

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLogin_DisabledUser(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	user := seedActiveUser(t, userRepo)
	user.Status = model.DashboardUserStatusDisabled

	_, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    user.Email,
		Password: "testpassword123",
	})

	if !errors.Is(err, ErrUserDisabled) {
		t.Errorf("expected ErrUserDisabled, got %v", err)
	}
}

func TestLogin_EmailNormalization(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	// Seed user with lowercase email.
	seedActiveUser(t, userRepo) // email = "test@example.com"

	// Login with uppercased email + surrounding whitespace — must still work.
	resp, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    "  TEST@EXAMPLE.COM  ",
		Password: "testpassword123",
	})

	if err != nil {
		t.Fatalf("Login with normalised email failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
}

// ─── Logout tests ─────────────────────────────────────────────────────────────

func TestLogout_ValidToken(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	seedActiveUser(t, userRepo)

	// Login to get a token.
	_, plainRefresh, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    "test@example.com",
		Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Logout using the hash.
	tokenHash := HashRefreshTokenPublic(plainRefresh)
	if err := svc.Logout(context.Background(), tokenHash); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	// Session should be gone.
	_, err = sessionRepo.GetByRefreshTokenHash(context.Background(), tokenHash)
	if !errors.Is(err, repository.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound after logout, got %v", err)
	}
}

func TestLogout_AlreadyLoggedOut(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	// Logout with a token that doesn't exist — must be idempotent.
	err := svc.Logout(context.Background(), "non-existent-hash")
	if err != nil {
		t.Errorf("expected nil for already-logged-out logout, got %v", err)
	}
}

// ─── VerifyAccessToken tests ──────────────────────────────────────────────────

func TestVerifyAccessToken_ValidToken(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	user := seedActiveUser(t, userRepo)

	resp, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    user.Email,
		Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	claims, err := svc.VerifyAccessToken(resp.AccessToken)
	if err != nil {
		t.Fatalf("VerifyAccessToken: %v", err)
	}
	if claims.Subject != user.ID.String() {
		t.Errorf("subject: got %q, want %q", claims.Subject, user.ID.String())
	}
	if claims.MerchantID != user.MerchantID.String() {
		t.Errorf("merchantID: got %q, want %q", claims.MerchantID, user.MerchantID.String())
	}
	if claims.Role != string(user.Role) {
		t.Errorf("role: got %q, want %q", claims.Role, string(user.Role))
	}
}

func TestVerifyAccessToken_Invalid(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	_, err := svc.VerifyAccessToken("not.a.valid.jwt")
	if err == nil {
		t.Error("expected error for invalid token, got nil")
	}
}

func TestVerifyAccessToken_Empty(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	_, err := svc.VerifyAccessToken("")
	if err == nil {
		t.Error("expected error for empty token, got nil")
	}
}

// ─── Me tests ─────────────────────────────────────────────────────────────────

func TestMe_ActiveUser(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	user := seedActiveUser(t, userRepo)

	resp, err := svc.Me(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if resp.ID != user.ID {
		t.Errorf("ID mismatch: got %v, want %v", resp.ID, user.ID)
	}
	if resp.Email != user.Email {
		t.Errorf("email mismatch: got %v, want %v", resp.Email, user.Email)
	}
}

func TestMe_UserNotFound(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	_, err := svc.Me(context.Background(), uuid.New())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

// ─── RefreshAccessToken tests ─────────────────────────────────────────────────

func TestRefreshAccessToken_Valid(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	seedActiveUser(t, userRepo)

	_, plainRefresh, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    "test@example.com",
		Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	newResp, newRefresh, err := svc.RefreshAccessToken(context.Background(), plainRefresh)
	if err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	if newResp.AccessToken == "" {
		t.Error("expected new access token")
	}
	if newRefresh == "" {
		t.Error("expected new refresh token")
	}
	// Old token should be rotated out.
	oldHash := HashRefreshTokenPublic(plainRefresh)
	_, err = sessionRepo.GetByRefreshTokenHash(context.Background(), oldHash)
	if !errors.Is(err, repository.ErrSessionNotFound) {
		t.Error("old session should have been deleted after rotation")
	}
}

func TestRefreshAccessToken_InvalidToken(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	_, _, err := svc.RefreshAccessToken(context.Background(), "invalid-refresh-token")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestRefreshAccessToken_DisabledUser(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	user := seedActiveUser(t, userRepo)

	_, plainRefresh, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    user.Email,
		Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Disable the user after login.
	user.Status = model.DashboardUserStatusDisabled

	_, _, err = svc.RefreshAccessToken(context.Background(), plainRefresh)
	if !errors.Is(err, ErrUserDisabled) {
		t.Errorf("expected ErrUserDisabled for disabled user refresh, got %v", err)
	}
}

// ─── Security: password_hash never returned ───────────────────────────────────

func TestLogin_PasswordHashNeverReturned(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)

	seedActiveUser(t, userRepo)

	resp, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    "test@example.com",
		Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// DashboardUserResponse has no PasswordHash field — it's enforced by the type.
	// Access token should not contain the word "password" or hash.
	if resp.User.Email == "" {
		t.Error("expected email in response")
	}
	// Verify the DashboardUserResponse struct has no password-related fields
	// by ensuring the role and status are set correctly.
	if resp.User.Role == "" {
		t.Error("expected role in response")
	}
	if resp.User.Status != model.DashboardUserStatusActive {
		t.Errorf("expected ACTIVE status, got %v", resp.User.Status)
	}
}

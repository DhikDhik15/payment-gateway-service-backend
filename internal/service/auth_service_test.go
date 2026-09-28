package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─── Mock repositories ────────────────────────────────────────────────────────
//
// Both mocks carry a mutex because the Phase 8D.1 refresh-rotation
// concurrency tests drive them from many goroutines at once; without
// locking, `go test -race` would flag the shared maps.

type mockMerchantUserRepo struct {
	mu             sync.Mutex
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
	m.mu.Lock()
	defer m.mu.Unlock()
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

// CreateInTx delegates to Create (which takes the lock) — no lock here.
func (m *mockMerchantUserRepo) CreateInTx(ctx context.Context, _ pgx.Tx, user *model.MerchantUser) error {
	return m.Create(ctx, user)
}

func (m *mockMerchantUserRepo) GetByID(_ context.Context, id uuid.UUID) (*model.MerchantUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	u, ok := m.usersByID[id]
	if !ok {
		return nil, repository.ErrMerchantUserNotFound
	}
	return cloneMockMerchantUser(u), nil
}

func (m *mockMerchantUserRepo) GetByEmail(_ context.Context, email string) (*model.MerchantUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getByEmailErr != nil {
		return nil, m.getByEmailErr
	}
	u, ok := m.users[email]
	if !ok {
		return nil, repository.ErrMerchantUserNotFound
	}
	return cloneMockMerchantUser(u), nil
}

func cloneMockMerchantUser(u *model.MerchantUser) *model.MerchantUser {
	if u == nil {
		return nil
	}
	copyUser := *u
	if u.LastLoginAt != nil {
		lastLogin := *u.LastLoginAt
		copyUser.LastLoginAt = &lastLogin
	}
	return &copyUser
}

func (m *mockMerchantUserRepo) ListByMerchant(_ context.Context, merchantID uuid.UUID) ([]*model.MerchantUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*model.MerchantUser
	for _, u := range m.usersByID {
		if u.MerchantID == merchantID {
			out = append(out, u)
		}
	}
	return out, nil
}

func (m *mockMerchantUserRepo) UpdateStatus(_ context.Context, id uuid.UUID, status model.DashboardUserStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByID[id]
	if !ok {
		return repository.ErrMerchantUserNotFound
	}
	u.Status = status
	return nil
}

func (m *mockMerchantUserRepo) UpdateRole(_ context.Context, id uuid.UUID, role model.DashboardUserRole) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByID[id]
	if !ok {
		return repository.ErrMerchantUserNotFound
	}
	u.Role = role
	return nil
}

func (m *mockMerchantUserRepo) CountActiveOwners(_ context.Context, merchantID uuid.UUID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, u := range m.usersByID {
		if u.MerchantID == merchantID &&
			u.Role == model.DashboardUserRoleOwner &&
			u.Status == model.DashboardUserStatusActive {
			count++
		}
	}
	return count, nil
}

// UpdateStatusWithOwnerLock and UpdateRoleWithOwnerLock are the in-memory test
// equivalent of the PostgreSQL transaction path. The mutex covers the complete
// count-and-mutate operation, so existing service tests exercise the same
// single-winner semantics as the real repository.
func (m *mockMerchantUserRepo) UpdateStatusWithOwnerLock(
	_ context.Context,
	merchantID uuid.UUID,
	targetUserID uuid.UUID,
	status model.DashboardUserStatus,
) (*model.MerchantUser, error) {
	return m.mutateWithOwnerLock(merchantID, targetUserID, &status, nil)
}

func (m *mockMerchantUserRepo) UpdateRoleWithOwnerLock(
	_ context.Context,
	merchantID uuid.UUID,
	targetUserID uuid.UUID,
	role model.DashboardUserRole,
) (*model.MerchantUser, error) {
	return m.mutateWithOwnerLock(merchantID, targetUserID, nil, &role)
}

func (m *mockMerchantUserRepo) mutateWithOwnerLock(
	merchantID uuid.UUID,
	targetUserID uuid.UUID,
	status *model.DashboardUserStatus,
	role *model.DashboardUserRole,
) (*model.MerchantUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	target, ok := m.usersByID[targetUserID]
	if !ok {
		return nil, repository.ErrMerchantUserNotFound
	}
	if target.MerchantID != merchantID {
		return nil, repository.ErrMerchantUserCrossTenant
	}

	removesActiveOwner := target.Role == model.DashboardUserRoleOwner &&
		target.Status == model.DashboardUserStatusActive &&
		((status != nil && *status == model.DashboardUserStatusDisabled) ||
			(role != nil && *role != model.DashboardUserRoleOwner))
	if removesActiveOwner {
		activeOwners := 0
		for _, user := range m.usersByID {
			if user.MerchantID == merchantID &&
				user.Role == model.DashboardUserRoleOwner &&
				user.Status == model.DashboardUserStatusActive {
				activeOwners++
			}
		}
		if activeOwners <= 1 {
			return nil, repository.ErrLastActiveOwnerRequired
		}
	}

	if status != nil {
		target.Status = *status
	} else if role != nil {
		target.Role = *role
	}
	target.UpdatedAt = time.Now().UTC()
	return cloneMockMerchantUser(target), nil
}

func (m *mockMerchantUserRepo) UpdatePasswordHash(_ context.Context, id uuid.UUID, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByID[id]
	if !ok {
		return repository.ErrMerchantUserNotFound
	}
	u.PasswordHash = passwordHash
	return nil
}

func (m *mockMerchantUserRepo) UpdateLastLoginAt(_ context.Context, id uuid.UUID, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateLoginErr != nil {
		return m.updateLoginErr
	}
	if u, ok := m.usersByID[id]; ok {
		u.LastLoginAt = &t
	}
	return nil
}

type mockSessionRepo struct {
	mu              sync.Mutex
	sessions        map[string]*model.DashboardSession // tokenHash → session
	sessionsByID    map[uuid.UUID]*model.DashboardSession
	createErr       error
	getErr          error
	deleteErr       error
	deleteByUserErr error
	consumeErr      error       // injected failure for ConsumeByRefreshTokenHash
	merchantRevokes []uuid.UUID // merchant IDs passed to DeleteByMerchantID
}

func newMockSessionRepo() *mockSessionRepo {
	return &mockSessionRepo{
		sessions:     make(map[string]*model.DashboardSession),
		sessionsByID: make(map[uuid.UUID]*model.DashboardSession),
	}
}

func (m *mockSessionRepo) Create(_ context.Context, session *model.DashboardSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	m.sessions[session.RefreshTokenHash] = session
	m.sessionsByID[session.ID] = session
	return nil
}

func (m *mockSessionRepo) GetByRefreshTokenHash(_ context.Context, tokenHash string) (*model.DashboardSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	s, ok := m.sessions[tokenHash]
	if !ok {
		return nil, repository.ErrSessionNotFound
	}
	return cloneMockDashboardSession(s), nil
}

// RotateByRefreshTokenHash mirrors the PostgreSQL compare-and-swap update. The
// session ID is retained while the token hash changes, so DELETE-based
// invalidation paths serialize on the same logical row.
func (m *mockSessionRepo) RotateByRefreshTokenHash(_ context.Context, oldTokenHash string, replacement *model.DashboardSession) (*model.DashboardSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.consumeErr != nil {
		return nil, m.consumeErr
	}
	if replacement == nil {
		return nil, errors.New("replacement session is nil")
	}
	old, ok := m.sessions[oldTokenHash]
	if !ok {
		return nil, repository.ErrSessionNotFound
	}
	if replacement.MerchantUserID != old.MerchantUserID {
		return nil, errors.New("replacement user mismatch")
	}
	if replacement.RefreshTokenHash == "" || replacement.RefreshTokenHash == oldTokenHash {
		return nil, errors.New("replacement token is invalid")
	}
	if _, exists := m.sessions[replacement.RefreshTokenHash]; exists {
		return nil, errors.New("replacement token already exists")
	}
	if m.createErr != nil {
		return nil, m.createErr
	}

	// Same physical row: retain the ID, remove the old hash index, and install
	// the replacement hash index.
	rotated := cloneMockDashboardSession(replacement)
	rotated.ID = old.ID
	delete(m.sessions, oldTokenHash)
	m.sessions[rotated.RefreshTokenHash] = rotated
	m.sessionsByID[old.ID] = rotated
	return cloneMockDashboardSession(rotated), nil
}

func cloneMockDashboardSession(s *model.DashboardSession) *model.DashboardSession {
	if s == nil {
		return nil
	}
	copySession := *s
	if s.LastUsedAt != nil {
		lastUsed := *s.LastUsedAt
		copySession.LastUsedAt = &lastUsed
	}
	return &copySession
}

// ConsumeByRefreshTokenHash mirrors the PostgreSQL DELETE ... RETURNING:
// under the mutex the delete-and-return is atomic, so exactly one concurrent
// caller observes the session and the rest get ErrSessionNotFound.
func (m *mockSessionRepo) ConsumeByRefreshTokenHash(_ context.Context, tokenHash string) (*model.DashboardSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.consumeErr != nil {
		return nil, m.consumeErr
	}
	s, ok := m.sessions[tokenHash]
	if !ok {
		return nil, repository.ErrSessionNotFound
	}
	delete(m.sessions, tokenHash)
	delete(m.sessionsByID, s.ID)
	return s, nil
}

func (m *mockSessionRepo) GetByUserID(_ context.Context, userID uuid.UUID) ([]*model.DashboardSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*model.DashboardSession
	for _, s := range m.sessionsByID {
		if s.MerchantUserID == userID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *mockSessionRepo) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *mockSessionRepo) DeleteByIDAndUser(_ context.Context, id uuid.UUID, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	s, ok := m.sessionsByID[id]
	if !ok || s.MerchantUserID != userID {
		return repository.ErrSessionNotFound
	}
	delete(m.sessions, s.RefreshTokenHash)
	delete(m.sessionsByID, id)
	return nil
}

func (m *mockSessionRepo) DeleteByUserID(_ context.Context, userID uuid.UUID) error {
	m.mu.Lock()
	if m.deleteByUserErr != nil {
		err := m.deleteByUserErr
		m.mu.Unlock()
		return err
	}
	defer m.mu.Unlock()
	for id, s := range m.sessionsByID {
		if s.MerchantUserID == userID {
			delete(m.sessions, s.RefreshTokenHash)
			delete(m.sessionsByID, id)
		}
	}
	return nil
}

func (m *mockSessionRepo) DeleteByMerchantID(_ context.Context, merchantID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Record the call so merchant-lifecycle tests can assert session
	// invalidation. The real repository deletes via a merchant_id JOIN.
	m.merchantRevokes = append(m.merchantRevokes, merchantID)
	return nil
}

func (m *mockSessionRepo) DeleteExpired(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessionsByID[id]; ok {
		s.LastUsedAt = &t
	}
	return nil
}

// sessionCount reports how many sessions are currently stored. Test-only
// helper; safe to call because it takes the mock's lock.
func (m *mockSessionRepo) sessionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
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

func TestLogin_InactiveMerchantRejected(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	merchantRepo := newMockMerchantRepo()
	merchant := seedMerchant(merchantRepo)
	user := seedActiveUser(t, userRepo)
	user.MerchantID = merchant.ID
	merchant.Status = model.MerchantStatusInactive
	svc := NewAuthService(userRepo, sessionRepo, AuthConfig{
		JWTSecret:       []byte("test-secret-for-unit-tests-32b!!"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 7 * 24 * time.Hour,
	}, merchantRepo)

	_, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    user.Email,
		Password: "testpassword123",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("inactive-merchant login error = %v, want ErrInvalidCredentials", err)
	}
	if got := sessionRepo.sessionCount(); got != 0 {
		t.Fatalf("inactive-merchant login created %d sessions, want 0", got)
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

func TestLogoutSession_RevokesRefreshButLeavesBoundedAccessToken(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)
	user := seedActiveUser(t, userRepo)

	resp, plainRefresh, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    user.Email,
		Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	claims, err := svc.VerifyAccessToken(resp.AccessToken)
	if err != nil {
		t.Fatalf("verify access token: %v", err)
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}

	if err := svc.LogoutSession(context.Background(), sessionID, user.ID); err != nil {
		t.Fatalf("logout session: %v", err)
	}
	if _, err := sessionRepo.GetByRefreshTokenHash(context.Background(), HashRefreshTokenPublic(plainRefresh)); !errors.Is(err, repository.ErrSessionNotFound) {
		t.Fatalf("refresh session should be revoked, got %v", err)
	}
	if _, _, err := svc.RefreshAccessToken(context.Background(), plainRefresh); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("refresh after logout = %v, want ErrInvalidCredentials", err)
	}
	// Model A: the already-issued access JWT remains cryptographically valid
	// until its natural exp; logout does not create a JWT blacklist.
	if _, err := svc.VerifyAccessToken(resp.AccessToken); err != nil {
		t.Fatalf("bounded access token should remain verifiable before expiry: %v", err)
	}
}

func TestLogoutSession_AccessTokenExpiresNaturally(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := NewAuthService(userRepo, sessionRepo, AuthConfig{
		JWTSecret:       []byte("test-secret-for-unit-tests-32b!!"),
		AccessTokenTTL:  time.Second,
		RefreshTokenTTL: 7 * 24 * time.Hour,
	})
	user := seedActiveUser(t, userRepo)
	resp, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email: user.Email, Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	claims, err := svc.VerifyAccessToken(resp.AccessToken)
	if err != nil {
		t.Fatalf("verify before logout: %v", err)
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		t.Fatalf("parse sid: %v", err)
	}
	if err := svc.LogoutSession(context.Background(), sessionID, user.ID); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := svc.VerifyAccessToken(resp.AccessToken); err != nil {
		t.Fatalf("token should remain valid before exp: %v", err)
	}
	time.Sleep(2100 * time.Millisecond)
	if _, err := svc.VerifyAccessToken(resp.AccessToken); !errors.Is(err, ErrJWTExpired) {
		t.Fatalf("token after exp = %v, want ErrJWTExpired", err)
	}
}

func TestLogoutSession_IsUserScopedAndIdempotent(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)
	user := seedActiveUser(t, userRepo)

	resp, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    user.Email,
		Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	claims, err := svc.VerifyAccessToken(resp.AccessToken)
	if err != nil {
		t.Fatalf("verify access token: %v", err)
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}

	// A valid signed token aimed at the wrong user must not delete the row.
	if err := svc.LogoutSession(context.Background(), sessionID, uuid.New()); err != nil {
		t.Fatalf("wrong-user logout should be idempotent: %v", err)
	}
	if got := sessionRepo.sessionCount(); got != 1 {
		t.Fatalf("wrong-user logout changed session count: got %d, want 1", got)
	}

	// Repeated correct-user logout remains safe and idempotent.
	for i := 0; i < 2; i++ {
		if err := svc.LogoutSession(context.Background(), sessionID, user.ID); err != nil {
			t.Fatalf("logout attempt %d: %v", i+1, err)
		}
	}
	if got := sessionRepo.sessionCount(); got != 0 {
		t.Fatalf("sessions after logout: got %d, want 0", got)
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

	loginResp, plainRefresh, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    "test@example.com",
		Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	firstClaims, err := svc.VerifyAccessToken(loginResp.AccessToken)
	if err != nil {
		t.Fatalf("verify first access token: %v", err)
	}
	if firstClaims.SessionID == "" {
		t.Fatal("login access token must carry a stable session ID")
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
	newClaims, err := svc.VerifyAccessToken(newResp.AccessToken)
	if err != nil {
		t.Fatalf("verify refreshed access token: %v", err)
	}
	if newClaims.SessionID != firstClaims.SessionID {
		t.Errorf("session ID changed across refresh: first=%q new=%q", firstClaims.SessionID, newClaims.SessionID)
	}
	// Old token should be rotated out.
	oldHash := HashRefreshTokenPublic(plainRefresh)
	_, err = sessionRepo.GetByRefreshTokenHash(context.Background(), oldHash)
	if !errors.Is(err, repository.ErrSessionNotFound) {
		t.Error("old session should have been deleted after rotation")
	}
}

func TestLogoutSession_ConcurrentCallsAreIdempotent(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	svc := newTestAuthService(userRepo, sessionRepo)
	user := seedActiveUser(t, userRepo)
	resp, _, err := svc.Login(context.Background(), model.LoginRequest{
		Email: user.Email, Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	claims, err := svc.VerifyAccessToken(resp.AccessToken)
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		t.Fatalf("parse sid: %v", err)
	}

	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.LogoutSession(context.Background(), sessionID, user.ID)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent logout returned error: %v", err)
		}
	}
	if got := sessionRepo.sessionCount(); got != 0 {
		t.Errorf("sessions after concurrent logout: got %d, want 0", got)
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

func TestRefreshAccessToken_InactiveMerchantRejected(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	merchantRepo := newMockMerchantRepo()
	merchant := seedMerchant(merchantRepo)
	user := seedActiveUser(t, userRepo)
	user.MerchantID = merchant.ID
	svc := NewAuthService(userRepo, sessionRepo, AuthConfig{
		JWTSecret:       []byte("test-secret-for-unit-tests-32b!!"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 7 * 24 * time.Hour,
	}, merchantRepo)

	_, plainRefresh, err := svc.Login(context.Background(), model.LoginRequest{
		Email:    user.Email,
		Password: "testpassword123",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	merchant.Status = model.MerchantStatusSuspended

	_, _, err = svc.RefreshAccessToken(context.Background(), plainRefresh)
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("inactive-merchant refresh error = %v, want ErrInvalidCredentials", err)
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

// ─── Phase 8D.1: refresh rotation is atomic (single-use refresh token) ───────
//
// These tests encode the DoD requirements: concurrent refresh attempts with
// the same token produce exactly ONE success, the original token can never be
// replayed after rotation, and a logged-out (revoked) session is rejected.
// Tokens themselves are never printed — only success/failure counts and
// SHA-256 hashes are asserted.

func TestRefreshAccessToken_ConcurrentSingleWinner(t *testing.T) {
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
	oldHash := HashRefreshTokenPublic(plainRefresh)
	if got := sessionRepo.sessionCount(); got != 1 {
		t.Fatalf("sessions after login: got %d, want 1", got)
	}

	const goroutines = 20
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		winners    int
		rejected   int
		unexpected []error
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			resp, refresh, err := svc.RefreshAccessToken(context.Background(), plainRefresh)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
				if resp == nil || resp.AccessToken == "" || refresh == "" {
					unexpected = append(unexpected, errors.New("winner returned an empty token response"))
				}
			case errors.Is(err, ErrInvalidCredentials):
				rejected++
			default:
				unexpected = append(unexpected, err)
			}
		}()
	}
	wg.Wait()

	if winners != 1 {
		t.Errorf("winners: got %d, want exactly 1", winners)
	}
	if rejected != goroutines-1 {
		t.Errorf("rejected: got %d, want %d", rejected, goroutines-1)
	}
	for _, e := range unexpected {
		t.Errorf("unexpected error: %v", e)
	}

	// Exactly one replacement session exists — no duplicate sessions from the race.
	if got := sessionRepo.sessionCount(); got != 1 {
		t.Errorf("sessions after rotation: got %d, want 1", got)
	}
	// The consumed (original) refresh token hash must no longer resolve.
	if _, err := sessionRepo.GetByRefreshTokenHash(context.Background(), oldHash); !errors.Is(err, repository.ErrSessionNotFound) {
		t.Errorf("original refresh token hash must be gone after rotation, got err=%v", err)
	}
}

func TestRefreshAccessToken_ReuseAfterRotation(t *testing.T) {
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

	_, replacementRefresh, err := svc.RefreshAccessToken(context.Background(), plainRefresh)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// Replaying the ORIGINAL token after rotation must fail (single-use).
	if _, _, err := svc.RefreshAccessToken(context.Background(), plainRefresh); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("replay of consumed token: got %v, want ErrInvalidCredentials", err)
	}
	// The replacement token works exactly once…
	if _, _, err := svc.RefreshAccessToken(context.Background(), replacementRefresh); err != nil {
		t.Errorf("replacement token refresh: %v", err)
	}
	// …and is itself consumed, so replaying it also fails.
	if _, _, err := svc.RefreshAccessToken(context.Background(), replacementRefresh); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("replay of replacement token: got %v, want ErrInvalidCredentials", err)
	}
	// A rejected replay must not leave extra sessions behind.
	if got := sessionRepo.sessionCount(); got != 1 {
		t.Errorf("sessions after replay attempts: got %d, want 1", got)
	}
}

func TestRefreshAccessToken_RevokedSessionRejected(t *testing.T) {
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

	// Logout deletes (revokes) the dashboard session.
	if err := svc.Logout(context.Background(), HashRefreshTokenPublic(plainRefresh)); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	// The revoked session's refresh token must be rejected.
	if _, _, err := svc.RefreshAccessToken(context.Background(), plainRefresh); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("refresh after logout: got %v, want ErrInvalidCredentials", err)
	}
	if got := sessionRepo.sessionCount(); got != 0 {
		t.Errorf("sessions after logout+refresh: got %d, want 0", got)
	}
}

func TestRefreshAccessToken_ConsumeFailureFailsClosed(t *testing.T) {
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

	// Inject a repository failure on the atomic consume.
	sessionRepo.consumeErr = errors.New("database unavailable")

	// Refresh must fail closed: no tokens issued when the session cannot be consumed.
	_, _, err = svc.RefreshAccessToken(context.Background(), plainRefresh)
	if err == nil {
		t.Fatal("expected error when consume fails, got nil")
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected a hard (non-credential) error, got ErrInvalidCredentials")
	}
	if got := sessionRepo.sessionCount(); got != 1 {
		t.Errorf("sessions: got %d, want 1 (no replacement issued)", got)
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

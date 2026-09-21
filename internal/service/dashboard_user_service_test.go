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

// ─── Mock merchant repository (minimal, for DashboardUserService) ─────────────

type mockMerchantRepo struct {
	merchants   map[uuid.UUID]*model.Merchant
	notFoundErr bool
}

func newMockMerchantRepo() *mockMerchantRepo {
	return &mockMerchantRepo{
		merchants: make(map[uuid.UUID]*model.Merchant),
	}
}

func (m *mockMerchantRepo) Create(_ context.Context, merchant *model.Merchant) error {
	m.merchants[merchant.ID] = merchant
	return nil
}

func (m *mockMerchantRepo) GetByID(_ context.Context, id uuid.UUID) (*model.Merchant, error) {
	if m.notFoundErr {
		return nil, repository.ErrMerchantNotFound
	}
	merchant, ok := m.merchants[id]
	if !ok {
		return nil, repository.ErrMerchantNotFound
	}
	return merchant, nil
}

func (m *mockMerchantRepo) GetByAPIKey(_ context.Context, _ string) (*model.Merchant, error) {
	return nil, repository.ErrMerchantNotFound
}

func (m *mockMerchantRepo) ExistsByCode(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func seedMerchant(merchantRepo *mockMerchantRepo) *model.Merchant {
	m := &model.Merchant{
		ID:        uuid.New(),
		Name:      "Test Merchant",
		Code:      "testmerchant",
		Status:    model.MerchantStatusActive,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	merchantRepo.merchants[m.ID] = m
	return m
}

func newTestDashboardUserService(userRepo *mockMerchantUserRepo, merchantRepo *mockMerchantRepo) DashboardUserService {
	return NewDashboardUserService(userRepo, newMockSessionRepo(), merchantRepo)
}

// ─── CreateUser tests ─────────────────────────────────────────────────────────

func TestCreateUser_Success(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)

	resp, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email:    "owner@example.com",
		Password: "securepassword",
		Role:     model.DashboardUserRoleOwner,
	})

	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if resp.Email != "owner@example.com" {
		t.Errorf("email: got %q, want %q", resp.Email, "owner@example.com")
	}
	if resp.Role != model.DashboardUserRoleOwner {
		t.Errorf("role: got %v, want OWNER", resp.Role)
	}
	if resp.Status != model.DashboardUserStatusActive {
		t.Errorf("status: got %v, want ACTIVE", resp.Status)
	}
	if resp.MerchantID != merchant.ID {
		t.Errorf("merchant ID mismatch")
	}
}

func TestCreateUser_EmailNormalization(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)

	resp, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email:    "  OWNER@EXAMPLE.COM  ",
		Password: "securepassword",
		Role:     model.DashboardUserRoleOwner,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// Email should be normalised to lowercase.
	if resp.Email != "owner@example.com" {
		t.Errorf("email not normalised: got %q, want %q", resp.Email, "owner@example.com")
	}
}

func TestCreateUser_DuplicateEmail(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	req := model.CreateDashboardUserRequest{
		Email:    "dup@example.com",
		Password: "securepassword",
		Role:     model.DashboardUserRoleOwner,
	}

	if _, err := svc.CreateUser(context.Background(), merchant.ID, req); err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}

	_, err := svc.CreateUser(context.Background(), merchant.ID, req)
	if !errors.Is(err, ErrEmailAlreadyExists) {
		t.Errorf("expected ErrEmailAlreadyExists, got %v", err)
	}
}

func TestCreateUser_UnknownMerchant(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	_, err := svc.CreateUser(context.Background(), uuid.New(), model.CreateDashboardUserRequest{
		Email:    "test@example.com",
		Password: "securepassword",
		Role:     model.DashboardUserRoleOwner,
	})
	if !errors.Is(err, repository.ErrMerchantNotFound) {
		t.Errorf("expected ErrMerchantNotFound, got %v", err)
	}
}

// ─── ListUsers tests ──────────────────────────────────────────────────────────

func TestListUsers_Success(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)

	for _, role := range []model.DashboardUserRole{
		model.DashboardUserRoleOwner,
		model.DashboardUserRoleAdmin,
		model.DashboardUserRoleViewer,
	} {
		_, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
			Email:    string(role) + "@example.com",
			Password: "password123",
			Role:     role,
		})
		if err != nil {
			t.Fatalf("CreateUser(%v): %v", role, err)
		}
	}

	users, err := svc.ListUsers(context.Background(), merchant.ID)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 3 {
		t.Errorf("expected 3 users, got %d", len(users))
	}
}

func TestListUsers_MerchantIsolation(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchantA := seedMerchant(merchantRepo)
	merchantB := seedMerchant(merchantRepo)

	// Create a user for merchant A.
	_, err := svc.CreateUser(context.Background(), merchantA.ID, model.CreateDashboardUserRequest{
		Email:    "a@example.com",
		Password: "password123",
		Role:     model.DashboardUserRoleOwner,
	})
	if err != nil {
		t.Fatalf("CreateUser for A: %v", err)
	}

	// List users for merchant B — should see none.
	users, err := svc.ListUsers(context.Background(), merchantB.ID)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("merchant B should see 0 users, got %d", len(users))
	}
}

// ─── UpdateUserStatus tests ───────────────────────────────────────────────────

func TestUpdateUserStatus_OwnerCanDisableOtherUser(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	sessionRepo := newMockSessionRepo()
	svc := NewDashboardUserService(userRepo, sessionRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)

	owner, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email: "owner@example.com", Password: "pass1234", Role: model.DashboardUserRoleOwner,
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email: "admin@example.com", Password: "pass1234", Role: model.DashboardUserRoleAdmin,
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}

	// Active refresh session must be revoked when the user is disabled.
	if err := sessionRepo.Create(context.Background(), &model.DashboardSession{
		ID:               uuid.New(),
		MerchantUserID:   target.ID,
		RefreshTokenHash: "abc123sessionhash",
		ExpiresAt:        time.Now().UTC().Add(24 * time.Hour),
		CreatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	updated, err := svc.UpdateUserStatus(
		context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		target.ID, model.DashboardUserStatusDisabled,
	)
	if err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if updated.Status != model.DashboardUserStatusDisabled {
		t.Errorf("expected DISABLED, got %v", updated.Status)
	}
	if len(sessionRepo.sessionsByID) != 0 {
		t.Errorf("expected target sessions revoked on disable, got %d", len(sessionRepo.sessionsByID))
	}
}

func TestUpdateUserStatus_AdminCannotChangeStatus(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)

	admin, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email: "admin@example.com", Password: "pass1234", Role: model.DashboardUserRoleAdmin,
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	target, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email: "viewer@example.com", Password: "pass1234", Role: model.DashboardUserRoleViewer,
	})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}

	_, err = svc.UpdateUserStatus(
		context.Background(),
		admin.ID, model.DashboardUserRoleAdmin, merchant.ID,
		target.ID, model.DashboardUserStatusDisabled,
	)
	if !errors.Is(err, ErrInsufficientRole) {
		t.Errorf("expected ErrInsufficientRole, got %v", err)
	}
}

func TestUpdateUserStatus_ViewerCannotChangeStatus(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)

	viewer, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email: "viewer@example.com", Password: "pass1234", Role: model.DashboardUserRoleViewer,
	})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	target, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email: "admin@example.com", Password: "pass1234", Role: model.DashboardUserRoleAdmin,
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	_, err = svc.UpdateUserStatus(
		context.Background(),
		viewer.ID, model.DashboardUserRoleViewer, merchant.ID,
		target.ID, model.DashboardUserStatusDisabled,
	)
	if !errors.Is(err, ErrInsufficientRole) {
		t.Errorf("expected ErrInsufficientRole for viewer, got %v", err)
	}
}

func TestUpdateUserStatus_SelfDisablePrevented(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)

	owner, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email: "owner@example.com", Password: "pass1234", Role: model.DashboardUserRoleOwner,
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}

	_, err = svc.UpdateUserStatus(
		context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		owner.ID, model.DashboardUserStatusDisabled, // trying to disable self
	)
	if !errors.Is(err, ErrSelfDisable) {
		t.Errorf("expected ErrSelfDisable, got %v", err)
	}
}

func TestUpdateUserStatus_CrossMerchantIsolation(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchantA := seedMerchant(merchantRepo)
	merchantB := seedMerchant(merchantRepo)

	ownerA, err := svc.CreateUser(context.Background(), merchantA.ID, model.CreateDashboardUserRequest{
		Email: "owner-a@example.com", Password: "pass1234", Role: model.DashboardUserRoleOwner,
	})
	if err != nil {
		t.Fatalf("create owner A: %v", err)
	}
	targetB, err := svc.CreateUser(context.Background(), merchantB.ID, model.CreateDashboardUserRequest{
		Email: "viewer-b@example.com", Password: "pass1234", Role: model.DashboardUserRoleViewer,
	})
	if err != nil {
		t.Fatalf("create target B: %v", err)
	}

	// Owner A trying to disable a user in merchant B.
	_, err = svc.UpdateUserStatus(
		context.Background(),
		ownerA.ID, model.DashboardUserRoleOwner, merchantA.ID,
		targetB.ID, model.DashboardUserStatusDisabled,
	)
	if !errors.Is(err, ErrCrossmerchantAccess) {
		t.Errorf("expected ErrCrossmerchantAccess, got %v", err)
	}
}

func TestUpdateUserStatus_UserNotFound(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)

	owner, err := svc.CreateUser(context.Background(), merchant.ID, model.CreateDashboardUserRequest{
		Email: "owner@example.com", Password: "pass1234", Role: model.DashboardUserRoleOwner,
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}

	_, err = svc.UpdateUserStatus(
		context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		uuid.New(), // non-existent user
		model.DashboardUserStatusDisabled,
	)
	if !errors.Is(err, ErrDashboardUserNotFound) {
		t.Errorf("expected ErrDashboardUserNotFound, got %v", err)
	}
}

// ─── Model validation tests ───────────────────────────────────────────────────

func TestDashboardUserRole_IsValid(t *testing.T) {
	valid := []model.DashboardUserRole{
		model.DashboardUserRoleOwner,
		model.DashboardUserRoleAdmin,
		model.DashboardUserRoleViewer,
	}
	for _, r := range valid {
		if !r.IsValid() {
			t.Errorf("role %q should be valid", r)
		}
	}
	if model.DashboardUserRole("SUPERADMIN").IsValid() {
		t.Error("SUPERADMIN should not be valid")
	}
}

func TestDashboardUserStatus_IsValid(t *testing.T) {
	if !model.DashboardUserStatusActive.IsValid() {
		t.Error("ACTIVE should be valid")
	}
	if !model.DashboardUserStatusDisabled.IsValid() {
		t.Error("DISABLED should be valid")
	}
	if model.DashboardUserStatus("PENDING").IsValid() {
		t.Error("PENDING should not be valid")
	}
}

func TestMerchantUser_IsActive(t *testing.T) {
	u := &model.MerchantUser{Status: model.DashboardUserStatusActive}
	if !u.IsActive() {
		t.Error("ACTIVE user should be active")
	}
	u.Status = model.DashboardUserStatusDisabled
	if u.IsActive() {
		t.Error("DISABLED user should not be active")
	}
}

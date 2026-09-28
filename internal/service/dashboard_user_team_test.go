package service

// dashboard_user_team_test.go — Phase 8A self-service team management tests.
//
// Covers:
//   - Role change authorization (OWNER only) and transition rules
//   - Last-OWNER invariant for role demotion and user disable
//   - User enable/disable hardening and session invalidation
//   - Tenant isolation between two merchants
//   - Session behavior on role change / re-enable
//   - Merchant lifecycle interaction with dashboard sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

// seedTeamUser creates an ACTIVE dashboard user with the given role.
func seedTeamUser(t *testing.T, svc DashboardUserService, merchantID uuid.UUID, email string, role model.DashboardUserRole) *model.DashboardUserResponse {
	t.Helper()
	resp, err := svc.CreateUser(context.Background(), merchantID, model.CreateDashboardUserRequest{
		Email:    email,
		Password: "password123",
		Role:     role,
	})
	if err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	return resp
}

// seedSessionFor creates a refresh session for a user directly in the mock.
func seedSessionFor(t *testing.T, sessionRepo *mockSessionRepo, userID uuid.UUID, tokenHash string) {
	t.Helper()
	if err := sessionRepo.Create(context.Background(), &model.DashboardSession{
		ID:               uuid.New(),
		MerchantUserID:   userID,
		RefreshTokenHash: tokenHash,
		ExpiresAt:        time.Now().UTC().Add(24 * time.Hour),
		CreatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// ─── Role change: allowed transitions (spec §18 #1–#4) ──────────────────────

func TestUpdateUserRole_OwnerChangesAdminToViewer(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)
	admin := seedTeamUser(t, svc, merchant.ID, "admin@example.com", model.DashboardUserRoleAdmin)

	updated, err := svc.UpdateUserRole(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		admin.ID, model.DashboardUserRoleViewer)
	if err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}
	if updated.Role != model.DashboardUserRoleViewer {
		t.Errorf("role: got %v, want VIEWER", updated.Role)
	}
}

func TestUpdateUserRole_OwnerChangesViewerToAdmin(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)
	viewer := seedTeamUser(t, svc, merchant.ID, "viewer@example.com", model.DashboardUserRoleViewer)

	updated, err := svc.UpdateUserRole(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		viewer.ID, model.DashboardUserRoleAdmin)
	if err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}
	if updated.Role != model.DashboardUserRoleAdmin {
		t.Errorf("role: got %v, want ADMIN", updated.Role)
	}
}

func TestUpdateUserRole_OwnerPromotesAdminToOwner(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)
	admin := seedTeamUser(t, svc, merchant.ID, "admin@example.com", model.DashboardUserRoleAdmin)

	updated, err := svc.UpdateUserRole(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		admin.ID, model.DashboardUserRoleOwner)
	if err != nil {
		t.Fatalf("UpdateUserRole promote: %v", err)
	}
	if updated.Role != model.DashboardUserRoleOwner {
		t.Errorf("role: got %v, want OWNER", updated.Role)
	}
}

func TestUpdateUserRole_OwnerDemotesOwnerWithAnotherOwnerRemaining(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	ownerA := seedTeamUser(t, svc, merchant.ID, "owner-a@example.com", model.DashboardUserRoleOwner)
	ownerB := seedTeamUser(t, svc, merchant.ID, "owner-b@example.com", model.DashboardUserRoleOwner)

	// OWNER A demotes OWNER B → ADMIN. Allowed: OWNER A remains active OWNER.
	updated, err := svc.UpdateUserRole(context.Background(),
		ownerA.ID, model.DashboardUserRoleOwner, merchant.ID,
		ownerB.ID, model.DashboardUserRoleAdmin)
	if err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}
	if updated.Role != model.DashboardUserRoleAdmin {
		t.Errorf("role: got %v, want ADMIN", updated.Role)
	}
}

// ─── Role change: last-OWNER invariant (spec §5, §6) ────────────────────────

func TestUpdateUserRole_CannotDemoteLastOwner_SelfDemotion(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	ownerA := seedTeamUser(t, svc, merchant.ID, "owner-a@example.com", model.DashboardUserRoleOwner)
	seedTeamUser(t, svc, merchant.ID, "admin-b@example.com", model.DashboardUserRoleAdmin)

	// OWNER A (the only ACTIVE OWNER) tries to demote themselves → reject.
	_, err := svc.UpdateUserRole(context.Background(),
		ownerA.ID, model.DashboardUserRoleOwner, merchant.ID,
		ownerA.ID, model.DashboardUserRoleAdmin)
	if !errors.Is(err, ErrLastOwnerRequired) {
		t.Errorf("expected ErrLastOwnerRequired, got %v", err)
	}

	// The role must be unchanged after the rejected operation.
	stored, err := userRepo.GetByID(context.Background(), ownerA.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Role != model.DashboardUserRoleOwner {
		t.Errorf("role must remain OWNER after rejected demotion, got %v", stored.Role)
	}
}

func TestUpdateUserRole_CannotDemoteLastOwner_AnotherOwnerTarget(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	ownerA := seedTeamUser(t, svc, merchant.ID, "owner-a@example.com", model.DashboardUserRoleOwner)
	seedTeamUser(t, svc, merchant.ID, "admin-b@example.com", model.DashboardUserRoleAdmin)

	// OWNER A attempts to demote OWNER A through a second (identical) target
	// reference — same invariant: the final ACTIVE OWNER cannot be demoted.
	// Covered as the target being the only ACTIVE OWNER regardless of caller.
	// Use the ADMIN as caller is impossible (OWNER-only), so the only caller
	// here is OWNER A demoting OWNER A — already covered above. This variant
	// proves the guard also fires when the caller is not the target: pass the
	// OWNER authority while the caller user is a different account, which is
	// the shape any future admin-override path would take.
	adminB, err := userRepo.GetByID(context.Background(), userByPhone(t, userRepo, "admin-b@example.com"))
	if err != nil {
		t.Fatalf("GetByID admin: %v", err)
	}

	_, err = svc.UpdateUserRole(context.Background(),
		adminB.ID, model.DashboardUserRoleOwner, merchant.ID, // callerRole presented as OWNER
		ownerA.ID, model.DashboardUserRoleAdmin)
	if !errors.Is(err, ErrLastOwnerRequired) {
		t.Errorf("expected ErrLastOwnerRequired for demoting the final OWNER, got %v", err)
	}
}

func TestUpdateUserRole_OwnerSelfDemotionWithAnotherOwnerRemaining(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	ownerA := seedTeamUser(t, svc, merchant.ID, "owner-a@example.com", model.DashboardUserRoleOwner)
	ownerB := seedTeamUser(t, svc, merchant.ID, "owner-b@example.com", model.DashboardUserRoleOwner)

	// OWNER A demotes themselves → ADMIN. Allowed because OWNER B remains.
	updated, err := svc.UpdateUserRole(context.Background(),
		ownerA.ID, model.DashboardUserRoleOwner, merchant.ID,
		ownerA.ID, model.DashboardUserRoleAdmin)
	if err != nil {
		t.Fatalf("self-demotion with another OWNER remaining: %v", err)
	}
	if updated.Role != model.DashboardUserRoleAdmin {
		t.Errorf("role: got %v, want ADMIN", updated.Role)
	}
	if ownerB.Role != model.DashboardUserRoleOwner {
		t.Errorf("OWNER B must be untouched, got %v", ownerB.Role)
	}
}

func TestUpdateUserRole_DemotingDisabledOwnerAllowed(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	ownerA := seedTeamUser(t, svc, merchant.ID, "owner-a@example.com", model.DashboardUserRoleOwner)
	ownerB := seedTeamUser(t, svc, merchant.ID, "owner-b@example.com", model.DashboardUserRoleOwner)

	// Disable OWNER B (allowed — OWNER A remains).
	if _, err := svc.UpdateUserStatus(context.Background(),
		ownerA.ID, model.DashboardUserRoleOwner, merchant.ID,
		ownerB.ID, model.DashboardUserStatusDisabled); err != nil {
		t.Fatalf("disable owner B: %v", err)
	}

	// Demoting the now-DISABLED OWNER does not affect the active-owner count.
	if _, err := svc.UpdateUserRole(context.Background(),
		ownerA.ID, model.DashboardUserRoleOwner, merchant.ID,
		ownerB.ID, model.DashboardUserRoleAdmin); err != nil {
		t.Fatalf("demote disabled owner: %v", err)
	}
}

// userByPhone resolves a user ID by email inside the mock repo.
func userByPhone(t *testing.T, userRepo *mockMerchantUserRepo, email string) uuid.UUID {
	t.Helper()
	u, err := userRepo.GetByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("lookup %s: %v", email, err)
	}
	return u.ID
}

// ─── Role change: authorization (spec §18 #10, #12) ─────────────────────────

func TestUpdateUserRole_AdminCannotChangeRoles(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	admin := seedTeamUser(t, svc, merchant.ID, "admin@example.com", model.DashboardUserRoleAdmin)
	viewer := seedTeamUser(t, svc, merchant.ID, "viewer@example.com", model.DashboardUserRoleViewer)

	_, err := svc.UpdateUserRole(context.Background(),
		admin.ID, model.DashboardUserRoleAdmin, merchant.ID,
		viewer.ID, model.DashboardUserRoleAdmin)
	if !errors.Is(err, ErrInsufficientRole) {
		t.Errorf("expected ErrInsufficientRole for ADMIN, got %v", err)
	}
}

func TestUpdateUserRole_ViewerCannotChangeRoles(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	viewer := seedTeamUser(t, svc, merchant.ID, "viewer@example.com", model.DashboardUserRoleViewer)
	admin := seedTeamUser(t, svc, merchant.ID, "admin@example.com", model.DashboardUserRoleAdmin)

	_, err := svc.UpdateUserRole(context.Background(),
		viewer.ID, model.DashboardUserRoleViewer, merchant.ID,
		admin.ID, model.DashboardUserRoleViewer)
	if !errors.Is(err, ErrInsufficientRole) {
		t.Errorf("expected ErrInsufficientRole for VIEWER, got %v", err)
	}
}

// ─── Role change: validation + not found (spec §17) ─────────────────────────

func TestUpdateUserRole_InvalidRoleRejected(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)
	target := seedTeamUser(t, svc, merchant.ID, "viewer@example.com", model.DashboardUserRoleViewer)

	_, err := svc.UpdateUserRole(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		target.ID, model.DashboardUserRole("SUPERADMIN"))
	if !errors.Is(err, ErrInvalidRole) {
		t.Errorf("expected ErrInvalidRole, got %v", err)
	}
}

func TestUpdateUserRole_TargetNotFound(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)

	_, err := svc.UpdateUserRole(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		uuid.New(), model.DashboardUserRoleAdmin)
	if !errors.Is(err, ErrDashboardUserNotFound) {
		t.Errorf("expected ErrDashboardUserNotFound, got %v", err)
	}
}

// ─── Tenant isolation (spec §18 #14–#15, §21) ───────────────────────────────

// TestTenantIsolation_TwoMerchants creates Merchant A (OWNER/ADMIN/VIEWER) and
// Merchant B (OWNER/ADMIN/VIEWER) and verifies neither can mutate the other's
// users through role or status changes.
func TestTenantIsolation_TwoMerchants(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchantA := seedMerchant(merchantRepo)
	merchantB := seedMerchant(merchantRepo)

	ownerA := seedTeamUser(t, svc, merchantA.ID, "owner-a@example.com", model.DashboardUserRoleOwner)
	seedTeamUser(t, svc, merchantA.ID, "admin-a@example.com", model.DashboardUserRoleAdmin)
	seedTeamUser(t, svc, merchantA.ID, "viewer-a@example.com", model.DashboardUserRoleViewer)

	ownerB := seedTeamUser(t, svc, merchantB.ID, "owner-b@example.com", model.DashboardUserRoleOwner)
	adminB := seedTeamUser(t, svc, merchantB.ID, "admin-b@example.com", model.DashboardUserRoleAdmin)
	viewerB := seedTeamUser(t, svc, merchantB.ID, "viewer-b@example.com", model.DashboardUserRoleViewer)

	// A must not modify B — role mutations.
	for _, target := range []uuid.UUID{ownerB.ID, adminB.ID, viewerB.ID} {
		_, err := svc.UpdateUserRole(context.Background(),
			ownerA.ID, model.DashboardUserRoleOwner, merchantA.ID,
			target, model.DashboardUserRoleViewer)
		if !errors.Is(err, ErrCrossmerchantAccess) {
			t.Errorf("role mutation on %s: expected ErrCrossmerchantAccess, got %v", target, err)
		}
	}

	// A must not modify B — status mutations.
	for _, target := range []uuid.UUID{ownerB.ID, adminB.ID, viewerB.ID} {
		_, err := svc.UpdateUserStatus(context.Background(),
			ownerA.ID, model.DashboardUserRoleOwner, merchantA.ID,
			target, model.DashboardUserStatusDisabled)
		if !errors.Is(err, ErrCrossmerchantAccess) {
			t.Errorf("status mutation on %s: expected ErrCrossmerchantAccess, got %v", target, err)
		}
	}

	// B must not modify A — role mutations.
	adminA, err := userRepo.GetByEmail(context.Background(), "admin-a@example.com")
	if err != nil {
		t.Fatalf("lookup admin A: %v", err)
	}
	viewerA, err := userRepo.GetByEmail(context.Background(), "viewer-a@example.com")
	if err != nil {
		t.Fatalf("lookup viewer A: %v", err)
	}
	for _, target := range []uuid.UUID{ownerA.ID, adminA.ID, viewerA.ID} {
		_, err := svc.UpdateUserRole(context.Background(),
			ownerB.ID, model.DashboardUserRoleOwner, merchantB.ID,
			target, model.DashboardUserRoleViewer)
		if !errors.Is(err, ErrCrossmerchantAccess) {
			t.Errorf("reverse role mutation on %s: expected ErrCrossmerchantAccess, got %v", target, err)
		}
		_, err = svc.UpdateUserStatus(context.Background(),
			ownerB.ID, model.DashboardUserRoleOwner, merchantB.ID,
			target, model.DashboardUserStatusDisabled)
		if !errors.Is(err, ErrCrossmerchantAccess) {
			t.Errorf("reverse status mutation on %s: expected ErrCrossmerchantAccess, got %v", target, err)
		}
	}

	// Nothing in B changed.
	for _, u := range []*model.MerchantUser{mustGetUser(t, userRepo, ownerB.ID), mustGetUser(t, userRepo, adminB.ID), mustGetUser(t, userRepo, viewerB.ID)} {
		if u.Status != model.DashboardUserStatusActive {
			t.Errorf("user %s status changed to %v — cross-tenant mutation leaked", u.Email, u.Status)
		}
	}
}

func mustGetUser(t *testing.T, userRepo *mockMerchantUserRepo, id uuid.UUID) *model.MerchantUser {
	t.Helper()
	u, err := userRepo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID(%s): %v", id, err)
	}
	return u
}

// ─── User status: enable/disable (spec §18 #7–#9, §8) ───────────────────────

func TestUpdateUserStatus_FailsClosedWhenSessionRevocationFails(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	sessionRepo := newMockSessionRepo()
	svc := NewDashboardUserService(userRepo, sessionRepo, merchantRepo)
	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)
	target := seedTeamUser(t, svc, merchant.ID, "viewer@example.com", model.DashboardUserRoleViewer)
	sessionRepo.deleteByUserErr = errors.New("session store unavailable")

	_, err := svc.UpdateUserStatus(context.Background(), owner.ID, model.DashboardUserRoleOwner, merchant.ID, target.ID, model.DashboardUserStatusDisabled)
	if err == nil {
		t.Fatal("user disable reported success despite session revocation failure")
	}
}

func TestUpdateUserStatus_OwnerDisablesViewer(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)
	viewer := seedTeamUser(t, svc, merchant.ID, "viewer@example.com", model.DashboardUserRoleViewer)

	updated, err := svc.UpdateUserStatus(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		viewer.ID, model.DashboardUserStatusDisabled)
	if err != nil {
		t.Fatalf("disable viewer: %v", err)
	}
	if updated.Status != model.DashboardUserStatusDisabled {
		t.Errorf("status: got %v, want DISABLED", updated.Status)
	}
}

func TestUpdateUserStatus_ReenableDisabledUser(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	sessionRepo := newMockSessionRepo()
	svc := NewDashboardUserService(userRepo, sessionRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)
	target := seedTeamUser(t, svc, merchant.ID, "admin@example.com", model.DashboardUserRoleAdmin)

	// Disable.
	if _, err := svc.UpdateUserStatus(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		target.ID, model.DashboardUserStatusDisabled); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// Re-enable (DISABLED → ACTIVE).
	updated, err := svc.UpdateUserStatus(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		target.ID, model.DashboardUserStatusActive)
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if updated.Status != model.DashboardUserStatusActive {
		t.Errorf("status: got %v, want ACTIVE", updated.Status)
	}

	// Re-enabling must NOT automatically recreate sessions — the user has to
	// authenticate again to obtain a new session. No password is generated.
	sessions, err := sessionRepo.GetByUserID(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("re-enable must not recreate sessions, got %d", len(sessions))
	}
	stored := mustGetUser(t, userRepo, target.ID)
	if stored.PasswordHash == "" {
		t.Error("password hash must be untouched by re-enable")
	}
}

// TestUpdateUserStatus_CannotDisableFinalOwner exercises the last-OWNER guard
// for the disable path when the caller is not the target (the shape a future
// admin-override path would take). In the current API the caller is always an
// active OWNER of the same merchant, so disabling the *final* OWNER can only
// be a self-disable — which is rejected earlier by ErrSelfDisable; both guards
// together ensure a merchant can never end up with zero ACTIVE OWNERs.
func TestUpdateUserStatus_CannotDisableFinalOwner(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	ownerA := seedTeamUser(t, svc, merchant.ID, "owner-a@example.com", model.DashboardUserRoleOwner)
	adminB := seedTeamUser(t, svc, merchant.ID, "admin-b@example.com", model.DashboardUserRoleAdmin)

	_, err := svc.UpdateUserStatus(context.Background(),
		adminB.ID, model.DashboardUserRoleOwner, merchant.ID, // OWNER authority, not the target
		ownerA.ID, model.DashboardUserStatusDisabled)
	if !errors.Is(err, ErrLastOwnerRequired) {
		t.Errorf("expected ErrLastOwnerRequired, got %v", err)
	}

	if mustGetUser(t, userRepo, ownerA.ID).Status != model.DashboardUserStatusActive {
		t.Error("final OWNER must remain ACTIVE after rejected disable")
	}
}

func TestUpdateUserStatus_DisableSecondOwnerAllowed(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	ownerA := seedTeamUser(t, svc, merchant.ID, "owner-a@example.com", model.DashboardUserRoleOwner)
	ownerB := seedTeamUser(t, svc, merchant.ID, "owner-b@example.com", model.DashboardUserRoleOwner)

	// OWNER A disables OWNER B → allowed because OWNER A remains active.
	if _, err := svc.UpdateUserStatus(context.Background(),
		ownerA.ID, model.DashboardUserRoleOwner, merchant.ID,
		ownerB.ID, model.DashboardUserStatusDisabled); err != nil {
		t.Fatalf("disable second OWNER: %v", err)
	}
}

// ─── Session tests (spec §19) ────────────────────────────────────────────────

func TestSession_DisableDeletesAllUserSessions(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	sessionRepo := newMockSessionRepo()
	svc := NewDashboardUserService(userRepo, sessionRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)
	target := seedTeamUser(t, svc, merchant.ID, "admin@example.com", model.DashboardUserRoleAdmin)

	seedSessionFor(t, sessionRepo, target.ID, "hash-session-1")
	seedSessionFor(t, sessionRepo, target.ID, "hash-session-2")
	// An unrelated session that must survive.
	other := seedTeamUser(t, svc, merchant.ID, "viewer@example.com", model.DashboardUserRoleViewer)
	seedSessionFor(t, sessionRepo, other.ID, "hash-session-other")

	if _, err := svc.UpdateUserStatus(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		target.ID, model.DashboardUserStatusDisabled); err != nil {
		t.Fatalf("disable: %v", err)
	}

	sessions, err := sessionRepo.GetByUserID(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected ALL sessions of the disabled user revoked, got %d", len(sessions))
	}

	// The old refresh token can no longer be found → cannot be refreshed.
	if _, err := sessionRepo.GetByRefreshTokenHash(context.Background(), "hash-session-1"); !errors.Is(err, repository.ErrSessionNotFound) {
		t.Errorf("old refresh session must be gone, got %v", err)
	}

	// Unrelated user's session must NOT be deleted.
	otherSessions, err := sessionRepo.GetByUserID(context.Background(), other.ID)
	if err != nil {
		t.Fatalf("GetByUserID other: %v", err)
	}
	if len(otherSessions) != 1 {
		t.Errorf("unrelated sessions must survive, got %d", len(otherSessions))
	}
}

func TestSession_RoleChangeDoesNotRevokeSessions(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	sessionRepo := newMockSessionRepo()
	svc := NewDashboardUserService(userRepo, sessionRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, svc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)
	target := seedTeamUser(t, svc, merchant.ID, "admin@example.com", model.DashboardUserRoleAdmin)
	seedSessionFor(t, sessionRepo, target.ID, "hash-role-session")

	if _, err := svc.UpdateUserRole(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		target.ID, model.DashboardUserRoleViewer); err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}

	// Sessions survive the role change…
	sessions, err := sessionRepo.GetByUserID(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("role change must not revoke sessions, got %d", len(sessions))
	}

	// …and the very next repository read returns the NEW role — this is what
	// RequireDashboardAuth relies on for fresh per-request authorization, so
	// no stale role can persist after a change.
	reloaded, err := userRepo.GetByID(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if reloaded.Role != model.DashboardUserRoleViewer {
		t.Errorf("reloaded role: got %v, want VIEWER (role must be fresh from DB)", reloaded.Role)
	}
}

func TestSession_DisabledUserCannotRefreshOldSession(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	sessionRepo := newMockSessionRepo()
	merchantRepo := newMockMerchantRepo()
	authSvc := newTestAuthService(userRepo, sessionRepo)
	dashSvc := NewDashboardUserService(userRepo, sessionRepo, merchantRepo)

	merchant := seedMerchant(merchantRepo)
	owner := seedTeamUser(t, dashSvc, merchant.ID, "owner@example.com", model.DashboardUserRoleOwner)

	// Log in — creates a real refresh session + access token.
	loginResp, plainRefresh, err := authSvc.Login(context.Background(), model.LoginRequest{
		Email: "owner@example.com", Password: "password123",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// The session belongs to the owner, who cannot self-disable — so disable
	// via a second account instead: create an OWNER B and disable them.
	ownerB := seedTeamUser(t, dashSvc, merchant.ID, "owner-b@example.com", model.DashboardUserRoleOwner)
	_, plainRefreshB, err := authSvc.Login(context.Background(), model.LoginRequest{
		Email: "owner-b@example.com", Password: "password123",
	})
	if err != nil {
		t.Fatalf("login B: %v", err)
	}
	if _, err := dashSvc.UpdateUserStatus(context.Background(),
		owner.ID, model.DashboardUserRoleOwner, merchant.ID,
		ownerB.ID, model.DashboardUserStatusDisabled); err != nil {
		t.Fatalf("disable owner B: %v", err)
	}

	// Old session of the disabled user is gone → refresh rejected.
	_, _, err = authSvc.RefreshAccessToken(context.Background(), plainRefreshB)
	if !errors.Is(err, ErrUserDisabled) && !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("refresh with old session after disable: expected rejection, got %v", err)
	}

	// The still-active user's refresh continues to work.
	if _, _, err := authSvc.RefreshAccessToken(context.Background(), plainRefresh); err != nil {
		t.Errorf("active user refresh must still work: %v", err)
	}

	// Access token of the disabled user is rejected by middleware even before
	// session lookup — verified at handler level in
	// TestAuthHandler_Me_DisabledUser and
	// TestDashboardUserHandler_ChangePassword_DisabledUserBlockedByMiddleware.
	if loginResp == nil {
		t.Fatal("expected login response")
	}
}

// ─── Merchant lifecycle × dashboard sessions (spec §19 #5–#6) ───────────────

func TestMerchantLifecycle_SuspensionRevokesDashboardSessions(t *testing.T) {
	merchantRepo := newMockMerchantRepo()
	sessionRepo := newMockSessionRepo()
	merchantSvc := NewMerchantServiceWithSessions(merchantRepo, sessionRepo)

	for _, target := range []model.MerchantStatus{
		model.MerchantStatusSuspended,
		model.MerchantStatusInactive,
	} {
		merchant := seedMerchant(merchantRepo)

		if _, err := merchantSvc.UpdateMerchantStatus(context.Background(), merchant.ID, target); err != nil {
			t.Fatalf("UpdateMerchantStatus(%s): %v", target, err)
		}

		found := false
		for _, id := range sessionRepo.merchantRevokes {
			if id == merchant.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("merchant %s → %s must revoke all dashboard sessions", merchant.ID, target)
		}
	}
}

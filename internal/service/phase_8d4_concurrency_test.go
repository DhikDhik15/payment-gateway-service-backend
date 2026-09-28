package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
)

// TestPhase8D4ServiceOwnerMutationsSingleWinner exercises the service path
// with concurrent callers. The PostgreSQL repository test proves the database
// lock/transaction behavior; this test proves that the stable business error is
// propagated without exposing repository details.
func TestPhase8D4ServiceOwnerMutationsSingleWinner(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)
	merchant := seedMerchant(merchantRepo)
	ownerA := seedTeamUser(t, svc, merchant.ID, "phase8d4-owner-a@example.com", model.DashboardUserRoleOwner)
	ownerB := seedTeamUser(t, svc, merchant.ID, "phase8d4-owner-b@example.com", model.DashboardUserRoleOwner)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := svc.UpdateUserRole(context.Background(), ownerA.ID, model.DashboardUserRoleOwner, merchant.ID, ownerA.ID, model.DashboardUserRoleAdmin)
		results <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		_, err := svc.UpdateUserRole(context.Background(), ownerA.ID, model.DashboardUserRoleOwner, merchant.ID, ownerB.ID, model.DashboardUserRoleAdmin)
		results <- err
	}()
	close(start)
	wg.Wait()
	close(results)

	var successes, lastOwnerErrors int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrLastOwnerRequired):
			lastOwnerErrors++
		default:
			t.Fatalf("unexpected owner mutation error: %v", err)
		}
	}
	if successes != 1 || lastOwnerErrors != 1 {
		t.Fatalf("successes=%d last-owner-errors=%d, want one each", successes, lastOwnerErrors)
	}

	owners, err := userRepo.CountActiveOwners(context.Background(), merchant.ID)
	if err != nil {
		t.Fatalf("count active owners: %v", err)
	}
	if owners != 1 {
		t.Fatalf("active owners after concurrent demotions = %d, want 1", owners)
	}
}

// TestPhase8D4ServiceCrossTenantOwnerMutation verifies the service preserves
// the existing non-leaking tenant error while using the transactional path.
func TestPhase8D4ServiceCrossTenantOwnerMutation(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	svc := newTestDashboardUserService(userRepo, merchantRepo)
	merchantA := seedMerchant(merchantRepo)
	merchantB := seedMerchant(merchantRepo)
	ownerA := seedTeamUser(t, svc, merchantA.ID, "phase8d4-tenant-a@example.com", model.DashboardUserRoleOwner)
	ownerB := seedTeamUser(t, svc, merchantB.ID, "phase8d4-tenant-b@example.com", model.DashboardUserRoleOwner)

	_, err := svc.UpdateUserRole(context.Background(), ownerA.ID, model.DashboardUserRoleOwner, merchantA.ID, ownerB.ID, model.DashboardUserRoleAdmin)
	if !errors.Is(err, ErrCrossmerchantAccess) {
		t.Fatalf("cross-tenant mutation error = %v, want ErrCrossmerchantAccess", err)
	}
	unchanged, err := userRepo.GetByID(context.Background(), ownerB.ID)
	if err != nil {
		t.Fatalf("load cross-tenant target: %v", err)
	}
	if unchanged.Role != model.DashboardUserRoleOwner || unchanged.MerchantID != merchantB.ID {
		t.Fatalf("cross-tenant target changed: role=%s merchant=%s", unchanged.Role, unchanged.MerchantID)
	}
}

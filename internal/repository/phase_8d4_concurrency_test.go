package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests intentionally use real PostgreSQL transactions. They are opt-in
// so the normal unit-test suite does not require a database; CI/staging can set
// TEST_DATABASE_URL to a disposable database with migrations through 000018
// applied. Every fixture uses UUIDs and removes its merchant (and cascaded
// users/sessions) during cleanup.
func phase8D4Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL concurrency test")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 32
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping test database: %v", err)
	}
	var migrationVersion int
	var migrationDirty bool
	if err := pool.QueryRow(context.Background(), `SELECT version, dirty FROM schema_migrations`).Scan(&migrationVersion, &migrationDirty); err != nil {
		pool.Close()
		t.Fatalf("read migration state: %v", err)
	}
	if migrationDirty || migrationVersion < 18 {
		pool.Close()
		t.Fatalf("test database migration state version=%d dirty=%t, want version>=18 and clean", migrationVersion, migrationDirty)
	}
	t.Cleanup(pool.Close)
	return pool
}

func phase8D4Hash(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func seedPhase8D4MerchantAndOwners(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	merchantID := uuid.New()
	ownerA := uuid.New()
	ownerB := uuid.New()
	now := time.Now().UTC()

	_, err := pool.Exec(ctx, `
		INSERT INTO merchants (id, name, code, api_key, api_secret, status, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, NULL, 'ACTIVE', $4, $4)
	`, merchantID, "Phase 8D.4 Test Merchant", "phase8d4-"+merchantID.String(), now)
	if err != nil {
		t.Fatalf("insert test merchant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM merchants WHERE id = $1`, merchantID)
	})

	for i, userID := range []uuid.UUID{ownerA, ownerB} {
		_, err = pool.Exec(ctx, `
			INSERT INTO merchant_users
			    (id, merchant_id, email, password_hash, role, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'OWNER', 'ACTIVE', $5, $5)
		`, userID, merchantID, fmt.Sprintf("phase8d4-%d-%s@example.invalid", i, userID), "test-password-hash", now)
		if err != nil {
			t.Fatalf("insert test owner: %v", err)
		}
	}
	return merchantID, ownerA, ownerB
}

func phase8D4Session(userID uuid.UUID, seed string) *model.DashboardSession {
	now := time.Now().UTC()
	return &model.DashboardSession{
		ID:               uuid.New(),
		MerchantUserID:   userID,
		RefreshTokenHash: phase8D4Hash(seed),
		ExpiresAt:        now.Add(time.Hour),
		CreatedAt:        now,
	}
}

func TestPhase8D4OwnerMutationsPreserveOneActiveOwner(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewMerchantUserRepository(pool)
	ownerRepo, ok := repo.(OwnerInvariantRepository)
	if !ok {
		t.Fatal("PostgreSQL merchant-user repository does not implement OwnerInvariantRepository")
	}

	cases := []struct {
		name string
		run  func(context.Context, OwnerInvariantRepository, uuid.UUID, uuid.UUID, uuid.UUID) error
	}{
		{
			name: "disable plus demote",
			run: func(ctx context.Context, ownerRepo OwnerInvariantRepository, merchantID, a, b uuid.UUID) error {
				return runTwoOwnerMutations(ctx,
					func() error {
						_, err := ownerRepo.UpdateStatusWithOwnerLock(ctx, merchantID, a, model.DashboardUserStatusDisabled)
						return err
					},
					func() error {
						_, err := ownerRepo.UpdateRoleWithOwnerLock(ctx, merchantID, b, model.DashboardUserRoleAdmin)
						return err
					},
				)
			},
		},
		{
			name: "two demotions",
			run: func(ctx context.Context, ownerRepo OwnerInvariantRepository, merchantID, a, b uuid.UUID) error {
				return runTwoOwnerMutations(ctx,
					func() error {
						_, err := ownerRepo.UpdateRoleWithOwnerLock(ctx, merchantID, a, model.DashboardUserRoleAdmin)
						return err
					},
					func() error {
						_, err := ownerRepo.UpdateRoleWithOwnerLock(ctx, merchantID, b, model.DashboardUserRoleAdmin)
						return err
					},
				)
			},
		},
		{
			name: "self demotion plus other disable",
			run: func(ctx context.Context, ownerRepo OwnerInvariantRepository, merchantID, a, b uuid.UUID) error {
				return runTwoOwnerMutations(ctx,
					func() error {
						_, err := ownerRepo.UpdateRoleWithOwnerLock(ctx, merchantID, a, model.DashboardUserRoleAdmin)
						return err
					},
					func() error {
						_, err := ownerRepo.UpdateStatusWithOwnerLock(ctx, merchantID, b, model.DashboardUserStatusDisabled)
						return err
					},
				)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merchantID, ownerA, ownerB := seedPhase8D4MerchantAndOwners(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := tc.run(ctx, ownerRepo, merchantID, ownerA, ownerB); err != nil {
				t.Fatalf("owner race result: %v", err)
			}

			var activeOwners int
			if err := pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM merchant_users
				WHERE merchant_id = $1 AND role = 'OWNER' AND status = 'ACTIVE'
			`, merchantID).Scan(&activeOwners); err != nil {
				t.Fatalf("count final active owners: %v", err)
			}
			if activeOwners != 1 {
				t.Fatalf("final active OWNER count = %d, want exactly 1", activeOwners)
			}
		})
	}
}

func TestPhase8D6MerchantStatusTransitionsSerialize(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewMerchantRepository(pool)
	merchantID, _, _ := seedPhase8D4MerchantAndOwners(t, pool)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, status := range []model.MerchantStatus{model.MerchantStatusSuspended, model.MerchantStatusInactive} {
		status := status
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- repo.UpdateStatus(context.Background(), merchantID, status)
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var successes, invalid int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrMerchantStatusTransitionInvalid):
			invalid++
		default:
			t.Fatalf("unexpected concurrent status error: %v", err)
		}
	}
	if successes != 1 || invalid != 1 {
		t.Fatalf("concurrent status successes=%d invalid=%d, want one each", successes, invalid)
	}
	var finalStatus model.MerchantStatus
	if err := pool.QueryRow(context.Background(), `SELECT status FROM merchants WHERE id = $1`, merchantID).Scan(&finalStatus); err != nil {
		t.Fatalf("read final merchant status: %v", err)
	}
	if finalStatus != model.MerchantStatusSuspended && finalStatus != model.MerchantStatusInactive {
		t.Fatalf("final status = %s, want SUSPENDED or INACTIVE", finalStatus)
	}
}

func TestPhase8D6MerchantStatusRevokesSessionsInTransaction(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewMerchantRepository(pool)
	merchantID, ownerA, _ := seedPhase8D4MerchantAndOwners(t, pool)
	sessionRepo := NewDashboardSessionRepository(pool)
	session := phase8D4Session(ownerA, "phase8d6-merchant-session")
	if err := sessionRepo.Create(context.Background(), session); err != nil {
		t.Fatalf("create merchant session: %v", err)
	}
	if err := repo.UpdateStatus(context.Background(), merchantID, model.MerchantStatusSuspended); err != nil {
		t.Fatalf("suspend merchant: %v", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM dashboard_sessions WHERE merchant_user_id = $1
	`, ownerA).Scan(&count); err != nil {
		t.Fatalf("count revoked merchant sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("sessions after merchant suspension = %d, want 0", count)
	}
}

func TestPhase8D6UserDisableRevokesSessionsInTransaction(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewMerchantUserRepository(pool)
	ownerRepo := repo.(OwnerInvariantRepository)
	merchantID, _, ownerB := seedPhase8D4MerchantAndOwners(t, pool)
	sessionRepo := NewDashboardSessionRepository(pool)
	if err := sessionRepo.Create(context.Background(), phase8D4Session(ownerB, "phase8d6-user-session")); err != nil {
		t.Fatalf("create user session: %v", err)
	}
	if _, err := ownerRepo.UpdateStatusWithOwnerLock(context.Background(), merchantID, ownerB, model.DashboardUserStatusDisabled); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM dashboard_sessions WHERE merchant_user_id = $1
	`, ownerB).Scan(&count); err != nil {
		t.Fatalf("count revoked user sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("sessions after user disable = %d, want 0", count)
	}
}

func TestPhase8D6PasswordChangeRevokesSessionsInTransaction(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewMerchantUserRepository(pool).(TransactionalPasswordUpdateRepository)
	_, ownerA, _ := seedPhase8D4MerchantAndOwners(t, pool)
	sessionRepo := NewDashboardSessionRepository(pool)
	if err := sessionRepo.Create(context.Background(), phase8D4Session(ownerA, "phase8d6-password-session")); err != nil {
		t.Fatalf("create password-change session: %v", err)
	}
	newHash := "$argon2id$v=19$m=1,t=1,p=1$aaaaaaaaaaaaaaaa$aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := repo.UpdatePasswordHashAndRevokeSessions(context.Background(), ownerA, newHash); err != nil {
		t.Fatalf("transactional password update: %v", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM dashboard_sessions WHERE merchant_user_id = $1
	`, ownerA).Scan(&count); err != nil {
		t.Fatalf("count revoked password-change sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("sessions after password change = %d, want 0", count)
	}
}

func TestPhase8D4OwnerMutationRejectsInactiveMerchant(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewMerchantUserRepository(pool)
	ownerRepo := repo.(OwnerInvariantRepository)
	merchantID, ownerA, _ := seedPhase8D4MerchantAndOwners(t, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE merchants SET status = 'SUSPENDED' WHERE id = $1`, merchantID); err != nil {
		t.Fatalf("suspend test merchant: %v", err)
	}
	if _, err := ownerRepo.UpdateRoleWithOwnerLock(context.Background(), merchantID, ownerA, model.DashboardUserRoleAdmin); !errors.Is(err, ErrMerchantInactive) {
		t.Fatalf("inactive-merchant owner mutation error category = %T, want ErrMerchantInactive", err)
	}
}

func TestPhase8D4OwnerMutationRejectsCrossTenantTarget(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewMerchantUserRepository(pool)
	ownerRepo := repo.(OwnerInvariantRepository)
	merchantA, _, _ := seedPhase8D4MerchantAndOwners(t, pool)
	_, ownerB, _ := seedPhase8D4MerchantAndOwners(t, pool)

	if _, err := ownerRepo.UpdateRoleWithOwnerLock(context.Background(), merchantA, ownerB, model.DashboardUserRoleAdmin); !errors.Is(err, ErrMerchantUserCrossTenant) {
		t.Fatalf("cross-tenant owner mutation error category = %T, want ErrMerchantUserCrossTenant", err)
	}
	unchanged, err := repo.GetByID(context.Background(), ownerB)
	if err != nil {
		t.Fatalf("load cross-tenant target: %v", err)
	}
	if unchanged.Role != model.DashboardUserRoleOwner || unchanged.MerchantID == merchantA {
		t.Fatalf("cross-tenant target changed unexpectedly")
	}
}

func runTwoOwnerMutations(
	ctx context.Context,
	first func() error,
	second func() error,
) error {
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		results <- first()
	}()
	go func() {
		defer wg.Done()
		<-start
		results <- second()
	}()
	close(start)
	wg.Wait()
	close(results)

	var successes, lastOwnerErrors int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrLastActiveOwnerRequired):
			lastOwnerErrors++
		default:
			return fmt.Errorf("unexpected mutation error: %w", err)
		}
	}
	if successes != 1 || lastOwnerErrors != 1 {
		return fmt.Errorf("successes=%d last-owner-errors=%d, want one each", successes, lastOwnerErrors)
	}
	return nil
}

func TestPhase8D4RefreshRotationSerializesWithLogout(t *testing.T) {
	pool := phase8D4Pool(t)
	_, ownerA, _ := seedPhase8D4MerchantAndOwners(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sessionRepo := NewDashboardSessionRepository(pool)
	rotator := sessionRepo.(AtomicRefreshSessionRepository)

	for i := 0; i < 20; i++ {
		old := phase8D4Session(ownerA, fmt.Sprintf("logout-race-%d-%s", i, uuid.New()))
		if err := sessionRepo.Create(ctx, old); err != nil {
			t.Fatalf("seed logout-race session: %v", err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _ = rotator.RotateByRefreshTokenHash(ctx, old.RefreshTokenHash, phase8D4Session(ownerA, "logout-replacement-"+uuid.New().String()))
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = sessionRepo.Delete(ctx, old.ID)
		}()
		close(start)
		wg.Wait()

		var remaining int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE id = $1`, old.ID).Scan(&remaining); err != nil {
			t.Fatalf("count logout-race row: %v", err)
		}
		if remaining != 0 {
			t.Fatalf("logout race left %d session rows, want 0", remaining)
		}
	}
}

func TestPhase8D4LogoutBySessionIDIsUserScoped(t *testing.T) {
	pool := phase8D4Pool(t)
	_, ownerA, _ := seedPhase8D4MerchantAndOwners(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	otherMerchantID := uuid.New()
	otherUserID := uuid.New()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchants (id, name, code, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'ACTIVE', $4, $4)
	`, otherMerchantID, "Phase 8D.4 Other Merchant", "p11-other-"+otherMerchantID.String(), now); err != nil {
		t.Fatalf("insert other merchant: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM merchants WHERE id = $1`, otherMerchantID) })
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchant_users
		    (id, merchant_id, email, password_hash, role, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'test-password-hash', 'OWNER', 'ACTIVE', $4, $4)
	`, otherUserID, otherMerchantID, "phase8d4-other-"+otherUserID.String()+"@example.invalid", now); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	repo := NewDashboardSessionRepository(pool)
	session := phase8D4Session(ownerA, "scoped-logout-"+uuid.NewString())
	if err := repo.Create(ctx, session); err != nil {
		t.Fatalf("seed scoped logout session: %v", err)
	}

	// A signed session claim for a different tenant/user must not delete the row.
	if err := repo.DeleteByIDAndUser(ctx, session.ID, otherUserID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("cross-tenant delete error = %v, want ErrSessionNotFound", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE id = $1`, session.ID).Scan(&count); err != nil {
		t.Fatalf("count session after wrong-user delete: %v", err)
	}
	if count != 1 {
		t.Fatalf("wrong-user delete changed session count: got %d, want 1", count)
	}

	if err := repo.DeleteByIDAndUser(ctx, session.ID, ownerA); err != nil {
		t.Fatalf("correct-user delete: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE id = $1`, session.ID).Scan(&count); err != nil {
		t.Fatalf("count session after logout: %v", err)
	}
	if count != 0 {
		t.Fatalf("session count after logout: got %d, want 0", count)
	}
	// Idempotent repeat.
	if err := repo.DeleteByIDAndUser(ctx, session.ID, ownerA); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("repeat logout error = %v, want ErrSessionNotFound", err)
	}
}

func TestPhase8D4RefreshRotationSingleWinnerAndRollback(t *testing.T) {
	pool := phase8D4Pool(t)
	merchantID, ownerA, _ := seedPhase8D4MerchantAndOwners(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sessionRepo := NewDashboardSessionRepository(pool)
	rotator, ok := sessionRepo.(AtomicRefreshSessionRepository)
	if !ok {
		t.Fatal("PostgreSQL session repository does not implement AtomicRefreshSessionRepository")
	}

	oldSession := phase8D4Session(ownerA, "old-refresh-"+uuid.New().String())
	if err := sessionRepo.Create(ctx, oldSession); err != nil {
		t.Fatalf("seed old refresh session: %v", err)
	}

	const requests = 20
	start := make(chan struct{})
	results := make(chan struct {
		hash string
		err  error
	}, requests)
	var wg sync.WaitGroup
	wg.Add(requests)
	for i := 0; i < requests; i++ {
		go func(i int) {
			defer wg.Done()
			replacement := phase8D4Session(ownerA, fmt.Sprintf("replacement-%d-%s", i, uuid.New()))
			<-start
			_, err := rotator.RotateByRefreshTokenHash(ctx, oldSession.RefreshTokenHash, replacement)
			results <- struct {
				hash string
				err  error
			}{hash: replacement.RefreshTokenHash, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	var winners []string
	for result := range results {
		switch {
		case result.err == nil:
			winners = append(winners, result.hash)
		case errors.Is(result.err, ErrSessionNotFound):
			// Expected concurrent reuse loser.
		default:
			t.Fatalf("unexpected refresh rotation error category: %T", result.err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("successful rotations = %d, want exactly 1", len(winners))
	}

	var oldCount, winnerCount, totalCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE refresh_token_hash = $1`, oldSession.RefreshTokenHash).Scan(&oldCount); err != nil {
		t.Fatalf("count old session: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE refresh_token_hash = $1`, winners[0]).Scan(&winnerCount); err != nil {
		t.Fatalf("count winning session: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE merchant_user_id = $1`, ownerA).Scan(&totalCount); err != nil {
		t.Fatalf("count replacement sessions: %v", err)
	}
	if oldCount != 0 || winnerCount != 1 || totalCount != 1 {
		t.Fatalf("session state old=%d winner=%d total=%d, want 0/1/1", oldCount, winnerCount, totalCount)
	}

	// The winning replacement is itself single-use.
	next := phase8D4Session(ownerA, "next-"+uuid.New().String())
	if _, err := rotator.RotateByRefreshTokenHash(ctx, winners[0], next); err != nil {
		t.Fatalf("winning replacement was not usable once: %v", err)
	}
	replay := phase8D4Session(ownerA, "replay-"+uuid.New().String())
	if _, err := rotator.RotateByRefreshTokenHash(ctx, winners[0], replay); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("replay error category = %T, want ErrSessionNotFound", err)
	}

	// A replacement update failure must roll the old token back. Seed an
	// occupied hash and a separate old token, then attempt to rotate into the
	// occupied unique hash.
	occupied := phase8D4Session(ownerA, "occupied-"+uuid.New().String())
	oldForRollback := phase8D4Session(ownerA, "rollback-old-"+uuid.New().String())
	if err := sessionRepo.Create(ctx, occupied); err != nil {
		t.Fatalf("seed occupied session: %v", err)
	}
	if err := sessionRepo.Create(ctx, oldForRollback); err != nil {
		t.Fatalf("seed rollback session: %v", err)
	}
	badReplacement := phase8D4Session(ownerA, "bad-replacement-"+uuid.New().String())
	badReplacement.RefreshTokenHash = occupied.RefreshTokenHash
	if _, err := rotator.RotateByRefreshTokenHash(ctx, oldForRollback.RefreshTokenHash, badReplacement); err == nil {
		t.Fatal("expected replacement insert failure")
	}
	var rollbackOldCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE refresh_token_hash = $1`, oldForRollback.RefreshTokenHash).Scan(&rollbackOldCount); err != nil {
		t.Fatalf("count rolled-back session: %v", err)
	}
	if rollbackOldCount != 1 {
		t.Fatalf("old session count after failed rotation = %d, want 1", rollbackOldCount)
	}

	// Lifecycle checks happen inside the same transaction. Disable the user
	// and verify a pending token is neither consumed nor replaced.
	disabledSession := phase8D4Session(ownerA, "disabled-"+uuid.New().String())
	if err := sessionRepo.Create(ctx, disabledSession); err != nil {
		t.Fatalf("seed disabled-user session: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE merchant_users SET status = 'DISABLED' WHERE id = $1`, ownerA); err != nil {
		t.Fatalf("disable test user: %v", err)
	}
	if _, err := rotator.RotateByRefreshTokenHash(ctx, disabledSession.RefreshTokenHash, phase8D4Session(ownerA, "disabled-replacement-"+uuid.New().String())); !errors.Is(err, ErrSessionUserDisabled) {
		t.Fatalf("disabled-user rotation error category = %T, want ErrSessionUserDisabled", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE refresh_token_hash = $1`, disabledSession.RefreshTokenHash).Scan(&oldCount); err != nil {
		t.Fatalf("count disabled-user session after rejection: %v", err)
	}
	if oldCount != 1 {
		t.Fatalf("disabled-user session count after rejection = %d, want 1", oldCount)
	}

	if _, err := pool.Exec(ctx, `UPDATE merchant_users SET status = 'ACTIVE' WHERE id = $1`, ownerA); err != nil {
		t.Fatalf("re-enable test user: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE merchants SET status = 'SUSPENDED' WHERE id = $1`, merchantID); err != nil {
		t.Fatalf("suspend test merchant: %v", err)
	}
	suspendedSession := phase8D4Session(ownerA, "suspended-"+uuid.New().String())
	if err := sessionRepo.Create(ctx, suspendedSession); err != nil {
		t.Fatalf("seed suspended-merchant session: %v", err)
	}
	if _, err := rotator.RotateByRefreshTokenHash(ctx, suspendedSession.RefreshTokenHash, phase8D4Session(ownerA, "suspended-replacement-"+uuid.New().String())); !errors.Is(err, ErrSessionMerchantInactive) {
		t.Fatalf("suspended-merchant rotation error category = %T, want ErrSessionMerchantInactive", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE refresh_token_hash = $1`, suspendedSession.RefreshTokenHash).Scan(&oldCount); err != nil {
		t.Fatalf("count suspended-merchant session after rejection: %v", err)
	}
	if oldCount != 1 {
		t.Fatalf("suspended-merchant session count after rejection = %d, want 1", oldCount)
	}
}

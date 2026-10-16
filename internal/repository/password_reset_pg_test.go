package repository_test

// PG-backed proof of password-reset single-use, session revocation, and
// plaintext discipline (Phase 8E).
//
// Requires TEST_DATABASE_URL pointing at a disposable database migrated
// through 000025. Skips otherwise, like the other *_pg_test.go suites.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func resetTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func resetSeedUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (merchantID, userID uuid.UUID) {
	t.Helper()
	merchantID = uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchants (id, name, code, api_key, api_secret, status)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE')`,
		merchantID, "Reset Merchant", "rst-"+uuid.NewString()[:8], uuid.NewString(), uuid.NewString(),
	); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	userID = uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchant_users (id, merchant_id, email, password_hash, role, status)
		VALUES ($1, $2, $3, $4, 'OWNER', 'ACTIVE')`,
		userID, merchantID, "reset-"+uuid.NewString()[:8]+"@example.com", "old-hash",
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return merchantID, userID
}

func resetTokenRow(userID uuid.UUID, hash string, expiresAt time.Time) *model.PasswordResetToken {
	return &model.PasswordResetToken{ID: uuid.New(), UserID: userID, TokenHash: hash, ExpiresAt: expiresAt}
}

func resetOutboxEntry(merchantID, tokenID uuid.UUID, now time.Time) *model.EmailOutbox {
	return &model.EmailOutbox{
		ID: uuid.New(), MerchantID: merchantID, ReferenceID: &tokenID,
		Type: model.EmailOutboxTypePasswordReset, Recipient: "u@example.com",
		Subject: "s", TextBody: "t", HTMLBody: "h",
		Status: model.EmailOutboxStatusPending, NextAttemptAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
}

func randomTokenHash(t *testing.T) (token, hash string) {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	token = hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:])
}

func TestPasswordResetPG_FullFlowRevokesSessions(t *testing.T) {
	pool := resetTestPool(t)
	ctx := context.Background()
	merchantID, userID := resetSeedUser(t, ctx, pool)

	// A live refresh session that must die with the reset.
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_sessions (id, merchant_user_id, refresh_token_hash, expires_at)
		VALUES ($1, $2, $3, $4)`,
		uuid.New(), userID, uuid.NewString(), time.Now().Add(time.Hour),
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	repo := repository.NewPasswordResetRepository(pool, repository.NewEmailOutboxRepository(pool))
	_, hash := randomTokenHash(t)
	now := time.Now().UTC()
	if err := repo.CreateWithEmailOutbox(ctx,
		resetTokenRow(userID, hash, now.Add(time.Hour)),
		resetOutboxEntry(merchantID, uuid.New(), now),
	); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Plaintext must never be persisted.
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM password_reset_tokens WHERE token_hash = $1`, hash).Scan(&n); err != nil || n != 1 {
		t.Fatalf("token row missing: %v", err)
	}

	got, err := repo.ConsumeWithPasswordUpdate(ctx, hash, "new-hash")
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got != userID {
		t.Fatalf("consume user = %v, want %v", got, userID)
	}

	var pw string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM merchant_users WHERE id = $1`, userID).Scan(&pw); err != nil || pw != "new-hash" {
		t.Fatalf("password not updated: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_sessions WHERE merchant_user_id = $1`, userID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("sessions not revoked (n=%d): %v", n, err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM password_reset_tokens WHERE token_hash = $1`, hash).Scan(&status); err != nil || status != "CONSUMED" {
		t.Fatalf("token not consumed: %v", err)
	}

	// Reuse is rejected.
	if _, err := repo.ConsumeWithPasswordUpdate(ctx, hash, "another"); !errors.Is(err, repository.ErrPasswordResetConsumed) {
		t.Fatalf("reuse must fail consumed, got %v", err)
	}
}

func TestPasswordResetPG_ExpiredAndUnknown(t *testing.T) {
	pool := resetTestPool(t)
	ctx := context.Background()
	merchantID, userID := resetSeedUser(t, ctx, pool)
	repo := repository.NewPasswordResetRepository(pool, repository.NewEmailOutboxRepository(pool))
	now := time.Now().UTC()

	_, hash := randomTokenHash(t)
	if err := repo.CreateWithEmailOutbox(ctx,
		resetTokenRow(userID, hash, now.Add(-time.Minute)),
		resetOutboxEntry(merchantID, uuid.New(), now),
	); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if _, err := repo.ConsumeWithPasswordUpdate(ctx, hash, "x"); !errors.Is(err, repository.ErrPasswordResetExpired) {
		t.Fatalf("expired must fail expired, got %v", err)
	}
	_, unknown := randomTokenHash(t)
	if _, err := repo.ConsumeWithPasswordUpdate(ctx, unknown, "x"); !errors.Is(err, repository.ErrPasswordResetNotFound) {
		t.Fatalf("unknown must fail not-found, got %v", err)
	}
}

func TestPasswordResetPG_ConcurrentConsumeSingleWinner(t *testing.T) {
	pool := resetTestPool(t)
	ctx := context.Background()
	merchantID, userID := resetSeedUser(t, ctx, pool)
	repo := repository.NewPasswordResetRepository(pool, repository.NewEmailOutboxRepository(pool))
	now := time.Now().UTC()
	_, hash := randomTokenHash(t)
	if err := repo.CreateWithEmailOutbox(ctx,
		resetTokenRow(userID, hash, now.Add(time.Hour)),
		resetOutboxEntry(merchantID, uuid.New(), now),
	); err != nil {
		t.Fatalf("create: %v", err)
	}

	const racers = 8
	var wg sync.WaitGroup
	wins := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.ConsumeWithPasswordUpdate(ctx, hash, "hash-winner")
			wins <- err
		}()
	}
	wg.Wait()
	close(wins)
	succeeded := 0
	for err := range wins {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, repository.ErrPasswordResetConsumed) {
			t.Fatalf("loser must fail consumed, got %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("exactly one consume must win, got %d", succeeded)
	}
}

package repository_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Phase 9A database contract for migration 000022_order_payment_link:
//
//	transactions.order_id            nullable, backward compatible
//	fk_transactions_order            composite (merchant_id, order_id) → orders
//	idx_transactions_merchant_order  merchant_id + order_id
//	uq_transactions_order_active     at most one CREATED/PENDING payment
//	uq_transactions_order_paid       at most one PAID payment
//
// These tests require TEST_DATABASE_URL pointing at a database migrated
// through 000022. They never commit schema changes.

var orderPaymentIndexes = []string{
	"idx_transactions_merchant_order",
	"uq_transactions_order_active",
	"uq_transactions_order_paid",
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func readMigrationFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "migrations", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	if strings.TrimSpace(string(body)) == "" {
		t.Fatalf("migration %s is empty", name)
	}
	return string(body)
}

func hasOrderIDColumn(t *testing.T, ctx context.Context, q rowQuerier) bool {
	t.Helper()
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'transactions' AND column_name = 'order_id'
		)`).Scan(&exists); err != nil {
		t.Fatalf("inspect transactions.order_id: %v", err)
	}
	return exists
}

func hasConstraint(t *testing.T, ctx context.Context, q rowQuerier, name string) (string, bool) {
	t.Helper()
	var def string
	err := q.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = $1`, name).Scan(&def)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("inspect constraint %s: %v", name, err)
	}
	return def, true
}

func existingIndexes(t *testing.T, ctx context.Context, q rowQuerier) map[string]bool {
	t.Helper()
	rows, err := q.Query(ctx, `SELECT indexname FROM pg_indexes WHERE tablename = 'transactions'`)
	if err != nil {
		t.Fatalf("list transaction indexes: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index names: %v", err)
	}
	return out
}

// TestOrderPaymentMigrationUpDownUp re-runs 000022 against the live schema
// inside a transaction and rolls it back, proving the DOWN statement really
// removes the link and the following UP statement really recreates it.
func TestOrderPaymentMigrationUpDownUp(t *testing.T) {
	pool := orderTestPool(t)
	ctx := context.Background()

	upSQL := readMigrationFile(t, "000022_order_payment_link.up.sql")
	downSQL := readMigrationFile(t, "000022_order_payment_link.down.sql")

	// Precondition: the database is already migrated up.
	if !hasOrderIDColumn(t, ctx, pool) {
		t.Fatal("transactions.order_id is missing; run migrations up first")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// ── DOWN ────────────────────────────────────────────────────────────────
	if _, err := tx.Exec(ctx, downSQL); err != nil {
		t.Fatalf("migration DOWN failed: %v", err)
	}
	if hasOrderIDColumn(t, ctx, tx) {
		t.Error("DOWN must drop transactions.order_id")
	}
	if _, ok := hasConstraint(t, ctx, tx, "fk_transactions_order"); ok {
		t.Error("DOWN must drop fk_transactions_order")
	}
	for _, name := range orderPaymentIndexes {
		if existingIndexes(t, ctx, tx)[name] {
			t.Errorf("DOWN must drop %s", name)
		}
	}

	// ── UP (again) ──────────────────────────────────────────────────────────
	if _, err := tx.Exec(ctx, upSQL); err != nil {
		t.Fatalf("re-running migration UP failed: %v", err)
	}
	assertOrderPaymentSchema(t, ctx, tx)

	// Legacy rows stay valid across DOWN/UP: order_id comes back nullable.
	var nullable bool
	if err := tx.QueryRow(ctx, `
		SELECT is_nullable = 'YES' FROM information_schema.columns
		WHERE table_schema='public' AND table_name='transactions' AND column_name='order_id'`).Scan(&nullable); err != nil {
		t.Fatalf("inspect nullability: %v", err)
	}
	if !nullable {
		t.Error("transactions.order_id must stay nullable for legacy payments")
	}
}

// TestOrderPaymentSchemaObjects pins the applied migration on the live schema
// (outside any test transaction), including the exact composite FK definition.
func TestOrderPaymentSchemaObjects(t *testing.T) {
	pool := orderTestPool(t)
	assertOrderPaymentSchema(t, context.Background(), pool)
}

func assertOrderPaymentSchema(t *testing.T, ctx context.Context, q rowQuerier) {
	t.Helper()
	if !hasOrderIDColumn(t, ctx, q) {
		t.Fatal("transactions.order_id is missing")
	}

	def, ok := hasConstraint(t, ctx, q, "fk_transactions_order")
	if !ok {
		t.Fatal("fk_transactions_order is missing")
	}
	if !strings.Contains(def, "FOREIGN KEY (merchant_id, order_id)") ||
		!strings.Contains(def, "REFERENCES orders(merchant_id, id)") {
		t.Errorf("fk_transactions_order must be the composite tenant-aware key, got: %s", def)
	}

	indexes := existingIndexes(t, ctx, q)
	for _, name := range orderPaymentIndexes {
		if !indexes[name] {
			t.Errorf("missing index %s", name)
		}
	}
}

func orderPaymentFixture(t *testing.T, pool *pgxpool.Pool) (merchantID, orderID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	merchantID, orderID = uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO merchants(id,name,code,status) VALUES ($1,'Order payment test merchant',$2,'ACTIVE')`,
		merchantID, "orderpay-"+merchantID.String()); err != nil {
		t.Fatalf("insert merchant: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO orders(id,merchant_id,status,currency,subtotal,total_amount,idempotency_key,request_hash)
		VALUES ($1,$2,'CREATED','IDR',15000,15000,$3,$4)`,
		orderID, merchantID, "orderpay-"+orderID.String(), strings.Repeat("0", 64)); err != nil {
		t.Fatalf("insert order: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM transactions WHERE merchant_id=$1`, merchantID)
		_, _ = pool.Exec(c, `DELETE FROM orders WHERE merchant_id=$1`, merchantID)
		_, _ = pool.Exec(c, `DELETE FROM merchants WHERE id=$1`, merchantID)
	})
	return merchantID, orderID
}

func pgError(t *testing.T, err error) *pgconn.PgError {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a PostgreSQL error, got %v", err)
	}
	return pgErr
}

func orderPaymentRow(merchantID uuid.UUID, orderID *uuid.UUID, status model.TransactionStatus, ref string) *model.Transaction {
	now := time.Now().UTC()
	return &model.Transaction{
		ID:              uuid.New(),
		MerchantID:      merchantID,
		OrderID:         orderID,
		MerchantOrderID: ref,
		Amount:          15000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
		Status:          status,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// TestOrderPaymentDatabaseGuards verifies the partial unique indexes, the
// composite foreign key, NULL semantics and tenant isolation end to end
// through the production repository.
func TestOrderPaymentDatabaseGuards(t *testing.T) {
	pool := orderTestPool(t)
	ctx := context.Background()
	repo := repository.NewTransactionRepository(pool)
	lookup, ok := repo.(repository.OrderPaymentLookup)
	if !ok {
		t.Fatal("postgres transaction repository must implement OrderPaymentLookup")
	}

	merchantA, orderA := orderPaymentFixture(t, pool)
	merchantB, orderB := orderPaymentFixture(t, pool)

	// ── Legacy payments: order_id NULL never conflicts ──────────────────────
	for i := 0; i < 2; i++ {
		legacy := orderPaymentRow(merchantA, nil, model.TransactionStatusPending, "legacy-null-"+uuid.NewString())
		if err := repo.Create(ctx, legacy); err != nil {
			t.Fatalf("legacy payment with NULL order_id must stay valid: %v", err)
		}
	}

	// ── Tenant-aware foreign key ────────────────────────────────────────────
	cross := orderPaymentRow(merchantA, &orderB, model.TransactionStatusCreated, "cross-tenant-"+uuid.NewString())
	err := repo.Create(ctx, cross)
	if err == nil {
		t.Fatal("merchant A must not reference merchant B's order")
	}
	if code := pgError(t, err).Code; code != "23503" {
		t.Errorf("expected foreign_key_violation (23503), got %s", code)
	}
	if name := pgError(t, err).ConstraintName; name != "fk_transactions_order" {
		t.Errorf("expected fk_transactions_order, got %s", name)
	}

	missing := uuid.New()
	unknown := orderPaymentRow(merchantA, &missing, model.TransactionStatusCreated, "unknown-order-"+uuid.NewString())
	err = repo.Create(ctx, unknown)
	if err == nil {
		t.Fatal("an unknown order_id must be rejected")
	}
	if code := pgError(t, err).Code; code != "23503" {
		t.Errorf("expected foreign_key_violation (23503), got %s", code)
	}

	// ── At most one active payment per Order ───────────────────────────────
	active := orderPaymentRow(merchantA, &orderA, model.TransactionStatusCreated, "order-active-1")
	if err := repo.Create(ctx, active); err != nil {
		t.Fatalf("first active payment: %v", err)
	}

	second := orderPaymentRow(merchantA, &orderA, model.TransactionStatusPending, "order-active-2")
	err = repo.Create(ctx, second)
	if !errors.Is(err, repository.ErrOrderPaymentConflict) {
		t.Fatalf("second active payment: expected ErrOrderPaymentConflict, got %v", err)
	}

	third := orderPaymentRow(merchantA, &orderA, model.TransactionStatusCreated, "order-active-3")
	err = repo.Create(ctx, third)
	if !errors.Is(err, repository.ErrOrderPaymentConflict) {
		t.Fatalf("third active payment: expected ErrOrderPaymentConflict, got %v", err)
	}

	// ── A terminal failure releases the active slot (retry) ────────────────
	if _, err := pool.Exec(ctx, `UPDATE transactions SET status='FAILED', updated_at=NOW() WHERE id=$1`, active.ID); err != nil {
		t.Fatalf("mark first attempt FAILED: %v", err)
	}
	retry := orderPaymentRow(merchantA, &orderA, model.TransactionStatusPending, "order-retry-1")
	if err := repo.Create(ctx, retry); err != nil {
		t.Fatalf("retry after FAILED must be allowed by the index: %v", err)
	}

	// Another active attempt while the retry is pending is still forbidden.
	pending2 := orderPaymentRow(merchantA, &orderA, model.TransactionStatusCreated, "order-retry-2")
	if err := repo.Create(ctx, pending2); !errors.Is(err, repository.ErrOrderPaymentConflict) {
		t.Fatalf("expected ErrOrderPaymentConflict while a retry is pending, got %v", err)
	}

	// ── At most one PAID payment per Order ─────────────────────────────────
	if _, err := pool.Exec(ctx, `UPDATE transactions SET status='PAID', paid_at=NOW() WHERE id=$1`, retry.ID); err != nil {
		t.Fatalf("mark retry PAID: %v", err)
	}
	paid := orderPaymentRow(merchantA, &orderA, model.TransactionStatusPaid, "order-paid-2")
	err = repo.Create(ctx, paid)
	if !errors.Is(err, repository.ErrOrderPaymentConflict) {
		t.Fatalf("second PAID payment: expected ErrOrderPaymentConflict, got %v", err)
	}

	// ── A different Order of the same merchant is unaffected ───────────────
	otherOrder := secondOrderFor(t, pool, merchantA)
	other := orderPaymentRow(merchantA, &otherOrder, model.TransactionStatusCreated, "order-other-"+uuid.NewString())
	if err := repo.Create(ctx, other); err != nil {
		t.Fatalf("a different order must be allowed: %v", err)
	}

	// ── Tenant isolation on the lookup ─────────────────────────────────────
	rows, err := lookup.FindByMerchantAndOrderID(ctx, merchantB, orderA)
	if err != nil {
		t.Fatalf("cross-merchant lookup: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("merchant B must not see merchant A's attempts, found %d", len(rows))
	}
}

// secondOrderFor creates an additional Order for the given merchant.
func secondOrderFor(t *testing.T, pool *pgxpool.Pool, merchantID uuid.UUID) uuid.UUID {
	t.Helper()
	orderID := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO orders(id,merchant_id,status,currency,subtotal,total_amount,idempotency_key,request_hash)
		VALUES ($1,$2,'CREATED','IDR',20000,20000,$3,$4)`,
		orderID, merchantID, "orderpay-"+orderID.String(), strings.Repeat("a", 64)); err != nil {
		t.Fatalf("insert second order: %v", err)
	}
	return orderID
}

// TestOrderPaymentLookupScoping covers the query the Order payment service
// uses to decide whether a retry is allowed.
func TestOrderPaymentLookupScoping(t *testing.T) {
	pool := orderTestPool(t)
	ctx := context.Background()
	repo := repository.NewTransactionRepository(pool)
	lookup := repo.(repository.OrderPaymentLookup)

	merchantA, orderA := orderPaymentFixture(t, pool)

	// Legacy payment for the same merchant must not look like an attempt.
	legacy := orderPaymentRow(merchantA, nil, model.TransactionStatusPending, "legacy-lookup-"+uuid.NewString())
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatalf("legacy payment: %v", err)
	}

	older := orderPaymentRow(merchantA, &orderA, model.TransactionStatusFailed, "lookup-older")
	older.CreatedAt = time.Now().UTC().Add(-time.Hour)
	if err := repo.Create(ctx, older); err != nil {
		t.Fatalf("older attempt: %v", err)
	}
	newer := orderPaymentRow(merchantA, &orderA, model.TransactionStatusPending, "lookup-newer")
	if err := repo.Create(ctx, newer); err != nil {
		t.Fatalf("newer attempt: %v", err)
	}

	rows, err := lookup.FindByMerchantAndOrderID(ctx, merchantA, orderA)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 attempts (legacy NULL excluded), got %d", len(rows))
	}
	if rows[0].ID != newer.ID || rows[1].ID != older.ID {
		t.Errorf("expected newest first, got %s then %s", rows[0].MerchantOrderID, rows[1].MerchantOrderID)
	}
	for _, row := range rows {
		if row.OrderID == nil || *row.OrderID != orderA {
			t.Errorf("every returned attempt must carry order %s, got %v", orderA, row.OrderID)
		}
	}

	// An order that has never been paid returns no attempts.
	if rows, err := lookup.FindByMerchantAndOrderID(ctx, merchantA, uuid.New()); err != nil || len(rows) != 0 {
		t.Errorf("unknown order: expected (empty, nil), got (%d, %v)", len(rows), err)
	}
}

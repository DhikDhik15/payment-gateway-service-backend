package repository

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func phase8D5Event(merchantID, actorID, targetID uuid.UUID) audit.Event {
	requestID := "req_audit_test"
	ip := "198.51.100.20"
	metadata, _ := audit.MarshalMetadata(map[string]any{
		"old_status": "ACTIVE",
		"new_status": "SUSPENDED",
	})
	return audit.Event{
		MerchantID:  audit.UUIDPtr(merchantID),
		ActorUserID: audit.UUIDPtr(actorID),
		ActorType:   audit.ActorTypeDashboardUser,
		Action:      audit.ActionMerchantStatusChanged,
		TargetType:  targetTypePtr(audit.TargetMerchant),
		TargetID:    audit.UUIDPtr(targetID),
		RequestID:   &requestID,
		IP:          &ip,
		Metadata:    metadata,
	}
}

func targetTypePtr(value audit.TargetType) *audit.TargetType { return &value }

func TestAuditLogRepositoryInsertListAndTenantIsolation(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewAuditLogRepository(pool)
	ctx := context.Background()
	merchantA, merchantB := uuid.New(), uuid.New()
	actorA, actorB := uuid.New(), uuid.New()
	targetA, targetB := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE merchant_id IN ($1, $2)`, merchantA, merchantB)
	})

	if err := repo.Insert(ctx, phase8D5Event(merchantA, actorA, targetA)); err != nil {
		t.Fatalf("insert merchant A event: %v", err)
	}
	if err := repo.Insert(ctx, phase8D5Event(merchantB, actorB, targetB)); err != nil {
		t.Fatalf("insert merchant B event: %v", err)
	}

	rowsA, err := repo.ListByMerchant(ctx, merchantA, 20, 0)
	if err != nil {
		t.Fatalf("list merchant A: %v", err)
	}
	if len(rowsA) != 1 || rowsA[0].MerchantID == nil || *rowsA[0].MerchantID != merchantA {
		t.Fatalf("merchant A rows = %#v, want one A row", rowsA)
	}
	if strings.Contains(string(rowsA[0].Metadata), merchantB.String()) {
		t.Fatal("merchant A result leaked merchant B identifier")
	}
	if rowsA[0].ActorType != string(audit.ActorTypeDashboardUser) {
		t.Fatalf("actor type = %q, want %q", rowsA[0].ActorType, audit.ActorTypeDashboardUser)
	}
	if rowsA[0].ActorUserID == nil || *rowsA[0].ActorUserID != actorA {
		t.Fatalf("actor user ID was not persisted: %#v", rowsA[0].ActorUserID)
	}
	if rowsA[0].RequestID == nil || *rowsA[0].RequestID != "req_audit_test" {
		t.Fatalf("request ID was not persisted: %#v", rowsA[0].RequestID)
	}
	if rowsA[0].IP == nil || *rowsA[0].IP != "198.51.100.20" {
		t.Fatalf("IP was not persisted: %#v", rowsA[0].IP)
	}
	if !json.Valid(rowsA[0].Metadata) {
		t.Fatalf("metadata is not JSON: %q", rowsA[0].Metadata)
	}
}

func TestAuditLogRepositoryRejectsUnsafeAndOversizedMetadata(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewAuditLogRepository(pool)
	ctx := context.Background()
	merchantID := uuid.New()
	base := phase8D5Event(merchantID, uuid.New(), uuid.New())

	unsafe := base
	unsafe.Metadata = json.RawMessage(`{"password":"TEST_PASSWORD_SECRET_123"}`)
	if err := repo.Insert(ctx, unsafe); !errors.Is(err, audit.ErrUnsafeAuditMetadata) {
		t.Fatalf("unsafe metadata error = %v, want %v", err, audit.ErrUnsafeAuditMetadata)
	}

	oversized := base
	oversized.Metadata = json.RawMessage(`{"blob":"` + strings.Repeat("x", audit.MaxMetadataBytes) + `"}`)
	if err := repo.Insert(ctx, oversized); !errors.Is(err, audit.ErrAuditMetadataTooLarge) {
		t.Fatalf("oversized metadata error = %v, want %v", err, audit.ErrAuditMetadataTooLarge)
	}
}

func TestAuditLogRepositoryTransactionRollbackAndCommit(t *testing.T) {
	pool := phase8D4Pool(t)
	repo := NewAuditLogRepository(pool)
	ctx := context.Background()
	merchantID := uuid.New()
	event := phase8D5Event(merchantID, uuid.New(), uuid.New())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE merchant_id = $1`, merchantID)
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollback transaction: %v", err)
	}
	if err := repo.InsertInTx(ctx, tx, event); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert audit in rollback transaction: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	rows, err := repo.ListByMerchant(ctx, merchantID, 20, 0)
	if err != nil {
		t.Fatalf("list after rollback: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rolled-back audit row count = %d, want 0", len(rows))
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin commit transaction: %v", err)
	}
	if err := repo.InsertInTx(ctx, tx, event); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert audit in commit transaction: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	rows, err = repo.ListByMerchant(ctx, merchantID, 20, 0)
	if err != nil {
		t.Fatalf("list after commit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("committed audit row count = %d, want 1", len(rows))
	}
}

type phase8D5RepositoryRecorder struct {
	repo AuditLogRepository
}

func (r *phase8D5RepositoryRecorder) Record(ctx context.Context, event audit.Event) error {
	return r.repo.Insert(ctx, event)
}

func (r *phase8D5RepositoryRecorder) RecordInTx(ctx context.Context, tx pgx.Tx, event audit.Event) error {
	return r.repo.InsertInTx(ctx, tx, event)
}

type phase8D5FailingRecorder struct{}

func (phase8D5FailingRecorder) Record(context.Context, audit.Event) error {
	return errors.New("audit unavailable")
}

func (phase8D5FailingRecorder) RecordInTx(context.Context, pgx.Tx, audit.Event) error {
	return errors.New("audit unavailable")
}

func TestAuditFailureRollsBackBusinessMutation(t *testing.T) {
	pool := phase8D4Pool(t)
	merchantID := uuid.New()
	now := time.Now().UTC()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO merchants (id, name, code, api_key, api_secret, status, created_at, updated_at)
		VALUES ($1, 'Audit rollback merchant', $2, NULL, NULL, 'ACTIVE', $3, $3)
	`, merchantID, "phase8d5-rollback-"+merchantID.String()[:8], now)
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE merchant_id = $1`, merchantID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM merchants WHERE id = $1`, merchantID)
	})

	repo := NewMerchantRepository(pool, phase8D5FailingRecorder{})
	event := phase8D5Event(merchantID, uuid.New(), merchantID)
	ctx := audit.WithEvent(context.Background(), event)
	if err := repo.UpdateStatus(ctx, merchantID, model.MerchantStatusSuspended); err == nil {
		t.Fatal("UpdateStatus() succeeded despite mandatory audit failure")
	}
	var status model.MerchantStatus
	if err := pool.QueryRow(context.Background(), `SELECT status FROM merchants WHERE id = $1`, merchantID).Scan(&status); err != nil {
		t.Fatalf("read merchant after rollback: %v", err)
	}
	if status != model.MerchantStatusActive {
		t.Fatalf("merchant status after audit rollback = %s, want ACTIVE", status)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM audit_logs WHERE merchant_id = $1`, merchantID).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("audit row count after rollback = %d, want 0", count)
	}
}

func TestPhase8D5OwnerAuditCommitsOnlyForSuccessfulMutation(t *testing.T) {
	pool := phase8D4Pool(t)
	merchantID, ownerA, ownerB := seedPhase8D4MerchantAndOwners(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE merchant_id = $1`, merchantID)
	})
	auditRepo := NewAuditLogRepository(pool)
	recorder := &phase8D5RepositoryRecorder{repo: auditRepo}
	repo := NewMerchantUserRepository(pool, recorder)
	ownerRepo, ok := repo.(OwnerInvariantRepository)
	if !ok {
		t.Fatal("merchant user repository does not implement OwnerInvariantRepository")
	}

	makeEvent := func(action audit.Action, target uuid.UUID, oldValue, newValue string) audit.Event {
		metadata, err := audit.MarshalMetadata(map[string]any{
			"old_value": oldValue,
			"new_value": newValue,
		})
		if err != nil {
			t.Fatalf("marshal event metadata: %v", err)
		}
		requestID := "req_owner_audit"
		ip := "198.51.100.44"
		targetType := audit.TargetUser
		return audit.Event{
			MerchantID:  audit.UUIDPtr(merchantID),
			ActorUserID: audit.UUIDPtr(ownerA),
			ActorType:   audit.ActorTypeDashboardUser,
			Action:      action,
			TargetType:  &targetType,
			TargetID:    audit.UUIDPtr(target),
			RequestID:   &requestID,
			IP:          &ip,
			Metadata:    metadata,
		}
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx := audit.WithEvent(context.Background(), makeEvent(audit.ActionUserRoleChanged, ownerA, "OWNER", "ADMIN"))
		_, errs[0] = ownerRepo.UpdateRoleWithOwnerLock(ctx, merchantID, ownerA, model.DashboardUserRoleAdmin)
	}()
	go func() {
		defer wg.Done()
		ctx := audit.WithEvent(context.Background(), makeEvent(audit.ActionUserStatusChanged, ownerB, "ACTIVE", "DISABLED"))
		_, errs[1] = ownerRepo.UpdateStatusWithOwnerLock(ctx, merchantID, ownerB, model.DashboardUserStatusDisabled)
	}()
	wg.Wait()

	successes := 0
	lastOwnerErrors := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrLastActiveOwnerRequired) {
			lastOwnerErrors++
		}
	}
	if successes != 1 || lastOwnerErrors != 1 {
		t.Fatalf("concurrent owner audit results successes=%d last-owner-errors=%d errors=%v", successes, lastOwnerErrors, errs)
	}
	rows, err := NewAuditLogRepository(pool).ListByMerchant(context.Background(), merchantID, 20, 0)
	if err != nil {
		t.Fatalf("list owner audit rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("owner audit row count = %d, want exactly one successful event", len(rows))
	}
}

func TestAuditLogRepositoryInterfaceHasNoMutationMethods(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf((*AuditLogRepository)(nil)).Elem()
	for i := 0; i < typ.NumMethod(); i++ {
		name := strings.ToLower(typ.Method(i).Name)
		if strings.Contains(name, "update") || strings.Contains(name, "delete") {
			t.Fatalf("append-only repository exposes mutation method %s", typ.Method(i).Name)
		}
	}
}

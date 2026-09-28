package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditLogRepository is intentionally append-only. It exposes insertion and
// tenant-scoped reads, but no update or delete operation.
type AuditLogRepository interface {
	Insert(ctx context.Context, event audit.Event) error
	InsertInTx(ctx context.Context, tx pgx.Tx, event audit.Event) error
	ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]model.AuditLog, error)
}

type pgAuditLogRepository struct {
	db *pgxpool.Pool
}

// NewAuditLogRepository returns a PostgreSQL-backed append-only audit repository.
func NewAuditLogRepository(db *pgxpool.Pool) AuditLogRepository {
	return &pgAuditLogRepository{db: db}
}

type auditLogExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func (r *pgAuditLogRepository) Insert(ctx context.Context, event audit.Event) error {
	return insertAuditLog(ctx, r.db, event)
}

func (r *pgAuditLogRepository) InsertInTx(ctx context.Context, tx pgx.Tx, event audit.Event) error {
	return insertAuditLog(ctx, tx, event)
}

func insertAuditLog(ctx context.Context, db auditLogExecutor, event audit.Event) error {
	if err := audit.ValidateEvent(event); err != nil {
		return fmt.Errorf("audit repository insert: %w", err)
	}

	const q = `
		INSERT INTO audit_logs
		    (merchant_id, actor_user_id, actor_type, action, target_type,
		     target_id, request_id, ip, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
	`
	metadata := event.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	_, err := db.Exec(ctx, q,
		nullableAuditUUID(event.MerchantID),
		nullableAuditUUID(event.ActorUserID),
		event.ActorType,
		event.Action,
		nullableAuditStringValue(event.TargetType),
		nullableAuditUUID(event.TargetID),
		nullableAuditString(event.RequestID),
		nullableAuditString(event.IP),
		string(metadata),
	)
	if err != nil {
		return fmt.Errorf("audit repository insert: %w", err)
	}
	return nil
}

func (r *pgAuditLogRepository) ListByMerchant(
	ctx context.Context,
	merchantID uuid.UUID,
	limit, offset int,
) ([]model.AuditLog, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	const q = `
		SELECT id, merchant_id, actor_user_id, actor_type, action,
		       target_type, target_id, request_id, ip, metadata, created_at
		FROM audit_logs
		WHERE merchant_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.db.Query(ctx, q, merchantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("audit repository list by merchant: %w", err)
	}
	defer rows.Close()

	result := make([]model.AuditLog, 0)
	for rows.Next() {
		var entry model.AuditLog
		if err := rows.Scan(
			&entry.ID,
			&entry.MerchantID,
			&entry.ActorUserID,
			&entry.ActorType,
			&entry.Action,
			&entry.TargetType,
			&entry.TargetID,
			&entry.RequestID,
			&entry.IP,
			&entry.Metadata,
			&entry.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("audit repository scan: %w", err)
		}
		result = append(result, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit repository rows: %w", err)
	}
	return result, nil
}

func nullableAuditUUID(value *uuid.UUID) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableAuditString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableAuditStringValue[T ~string](value *T) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

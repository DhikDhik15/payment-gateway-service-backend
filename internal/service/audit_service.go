package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrAuditRepositoryUnavailable = errors.New("audit repository unavailable")

// AuditService is the single application-facing audit writer. Transactional
// repositories use RecordInTx; failed authentication paths may use
// RecordBestEffort without changing the authentication response.
type AuditService interface {
	audit.Recorder
	RecordBestEffort(ctx context.Context, event audit.Event) error
}

type auditService struct {
	repo repository.AuditLogRepository
}

// NewAuditService constructs the centralized security audit service.
func NewAuditService(repo repository.AuditLogRepository) AuditService {
	return &auditService{repo: repo}
}

func (s *auditService) Record(ctx context.Context, event audit.Event) error {
	if s == nil || s.repo == nil {
		return ErrAuditRepositoryUnavailable
	}
	if err := audit.ValidateEvent(event); err != nil {
		return fmt.Errorf("audit service record: %w", err)
	}
	if err := s.repo.Insert(ctx, event); err != nil {
		return fmt.Errorf("audit service record: %w", err)
	}
	return nil
}

func (s *auditService) RecordInTx(ctx context.Context, tx pgx.Tx, event audit.Event) error {
	if s == nil || s.repo == nil || tx == nil {
		return ErrAuditRepositoryUnavailable
	}
	if err := audit.ValidateEvent(event); err != nil {
		return fmt.Errorf("audit service record in transaction: %w", err)
	}
	if err := s.repo.InsertInTx(ctx, tx, event); err != nil {
		return fmt.Errorf("audit service record in transaction: %w", err)
	}
	return nil
}

// RecordBestEffort is reserved for events outside a business transaction,
// such as failed authentication. It logs only safe event dimensions and
// returns the write error so the caller can decide how to proceed without
// weakening the original authentication response.
func (s *auditService) RecordBestEffort(ctx context.Context, event audit.Event) error {
	if err := s.Record(ctx, event); err != nil {
		slog.Warn("security audit write failed",
			slog.String("action", string(event.Action)),
			slog.String("actor_type", string(event.ActorType)),
			slog.String("merchant_id", auditUUIDString(event.MerchantID)),
			// Request IDs are correlation values, not authentication data;
			// omit them from this failure log so a malformed caller cannot
			// smuggle a credential into operational logs.
			slog.String("error", err.Error()),
		)
		return err
	}
	return nil
}

func auditUUIDString(value *uuid.UUID) string {
	if value == nil {
		return ""
	}
	return value.String()
}

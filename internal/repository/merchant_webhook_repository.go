package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrMerchantWebhookConfigNotFound    = errors.New("merchant webhook config not found")
	ErrMerchantWebhookDeliveryNotFound  = errors.New("merchant webhook delivery not found")
	ErrMerchantWebhookDeliveryDuplicate = errors.New("merchant webhook delivery already exists")
)

// MerchantWebhookConfigRepository manages outbound webhook endpoint configuration.
type MerchantWebhookConfigRepository interface {
	Upsert(ctx context.Context, cfg *model.MerchantWebhookConfig) error
	FindByMerchantID(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfig, error)
	FindActiveByMerchantID(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfig, error)
	UpdateSecret(ctx context.Context, merchantID uuid.UUID, encryptedSecret string) (*model.MerchantWebhookConfig, error)
	Disable(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfig, error)
}

type pgMerchantWebhookConfigRepository struct {
	db      *pgxpool.Pool
	auditor audit.Recorder
}

// NewMerchantWebhookConfigRepository returns a PostgreSQL-backed config repository.
func NewMerchantWebhookConfigRepository(db *pgxpool.Pool, recorders ...audit.Recorder) MerchantWebhookConfigRepository {
	var recorder audit.Recorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &pgMerchantWebhookConfigRepository{db: db, auditor: recorder}
}

func (r *pgMerchantWebhookConfigRepository) Upsert(ctx context.Context, cfg *model.MerchantWebhookConfig) error {
	const q = `
		INSERT INTO merchant_webhook_configs
		    (id, merchant_id, url, encrypted_secret, status, description, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (merchant_id) DO UPDATE SET
		    url              = EXCLUDED.url,
		    encrypted_secret = EXCLUDED.encrypted_secret,
		    status           = EXCLUDED.status,
		    description      = EXCLUDED.description,
		    updated_at       = EXCLUDED.updated_at
		RETURNING id, created_at, updated_at`

	_, hasEvent := audit.EventFromContext(ctx)
	if !hasEvent {
		err := r.db.QueryRow(ctx, q,
			cfg.ID, cfg.MerchantID, cfg.URL, cfg.EncryptedSecret, cfg.Status,
			cfg.Description, cfg.CreatedAt, cfg.UpdatedAt,
		).Scan(&cfg.ID, &cfg.CreatedAt, &cfg.UpdatedAt)
		if err != nil {
			return fmt.Errorf("webhook config upsert: %w", err)
		}
		return nil
	}
	if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
		return err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("webhook config upsert begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := tx.QueryRow(ctx, q,
		cfg.ID, cfg.MerchantID, cfg.URL, cfg.EncryptedSecret, cfg.Status,
		cfg.Description, cfg.CreatedAt, cfg.UpdatedAt,
	).Scan(&cfg.ID, &cfg.CreatedAt, &cfg.UpdatedAt); err != nil {
		return fmt.Errorf("webhook config upsert: %w", err)
	}
	if event, ok := audit.EventFromContext(ctx); ok {
		event.TargetID = audit.UUIDPtr(cfg.ID)
		if err := audit.RecordEventInTx(ctx, tx, r.auditor, event); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("webhook config upsert commit: %w", err)
	}
	committed = true
	return nil
}

func (r *pgMerchantWebhookConfigRepository) FindByMerchantID(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfig, error) {
	const q = `
		SELECT id, merchant_id, url, encrypted_secret, status, description, created_at, updated_at
		FROM merchant_webhook_configs
		WHERE merchant_id = $1`
	cfg, err := scanWebhookConfig(r.db.QueryRow(ctx, q, merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantWebhookConfigNotFound
		}
		return nil, fmt.Errorf("webhook config find: %w", err)
	}
	return cfg, nil
}

func (r *pgMerchantWebhookConfigRepository) FindActiveByMerchantID(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfig, error) {
	const q = `
		SELECT id, merchant_id, url, encrypted_secret, status, description, created_at, updated_at
		FROM merchant_webhook_configs
		WHERE merchant_id = $1 AND status = 'ACTIVE'`
	cfg, err := scanWebhookConfig(r.db.QueryRow(ctx, q, merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantWebhookConfigNotFound
		}
		return nil, fmt.Errorf("webhook config find active: %w", err)
	}
	return cfg, nil
}

func (r *pgMerchantWebhookConfigRepository) UpdateSecret(ctx context.Context, merchantID uuid.UUID, encryptedSecret string) (*model.MerchantWebhookConfig, error) {
	const q = `
		UPDATE merchant_webhook_configs
		SET encrypted_secret = $1, updated_at = NOW()
		WHERE merchant_id = $2
		RETURNING id, merchant_id, url, encrypted_secret, status, description, created_at, updated_at`
	_, hasEvent := audit.EventFromContext(ctx)
	if !hasEvent {
		cfg, err := scanWebhookConfig(r.db.QueryRow(ctx, q, encryptedSecret, merchantID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrMerchantWebhookConfigNotFound
			}
			return nil, fmt.Errorf("webhook config rotate: %w", err)
		}
		return cfg, nil
	}
	if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
		return nil, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("webhook config rotate begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	cfg, err := scanWebhookConfig(tx.QueryRow(ctx, q, encryptedSecret, merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantWebhookConfigNotFound
		}
		return nil, fmt.Errorf("webhook config rotate: %w", err)
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("webhook config rotate commit: %w", err)
	}
	committed = true
	return cfg, nil
}

func (r *pgMerchantWebhookConfigRepository) Disable(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfig, error) {
	const q = `
		UPDATE merchant_webhook_configs
		SET status = 'DISABLED', updated_at = NOW()
		WHERE merchant_id = $1
		RETURNING id, merchant_id, url, encrypted_secret, status, description, created_at, updated_at`
	_, hasEvent := audit.EventFromContext(ctx)
	if !hasEvent {
		cfg, err := scanWebhookConfig(r.db.QueryRow(ctx, q, merchantID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrMerchantWebhookConfigNotFound
			}
			return nil, fmt.Errorf("webhook config disable: %w", err)
		}
		return cfg, nil
	}
	if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
		return nil, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("webhook config disable begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	cfg, err := scanWebhookConfig(tx.QueryRow(ctx, q, merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantWebhookConfigNotFound
		}
		return nil, fmt.Errorf("webhook config disable: %w", err)
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("webhook config disable commit: %w", err)
	}
	committed = true
	return cfg, nil
}

func scanWebhookConfig(row pgx.Row) (*model.MerchantWebhookConfig, error) {
	cfg := &model.MerchantWebhookConfig{}
	err := row.Scan(
		&cfg.ID, &cfg.MerchantID, &cfg.URL, &cfg.EncryptedSecret,
		&cfg.Status, &cfg.Description, &cfg.CreatedAt, &cfg.UpdatedAt,
	)
	return cfg, err
}

// ─── Delivery repository ──────────────────────────────────────────────────────

// MerchantWebhookDeliveryRepository is the transactional outbox for outbound webhooks.
type MerchantWebhookDeliveryRepository interface {
	Create(ctx context.Context, d *model.MerchantWebhookDelivery) error
	CreateInTx(ctx context.Context, tx pgx.Tx, d *model.MerchantWebhookDelivery) error
	FindByID(ctx context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDelivery, error)
	ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*model.MerchantWebhookDelivery, int64, error)
	ClaimPending(ctx context.Context, batchSize int, staleAfter time.Duration) ([]*model.MerchantWebhookDelivery, error)
	MarkDelivered(ctx context.Context, id uuid.UUID, httpStatus int, attemptCount int) error
	MarkRetry(ctx context.Context, id uuid.UUID, attemptCount int, nextAttemptAt time.Time, httpStatus *int, lastError string) error
	MarkFailed(ctx context.Context, id uuid.UUID, attemptCount int, httpStatus *int, lastError string) error
	MarkDead(ctx context.Context, id uuid.UUID, attemptCount int, httpStatus *int, lastError string) error
	ManualRetry(ctx context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDelivery, error)
}

type pgMerchantWebhookDeliveryRepository struct {
	db *pgxpool.Pool
}

// NewMerchantWebhookDeliveryRepository returns a PostgreSQL-backed delivery repository.
func NewMerchantWebhookDeliveryRepository(db *pgxpool.Pool) MerchantWebhookDeliveryRepository {
	return &pgMerchantWebhookDeliveryRepository{db: db}
}

const deliveryColumns = `id, merchant_id, config_id, event_id, event_type, transaction_id,
	endpoint_url, payload, attempt_count, status, next_attempt_at,
	last_attempt_at, processing_at, delivered_at, last_http_status,
	last_error, created_at, updated_at`

// deliveryColumnsD qualifies columns for UPDATE … RETURNING with a table alias.
const deliveryColumnsD = `d.id, d.merchant_id, d.config_id, d.event_id, d.event_type, d.transaction_id,
	d.endpoint_url, d.payload, d.attempt_count, d.status, d.next_attempt_at,
	d.last_attempt_at, d.processing_at, d.delivered_at, d.last_http_status,
	d.last_error, d.created_at, d.updated_at`

type deliveryExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func insertDelivery(ctx context.Context, db deliveryExecer, d *model.MerchantWebhookDelivery) error {
	const q = `
		INSERT INTO merchant_webhook_deliveries
		    (id, merchant_id, config_id, event_id, event_type, transaction_id,
		     endpoint_url, payload, attempt_count, status, next_attempt_at,
		     last_attempt_at, processing_at, delivered_at, last_http_status,
		     last_error, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`
	_, err := db.Exec(ctx, q,
		d.ID, d.MerchantID, d.ConfigID, d.EventID, d.EventType, d.TransactionID,
		d.EndpointURL, d.Payload, d.AttemptCount, d.Status, d.NextAttemptAt,
		d.LastAttemptAt, d.ProcessingAt, d.DeliveredAt, d.LastHTTPStatus,
		d.LastError, d.CreatedAt, d.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrMerchantWebhookDeliveryDuplicate
		}
		return fmt.Errorf("webhook delivery create: %w", err)
	}
	return nil
}

func (r *pgMerchantWebhookDeliveryRepository) Create(ctx context.Context, d *model.MerchantWebhookDelivery) error {
	return insertDelivery(ctx, r.db, d)
}

func (r *pgMerchantWebhookDeliveryRepository) CreateInTx(ctx context.Context, tx pgx.Tx, d *model.MerchantWebhookDelivery) error {
	return insertDelivery(ctx, tx, d)
}

func (r *pgMerchantWebhookDeliveryRepository) FindByID(ctx context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDelivery, error) {
	q := `SELECT ` + deliveryColumns + `
		FROM merchant_webhook_deliveries
		WHERE id = $1 AND merchant_id = $2`
	d, err := scanDelivery(r.db.QueryRow(ctx, q, deliveryID, merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantWebhookDeliveryNotFound
		}
		return nil, fmt.Errorf("webhook delivery find: %w", err)
	}
	return d, nil
}

func (r *pgMerchantWebhookDeliveryRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*model.MerchantWebhookDelivery, int64, error) {
	const countQ = `SELECT COUNT(*) FROM merchant_webhook_deliveries WHERE merchant_id = $1`
	var total int64
	if err := r.db.QueryRow(ctx, countQ, merchantID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("webhook delivery count: %w", err)
	}

	q := `SELECT ` + deliveryColumns + `
		FROM merchant_webhook_deliveries
		WHERE merchant_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2 OFFSET $3`
	rows, err := r.db.Query(ctx, q, merchantID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("webhook delivery list: %w", err)
	}
	defer rows.Close()

	var out []*model.MerchantWebhookDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ClaimPending recovers stale PROCESSING rows, then claims due PENDING rows with
// FOR UPDATE SKIP LOCKED and marks them PROCESSING.
func (r *pgMerchantWebhookDeliveryRepository) ClaimPending(ctx context.Context, batchSize int, staleAfter time.Duration) ([]*model.MerchantWebhookDelivery, error) {
	dbTx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("webhook delivery claim begin: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	const recoverQ = `
		UPDATE merchant_webhook_deliveries
		SET status = 'PENDING',
		    processing_at = NULL,
		    updated_at = NOW()
		WHERE status = 'PROCESSING'
		  AND processing_at IS NOT NULL
		  AND processing_at < NOW() - ($1 * INTERVAL '1 millisecond')`
	if _, err := dbTx.Exec(ctx, recoverQ, staleAfter.Milliseconds()); err != nil {
		return nil, fmt.Errorf("webhook delivery recover stale: %w", err)
	}

	claimQ := `
		WITH cte AS (
			SELECT id
			FROM merchant_webhook_deliveries
			WHERE status = 'PENDING'
			  AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at ASC, id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE merchant_webhook_deliveries d
		SET status = 'PROCESSING',
		    processing_at = NOW(),
		    updated_at = NOW()
		FROM cte
		WHERE d.id = cte.id
		RETURNING ` + deliveryColumnsD

	rows, err := dbTx.Query(ctx, claimQ, batchSize)
	if err != nil {
		return nil, fmt.Errorf("webhook delivery claim: %w", err)
	}
	defer rows.Close()

	var claimed []*model.MerchantWebhookDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := dbTx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("webhook delivery claim commit: %w", err)
	}
	return claimed, nil
}

func (r *pgMerchantWebhookDeliveryRepository) MarkDelivered(ctx context.Context, id uuid.UUID, httpStatus int, attemptCount int) error {
	const q = `
		UPDATE merchant_webhook_deliveries
		SET status = 'DELIVERED',
		    attempt_count = $1,
		    last_http_status = $2,
		    last_attempt_at = NOW(),
		    delivered_at = NOW(),
		    processing_at = NULL,
		    last_error = NULL,
		    updated_at = NOW()
		WHERE id = $3 AND status = 'PROCESSING'`
	tag, err := r.db.Exec(ctx, q, attemptCount, httpStatus, id)
	if err != nil {
		return fmt.Errorf("webhook delivery mark delivered: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantWebhookDeliveryNotFound
	}
	return nil
}

func (r *pgMerchantWebhookDeliveryRepository) MarkRetry(ctx context.Context, id uuid.UUID, attemptCount int, nextAttemptAt time.Time, httpStatus *int, lastError string) error {
	errMsg := truncateErr(lastError)
	const q = `
		UPDATE merchant_webhook_deliveries
		SET status = 'PENDING',
		    attempt_count = $1,
		    next_attempt_at = $2,
		    last_http_status = $3,
		    last_error = $4,
		    last_attempt_at = NOW(),
		    processing_at = NULL,
		    updated_at = NOW()
		WHERE id = $5 AND status = 'PROCESSING'`
	tag, err := r.db.Exec(ctx, q, attemptCount, nextAttemptAt, httpStatus, errMsg, id)
	if err != nil {
		return fmt.Errorf("webhook delivery mark retry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantWebhookDeliveryNotFound
	}
	return nil
}

func (r *pgMerchantWebhookDeliveryRepository) MarkFailed(ctx context.Context, id uuid.UUID, attemptCount int, httpStatus *int, lastError string) error {
	errMsg := truncateErr(lastError)
	const q = `
		UPDATE merchant_webhook_deliveries
		SET status = 'FAILED',
		    attempt_count = $1,
		    last_http_status = $2,
		    last_error = $3,
		    last_attempt_at = NOW(),
		    processing_at = NULL,
		    updated_at = NOW()
		WHERE id = $4 AND status = 'PROCESSING'`
	tag, err := r.db.Exec(ctx, q, attemptCount, httpStatus, errMsg, id)
	if err != nil {
		return fmt.Errorf("webhook delivery mark failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantWebhookDeliveryNotFound
	}
	return nil
}

func (r *pgMerchantWebhookDeliveryRepository) MarkDead(ctx context.Context, id uuid.UUID, attemptCount int, httpStatus *int, lastError string) error {
	errMsg := truncateErr(lastError)
	const q = `
		UPDATE merchant_webhook_deliveries
		SET status = 'DEAD',
		    attempt_count = $1,
		    last_http_status = $2,
		    last_error = $3,
		    last_attempt_at = NOW(),
		    processing_at = NULL,
		    updated_at = NOW()
		WHERE id = $4 AND status = 'PROCESSING'`
	tag, err := r.db.Exec(ctx, q, attemptCount, httpStatus, errMsg, id)
	if err != nil {
		return fmt.Errorf("webhook delivery mark dead: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantWebhookDeliveryNotFound
	}
	return nil
}

func (r *pgMerchantWebhookDeliveryRepository) ManualRetry(ctx context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDelivery, error) {
	q := `
		UPDATE merchant_webhook_deliveries
		SET status = 'PENDING',
		    next_attempt_at = NOW(),
		    processing_at = NULL,
		    last_error = NULL,
		    updated_at = NOW()
		WHERE id = $1
		  AND merchant_id = $2
		  AND status IN ('FAILED', 'DEAD', 'PENDING')
		RETURNING ` + deliveryColumns
	d, err := scanDelivery(r.db.QueryRow(ctx, q, deliveryID, merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantWebhookDeliveryNotFound
		}
		return nil, fmt.Errorf("webhook delivery manual retry: %w", err)
	}
	return d, nil
}

func scanDelivery(row pgx.Row) (*model.MerchantWebhookDelivery, error) {
	d := &model.MerchantWebhookDelivery{}
	err := row.Scan(
		&d.ID, &d.MerchantID, &d.ConfigID, &d.EventID, &d.EventType, &d.TransactionID,
		&d.EndpointURL, &d.Payload, &d.AttemptCount, &d.Status, &d.NextAttemptAt,
		&d.LastAttemptAt, &d.ProcessingAt, &d.DeliveredAt, &d.LastHTTPStatus,
		&d.LastError, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func truncateErr(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max]
}

package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrMockPaymentNotFound = errors.New("mock payment not found")
var ErrMockPaymentInvalidState = errors.New("mock payment invalid state")
var ErrMockPaymentExpired = errors.New("mock payment expired")

// MockPaymentRepository is the provider-side persistence boundary.
type MockPaymentRepository interface {
	Create(context.Context, *model.MockPayment) error
	FindByPublicID(context.Context, string) (*model.MockPayment, error)
	Transition(context.Context, string, model.TransactionStatus) (*model.MockPayment, error)
}

type pgMockPaymentRepository struct{ db *pgxpool.Pool }

func NewMockPaymentRepository(db *pgxpool.Pool) MockPaymentRepository {
	return &pgMockPaymentRepository{db}
}

const mockPaymentCols = "public_id, provider_transaction_id, gateway_transaction_id, merchant_order_id, amount, currency, payment_method, status, expired_at, created_at, terminal_at"

func scanMockPayment(row pgx.Row) (*model.MockPayment, error) {
	var p model.MockPayment
	err := row.Scan(&p.PublicID, &p.ProviderTransactionID, &p.GatewayTransactionID, &p.MerchantOrderID, &p.Amount, &p.Currency, &p.PaymentMethod, &p.Status, &p.ExpiredAt, &p.CreatedAt, &p.TerminalAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMockPaymentNotFound
	}
	return &p, err
}
func (r *pgMockPaymentRepository) Create(ctx context.Context, p *model.MockPayment) error {
	_, err := r.db.Exec(ctx, `INSERT INTO mock_payments (`+mockPaymentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, p.PublicID, p.ProviderTransactionID, p.GatewayTransactionID, p.MerchantOrderID, p.Amount, p.Currency, p.PaymentMethod, p.Status, p.ExpiredAt, p.CreatedAt, p.TerminalAt)
	if err != nil {
		return fmt.Errorf("create mock payment: %w", err)
	}
	return nil
}
func (r *pgMockPaymentRepository) FindByPublicID(ctx context.Context, id string) (*model.MockPayment, error) {
	p, err := scanMockPayment(r.db.QueryRow(ctx, `SELECT `+mockPaymentCols+` FROM mock_payments WHERE public_id=$1`, id))
	if err != nil && !errors.Is(err, ErrMockPaymentNotFound) {
		return nil, fmt.Errorf("find mock payment: %w", err)
	}
	return p, err
}
func (r *pgMockPaymentRepository) Transition(ctx context.Context, id string, to model.TransactionStatus) (*model.MockPayment, error) {
	var p model.MockPayment
	err := r.db.QueryRow(ctx, `UPDATE mock_payments SET status=$2, terminal_at=NOW() WHERE public_id=$1 AND status='PENDING' AND expired_at>NOW() RETURNING `+mockPaymentCols, id, to).Scan(&p.PublicID, &p.ProviderTransactionID, &p.GatewayTransactionID, &p.MerchantOrderID, &p.Amount, &p.Currency, &p.PaymentMethod, &p.Status, &p.ExpiredAt, &p.CreatedAt, &p.TerminalAt)
	if err == nil {
		return &p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("transition mock payment: %w", err)
	}
	existing, findErr := r.FindByPublicID(ctx, id)
	if findErr != nil {
		return nil, findErr
	}
	if existing.Status == to {
		return existing, nil
	}
	if !existing.ExpiredAt.After(time.Now().UTC()) {
		return nil, ErrMockPaymentExpired
	}
	return nil, ErrMockPaymentInvalidState
}

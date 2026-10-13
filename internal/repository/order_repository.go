package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrOrderNotFound = errors.New("order not found")

// OrderTransaction keeps product reads, key serialization and both inserts on
// the same database transaction. No dashboard query is permitted without a tenant.
type OrderTransaction interface {
	LockIdempotencyKey(ctx context.Context, merchantID uuid.UUID, key string) error
	FindByIdempotencyKey(ctx context.Context, merchantID uuid.UUID, key string) (*model.Order, error)
	GetProductForOrder(ctx context.Context, merchantID, productID uuid.UUID) (*model.Product, error)
	CreateOrder(ctx context.Context, order *model.Order) error
}

type OrderRepository interface {
	InTransaction(ctx context.Context, fn func(OrderTransaction) error) error
	GetOrderByID(ctx context.Context, merchantID, orderID uuid.UUID) (*model.Order, error)
	ListOrders(ctx context.Context, merchantID uuid.UUID, page, limit int) (model.OrderListResult, error)
}

type pgOrderRepository struct{ db *pgxpool.Pool }

type pgOrderTx struct{ tx pgx.Tx }

func NewOrderRepository(db *pgxpool.Pool) OrderRepository { return &pgOrderRepository{db: db} }

func (r *pgOrderRepository) InTransaction(ctx context.Context, fn func(OrderTransaction) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin order transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := fn(&pgOrderTx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit order transaction: %w", err)
	}
	return nil
}

// The transaction-scoped advisory lock serializes same-merchant/key requests
// before product loading. The permanent UNIQUE(merchant_id,idempotency_key)
// constraint remains the final database-enforced defense against duplicates.
func (r *pgOrderTx) LockIdempotencyKey(ctx context.Context, merchantID uuid.UUID, key string) error {
	_, err := r.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, merchantID.String()+":"+key)
	if err != nil {
		return fmt.Errorf("lock order idempotency key: %w", err)
	}
	return nil
}

func (r *pgOrderTx) FindByIdempotencyKey(ctx context.Context, merchantID uuid.UUID, key string) (*model.Order, error) {
	return getOrder(ctx, r.tx, `merchant_id = $1 AND idempotency_key = $2`, merchantID, key)
}

func (r *pgOrderTx) GetProductForOrder(ctx context.Context, merchantID, productID uuid.UUID) (*model.Product, error) {
	// FOR SHARE freezes price, currency, active and deletion until this order is
	// committed. The same merchant predicate is mandatory on every product read.
	const q = `SELECT id, merchant_id, name, price, currency, active
		FROM products WHERE merchant_id = $1 AND id = $2 FOR SHARE`
	var p model.Product
	err := r.tx.QueryRow(ctx, q, merchantID, productID).Scan(&p.ID, &p.MerchantID, &p.Name, &p.Price, &p.Currency, &p.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load order product: %w", err)
	}
	return &p, nil
}

func (r *pgOrderTx) CreateOrder(ctx context.Context, order *model.Order) error {
	const q = `INSERT INTO orders (id, merchant_id, status, currency, subtotal, total_amount, idempotency_key, request_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at, updated_at`
	if err := r.tx.QueryRow(ctx, q, order.ID, order.MerchantID, order.Status, order.Currency,
		order.Subtotal, order.TotalAmount, order.IdempotencyKey, order.RequestHash).Scan(&order.CreatedAt, &order.UpdatedAt); err != nil {
		return fmt.Errorf("insert order: %w", err)
	}
	for i := range order.Items {
		item := &order.Items[i]
		const q = `INSERT INTO order_items (id, order_id, merchant_id, product_id, product_name, quantity, unit_price, subtotal)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`
		if err := r.tx.QueryRow(ctx, q, item.ID, order.ID, order.MerchantID, item.ProductID,
			item.ProductName, item.Quantity, item.UnitPrice, item.Subtotal).Scan(&item.CreatedAt); err != nil {
			return fmt.Errorf("insert order item: %w", err)
		}
	}
	return nil
}

type orderQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

const orderColumns = `id, merchant_id, status, currency, subtotal, total_amount, idempotency_key, request_hash, created_at, updated_at`

func scanOrder(row pgx.Row) (*model.Order, error) {
	var o model.Order
	err := row.Scan(&o.ID, &o.MerchantID, &o.Status, &o.Currency, &o.Subtotal, &o.TotalAmount,
		&o.IdempotencyKey, &o.RequestHash, &o.CreatedAt, &o.UpdatedAt)
	return &o, err
}

func loadOrderItems(ctx context.Context, db orderQuerier, order *model.Order) error {
	rows, err := db.Query(ctx, `SELECT id, order_id, merchant_id, product_id, product_name, quantity, unit_price, subtotal, created_at
		FROM order_items WHERE merchant_id = $1 AND order_id = $2 ORDER BY product_id NULLS LAST, id`, order.MerchantID, order.ID)
	if err != nil {
		return fmt.Errorf("list order items: %w", err)
	}
	defer rows.Close()
	order.Items = make([]model.OrderItem, 0)
	for rows.Next() {
		var item model.OrderItem
		if err := rows.Scan(&item.ID, &item.OrderID, &item.MerchantID, &item.ProductID,
			&item.ProductName, &item.Quantity, &item.UnitPrice, &item.Subtotal, &item.CreatedAt); err != nil {
			return fmt.Errorf("scan order item: %w", err)
		}
		order.Items = append(order.Items, item)
	}
	return rows.Err()
}

func getOrder(ctx context.Context, db orderQuerier, condition string, args ...any) (*model.Order, error) {
	order, err := scanOrder(db.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE `+condition, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}
	if err := loadOrderItems(ctx, db, order); err != nil {
		return nil, err
	}
	return order, nil
}

func (r *pgOrderRepository) GetOrderByID(ctx context.Context, merchantID, orderID uuid.UUID) (*model.Order, error) {
	return getOrder(ctx, r.db, `merchant_id = $1 AND id = $2`, merchantID, orderID)
}

func (r *pgOrderRepository) ListOrders(ctx context.Context, merchantID uuid.UUID, page, limit int) (model.OrderListResult, error) {
	result := model.OrderListResult{Orders: make([]model.Order, 0), Page: page, Limit: limit}
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM orders WHERE merchant_id = $1`, merchantID).Scan(&result.Total); err != nil {
		return result, fmt.Errorf("count orders: %w", err)
	}
	result.TotalPages = int((result.Total + int64(limit) - 1) / int64(limit))
	rows, err := r.db.Query(ctx, `SELECT `+orderColumns+` FROM orders WHERE merchant_id = $1
		ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, merchantID, limit, (page-1)*limit)
	if err != nil {
		return result, fmt.Errorf("list orders: %w", err)
	}
	for rows.Next() {
		order, scanErr := scanOrder(rows)
		if scanErr != nil {
			rows.Close()
			return result, fmt.Errorf("scan order: %w", scanErr)
		}
		result.Orders = append(result.Orders, *order)
	}
	err = rows.Err()
	rows.Close() // release the connection before fetching item snapshots
	if err != nil {
		return result, err
	}
	for i := range result.Orders {
		if err := loadOrderItems(ctx, r.db, &result.Orders[i]); err != nil {
			return result, err
		}
	}
	return result, nil
}

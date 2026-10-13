-- Migration: 000021_create_orders (up)
-- Phase 8A: merchant-scoped orders with immutable price snapshots.
--
-- Conventions follow the existing schema:
--   * UUID primary keys via gen_random_uuid()
--   * merchant_id FK → merchants(id) ON DELETE CASCADE
--   * TIMESTAMPTZ DEFAULT NOW()
--   * integer minor units for money (BIGINT, no floating point)
--
-- Tenant isolation is enforced by the database, not only by queries:
--   * order_items → orders and order_items → products use COMPOSITE foreign
--     keys on (merchant_id, id), so an item can never point at another
--     merchant's order or product.
--   * The order idempotency key is DURABLE: UNIQUE (merchant_id,
--     idempotency_key) with no expiry, so a key can never create two orders.
--
-- Product deletion: products are hard-deleted (DELETE FROM products). Order
-- items keep their name/price snapshot and only lose the product link via
-- ON DELETE SET NULL (product_id). The column-list form (PostgreSQL 15+) is
-- required because a plain SET NULL on the composite key would also null
-- merchant_id, which is NOT NULL.

-- Composite FK targets. id is already unique, so these add no new semantics,
-- they only make (merchant_id, id) referenceable.
ALTER TABLE products
    ADD CONSTRAINT uq_products_merchant_id_id UNIQUE (merchant_id, id);

CREATE TABLE IF NOT EXISTS orders (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id     UUID         NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    status          VARCHAR(30)  NOT NULL DEFAULT 'CREATED',
    currency        VARCHAR(3)   NOT NULL,
    subtotal        BIGINT       NOT NULL,
    total_amount    BIGINT       NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    request_hash    CHAR(64)     NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_orders_merchant_id_id UNIQUE (merchant_id, id),
    -- Durable, merchant-scoped idempotency (never global, never expires).
    CONSTRAINT uq_orders_merchant_idempotency_key UNIQUE (merchant_id, idempotency_key),
    CONSTRAINT chk_orders_status CHECK (status IN ('CREATED')),
    CONSTRAINT chk_orders_subtotal_non_negative CHECK (subtotal >= 0),
    CONSTRAINT chk_orders_total_amount_non_negative CHECK (total_amount >= 0),
    CONSTRAINT chk_orders_idempotency_key_format CHECK (idempotency_key ~ '^[!-~]{1,255}$'),
    CONSTRAINT chk_orders_request_hash_format CHECK (request_hash ~ '^[0-9a-f]{64}$')
);

-- Dashboard listing: WHERE merchant_id = $1 ORDER BY created_at DESC, id DESC.
CREATE INDEX IF NOT EXISTS idx_orders_merchant_created
    ON orders (merchant_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS order_items (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id     UUID         NOT NULL,
    merchant_id  UUID         NOT NULL,
    product_id   UUID,
    product_name VARCHAR(200) NOT NULL,
    quantity     BIGINT       NOT NULL,
    unit_price   BIGINT       NOT NULL,
    subtotal     BIGINT       NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_order_items_order
        FOREIGN KEY (merchant_id, order_id)
        REFERENCES orders (merchant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_order_items_product
        FOREIGN KEY (merchant_id, product_id)
        REFERENCES products (merchant_id, id) ON DELETE SET NULL (product_id),
    CONSTRAINT chk_order_items_quantity_positive CHECK (quantity > 0),
    CONSTRAINT chk_order_items_unit_price_non_negative CHECK (unit_price >= 0),
    CONSTRAINT chk_order_items_subtotal_non_negative CHECK (subtotal >= 0),
    CONSTRAINT chk_order_items_subtotal CHECK (subtotal = unit_price * quantity)
);

-- Merchant-scoped item reads and FK checks when an order is deleted.
CREATE INDEX IF NOT EXISTS idx_order_items_merchant_order
    ON order_items (merchant_id, order_id);

CREATE INDEX IF NOT EXISTS idx_order_items_order
    ON order_items (order_id);

-- Supports the ON DELETE SET NULL lookup when a product is deleted.
CREATE INDEX IF NOT EXISTS idx_order_items_merchant_product
    ON order_items (merchant_id, product_id)
    WHERE product_id IS NOT NULL;

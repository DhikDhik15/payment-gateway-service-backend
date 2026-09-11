-- Migration: 000002_create_transactions (up)
-- Creates the transactions table that stores every payment attempt from a merchant.

CREATE TABLE IF NOT EXISTS transactions (
    id                  UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id         UUID            NOT NULL REFERENCES merchants (id),
    merchant_order_id   VARCHAR(100)    NOT NULL,
    amount              BIGINT          NOT NULL,
    currency            VARCHAR(3)      NOT NULL,
    payment_method      VARCHAR(50)     NOT NULL,
    provider            VARCHAR(50),
    status              VARCHAR(30)     NOT NULL DEFAULT 'CREATED',
    expired_at          TIMESTAMP WITH TIME ZONE,
    paid_at             TIMESTAMP WITH TIME ZONE,
    created_at          TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- A merchant cannot create two transactions with the same order ID.
CREATE UNIQUE INDEX IF NOT EXISTS uq_transactions_merchant_order
    ON transactions (merchant_id, merchant_order_id);

-- Common query patterns
CREATE INDEX IF NOT EXISTS idx_transactions_merchant_id ON transactions (merchant_id);
CREATE INDEX IF NOT EXISTS idx_transactions_status      ON transactions (status);
CREATE INDEX IF NOT EXISTS idx_transactions_created_at  ON transactions (created_at DESC);

-- Constraint: only allowed status values
ALTER TABLE transactions
    ADD CONSTRAINT chk_transactions_status
    CHECK (status IN ('CREATED', 'PENDING', 'PAID', 'FAILED', 'EXPIRED', 'CANCELLED'));

-- Constraint: amount must be positive
ALTER TABLE transactions
    ADD CONSTRAINT chk_transactions_amount_positive
    CHECK (amount > 0);

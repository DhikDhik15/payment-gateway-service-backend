-- Phase 9A: link the existing canonical transaction/payment record to an Order.
-- Legacy external payments remain valid with order_id NULL.
ALTER TABLE transactions
    ADD COLUMN order_id UUID;

ALTER TABLE transactions
    ADD CONSTRAINT fk_transactions_order
    FOREIGN KEY (merchant_id, order_id)
    REFERENCES orders (merchant_id, id);

CREATE INDEX idx_transactions_merchant_order
    ON transactions (merchant_id, order_id)
    WHERE order_id IS NOT NULL;

-- A retry is allowed only after the prior attempt reaches a terminal failure,
-- cancellation, or expiry. One successful payment is permanent per Order.
CREATE UNIQUE INDEX uq_transactions_order_active
    ON transactions (merchant_id, order_id)
    WHERE order_id IS NOT NULL AND status IN ('CREATED', 'PENDING');

CREATE UNIQUE INDEX uq_transactions_order_paid
    ON transactions (merchant_id, order_id)
    WHERE order_id IS NOT NULL AND status = 'PAID';
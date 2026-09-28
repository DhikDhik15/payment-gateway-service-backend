CREATE TABLE mock_payments (
    public_id               VARCHAR(150) PRIMARY KEY,
    provider_transaction_id VARCHAR(150) NOT NULL UNIQUE,
    gateway_transaction_id  UUID NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    merchant_order_id       VARCHAR(100) NOT NULL,
    amount                  BIGINT NOT NULL CHECK (amount > 0),
    currency                VARCHAR(3) NOT NULL,
    payment_method          VARCHAR(50) NOT NULL,
    status                  VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    expired_at              TIMESTAMPTZ NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    terminal_at             TIMESTAMPTZ,
    CONSTRAINT chk_mock_payments_status CHECK (status IN ('PENDING', 'PAID', 'FAILED', 'EXPIRED'))
);
CREATE INDEX idx_mock_payments_expired_pending ON mock_payments(expired_at) WHERE status = 'PENDING';

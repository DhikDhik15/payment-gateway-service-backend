-- Migration: 000012_refund_core (up)
-- Phase 7B: transaction refund counters, refunds, refund_attempts, idempotency refund_id

ALTER TABLE transactions
    ADD COLUMN IF NOT EXISTS refunded_amount BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reserved_refund_amount BIGINT NOT NULL DEFAULT 0;

ALTER TABLE transactions
    ADD CONSTRAINT chk_transactions_refunded_amount_nonneg
        CHECK (refunded_amount >= 0),
    ADD CONSTRAINT chk_transactions_reserved_refund_amount_nonneg
        CHECK (reserved_refund_amount >= 0),
    ADD CONSTRAINT chk_transactions_refund_totals
        CHECK (refunded_amount + reserved_refund_amount <= amount);

CREATE TABLE IF NOT EXISTS refunds (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id         UUID NOT NULL REFERENCES merchants (id),
    transaction_id      UUID NOT NULL REFERENCES transactions (id),
    amount              BIGINT NOT NULL,
    currency            VARCHAR(3) NOT NULL,
    status              VARCHAR(30) NOT NULL,
    provider            VARCHAR(50) NOT NULL,
    provider_refund_id  VARCHAR(150),
    reason              TEXT,
    failure_code        VARCHAR(50),
    failure_message     TEXT,
    idempotency_key_id  UUID REFERENCES idempotency_keys (id) ON DELETE SET NULL,
    requested_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    succeeded_at        TIMESTAMPTZ,
    failed_at           TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_refunds_amount_positive CHECK (amount > 0),
    CONSTRAINT chk_refunds_status CHECK (
        status IN ('PENDING', 'PROCESSING', 'SUCCEEDED', 'FAILED')
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_refunds_provider_refund_id
    ON refunds (provider, provider_refund_id)
    WHERE provider_refund_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_refunds_merchant_id_created
    ON refunds (merchant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_refunds_transaction_id
    ON refunds (transaction_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_refunds_status
    ON refunds (status);

CREATE TABLE IF NOT EXISTS refund_attempts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    refund_id           UUID NOT NULL REFERENCES refunds (id),
    provider            VARCHAR(50) NOT NULL,
    provider_refund_id  VARCHAR(150),
    request_payload     JSONB,
    response_payload    JSONB,
    status              VARCHAR(30) NOT NULL,
    attempt_number      INT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_refund_attempts_attempt_number_positive CHECK (attempt_number > 0)
);

CREATE INDEX IF NOT EXISTS idx_refund_attempts_refund_id
    ON refund_attempts (refund_id, attempt_number ASC);

ALTER TABLE idempotency_keys
    ADD COLUMN IF NOT EXISTS refund_id UUID REFERENCES refunds (id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_refund_id
    ON idempotency_keys (refund_id)
    WHERE refund_id IS NOT NULL;

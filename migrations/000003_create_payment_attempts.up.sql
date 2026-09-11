-- Migration: 000003_create_payment_attempts (up)
-- Stores every individual communication attempt with a payment provider.

CREATE TABLE IF NOT EXISTS payment_attempts (
    id                      UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id          UUID            NOT NULL REFERENCES transactions (id),
    provider                VARCHAR(50)     NOT NULL,
    provider_transaction_id VARCHAR(100),
    request_payload         JSONB,
    response_payload        JSONB,
    status                  VARCHAR(30)     NOT NULL,
    attempt_number          INT             NOT NULL,
    created_at              TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- Query by transaction to list all attempts for a payment
CREATE INDEX IF NOT EXISTS idx_payment_attempts_transaction_id
    ON payment_attempts (transaction_id);

-- Query latest attempt for a transaction
CREATE INDEX IF NOT EXISTS idx_payment_attempts_transaction_attempt
    ON payment_attempts (transaction_id, attempt_number DESC);

-- Constraint: attempt_number must be positive
ALTER TABLE payment_attempts
    ADD CONSTRAINT chk_payment_attempts_attempt_number_positive
    CHECK (attempt_number > 0);

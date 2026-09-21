-- Migration: 000011_create_merchant_webhook_deliveries (up)
-- Transactional outbox for outbound merchant webhook deliveries (Phase 6).
-- Distinct from inbound webhook_events (provider → gateway).

CREATE TABLE IF NOT EXISTS merchant_webhook_deliveries (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id       UUID NOT NULL REFERENCES merchants (id),
    config_id         UUID REFERENCES merchant_webhook_configs (id) ON DELETE SET NULL,
    event_id          VARCHAR(64) NOT NULL,
    event_type        VARCHAR(64) NOT NULL,
    transaction_id    UUID NOT NULL REFERENCES transactions (id),
    endpoint_url      TEXT NOT NULL,
    payload           JSONB NOT NULL,
    attempt_count     INT NOT NULL DEFAULT 0,
    status            VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    next_attempt_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at   TIMESTAMPTZ,
    processing_at     TIMESTAMPTZ,
    delivered_at      TIMESTAMPTZ,
    last_http_status  INT,
    last_error        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_merchant_webhook_deliveries_status
        CHECK (status IN ('PENDING', 'PROCESSING', 'DELIVERED', 'FAILED', 'DEAD')),
    -- One logical event per merchant (single-endpoint-per-merchant design).
    CONSTRAINT uq_merchant_webhook_deliveries_merchant_event
        UNIQUE (merchant_id, event_id)
);

-- Worker claim query: PENDING due for attempt.
CREATE INDEX IF NOT EXISTS idx_merchant_webhook_deliveries_claim
    ON merchant_webhook_deliveries (next_attempt_at ASC, id ASC)
    WHERE status = 'PENDING';

-- Stale PROCESSING recovery.
CREATE INDEX IF NOT EXISTS idx_merchant_webhook_deliveries_stale
    ON merchant_webhook_deliveries (processing_at ASC)
    WHERE status = 'PROCESSING';

CREATE INDEX IF NOT EXISTS idx_merchant_webhook_deliveries_merchant_id
    ON merchant_webhook_deliveries (merchant_id);

CREATE INDEX IF NOT EXISTS idx_merchant_webhook_deliveries_transaction_id
    ON merchant_webhook_deliveries (transaction_id);

CREATE INDEX IF NOT EXISTS idx_merchant_webhook_deliveries_status
    ON merchant_webhook_deliveries (status);

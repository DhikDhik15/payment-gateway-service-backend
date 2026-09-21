-- Migration: 000005_create_webhook_events (up)
-- Creates the webhook_events table that records every inbound event from
-- payment providers. Kept separate from payment_attempts (which tracks
-- outbound provider API calls) to maintain clear audit boundaries.

CREATE TABLE IF NOT EXISTS webhook_events (
    id                      UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    provider                VARCHAR(50)     NOT NULL,
    event_id                VARCHAR(150)    NOT NULL,
    event_type              VARCHAR(100)    NOT NULL,
    transaction_id          UUID            REFERENCES transactions (id) ON DELETE SET NULL,
    provider_transaction_id VARCHAR(150),
    payload                 JSONB           NOT NULL,
    signature               TEXT,
    status                  VARCHAR(30)     NOT NULL DEFAULT 'RECEIVED',
    error_message           TEXT,
    processed_at            TIMESTAMP WITH TIME ZONE,
    created_at              TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- Idempotency: a provider cannot send the same event_id twice for the same provider.
CREATE UNIQUE INDEX IF NOT EXISTS uq_webhook_events_provider_event_id
    ON webhook_events (provider, event_id);

-- Common query patterns
CREATE INDEX IF NOT EXISTS idx_webhook_events_provider
    ON webhook_events (provider);

CREATE INDEX IF NOT EXISTS idx_webhook_events_event_type
    ON webhook_events (event_type);

CREATE INDEX IF NOT EXISTS idx_webhook_events_transaction_id
    ON webhook_events (transaction_id)
    WHERE transaction_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_webhook_events_provider_transaction_id
    ON webhook_events (provider_transaction_id)
    WHERE provider_transaction_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_webhook_events_status
    ON webhook_events (status);

CREATE INDEX IF NOT EXISTS idx_webhook_events_created_at
    ON webhook_events (created_at DESC);

-- Check constraint: only known status values
ALTER TABLE webhook_events
    ADD CONSTRAINT chk_webhook_events_status
    CHECK (status IN ('RECEIVED', 'PROCESSED', 'FAILED', 'IGNORED'));

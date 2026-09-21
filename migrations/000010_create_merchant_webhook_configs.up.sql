-- Migration: 000010_create_merchant_webhook_configs (up)
-- Outbound merchant webhook endpoint configuration (Phase 6).
-- Separate from inbound provider webhook_events (Phase 3).
-- Design: one webhook configuration row per merchant.

CREATE TABLE IF NOT EXISTS merchant_webhook_configs (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id      UUID NOT NULL REFERENCES merchants (id),
    url              TEXT NOT NULL,
    -- AES-256-GCM ciphertext of the signing secret (base64). Never store plaintext.
    encrypted_secret TEXT NOT NULL,
    status           VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
    description      VARCHAR(100),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_merchant_webhook_configs_status
        CHECK (status IN ('ACTIVE', 'DISABLED')),
    CONSTRAINT uq_merchant_webhook_configs_merchant_id UNIQUE (merchant_id)
);

CREATE INDEX IF NOT EXISTS idx_merchant_webhook_configs_merchant_id
    ON merchant_webhook_configs (merchant_id);

CREATE INDEX IF NOT EXISTS idx_merchant_webhook_configs_status
    ON merchant_webhook_configs (status);

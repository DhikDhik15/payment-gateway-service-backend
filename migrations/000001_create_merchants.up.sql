-- Migration: 000001_create_merchants (up)
-- Creates the merchants table that stores registered merchant accounts.

CREATE TABLE IF NOT EXISTS merchants (
    id          UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(150)    NOT NULL,
    code        VARCHAR(50)     NOT NULL,
    api_key     VARCHAR(255)    NOT NULL,
    api_secret  VARCHAR(255)    NOT NULL,
    status      VARCHAR(20)     NOT NULL DEFAULT 'ACTIVE',
    created_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- Unique constraints
CREATE UNIQUE INDEX IF NOT EXISTS uq_merchants_code    ON merchants (code);
CREATE UNIQUE INDEX IF NOT EXISTS uq_merchants_api_key ON merchants (api_key);

-- Fast lookup by API key (used on every authenticated request)
CREATE INDEX IF NOT EXISTS idx_merchants_api_key ON merchants (api_key);

-- Constraint: only allowed status values
ALTER TABLE merchants
    ADD CONSTRAINT chk_merchants_status
    CHECK (status IN ('ACTIVE', 'INACTIVE', 'SUSPENDED'));

-- Migration: 000014_create_merchant_users (up)
-- Phase 8: Dashboard user accounts and session persistence.
--
-- IMPORTANT: Dashboard authentication is completely separate from merchant API keys.
--   Merchant API keys  → machine-to-machine server integration
--   Dashboard accounts → human login to the management UI
--
-- Email uniqueness is GLOBAL (not per-merchant) for the following reasons:
--   1. Auth lookup goes email → user → merchant in one index scan (no merchant_id needed at login).
--   2. Prevents the same person from accidentally owning seats in two separate merchants.
--   3. Password-reset flows (future) do not need a merchant discriminator.

CREATE TABLE IF NOT EXISTS merchant_users (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id     UUID         NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    email           VARCHAR(254) NOT NULL,
    password_hash   TEXT         NOT NULL,      -- Argon2id hash; never returned in responses
    role            VARCHAR(20)  NOT NULL,
    status          VARCHAR(20)  NOT NULL DEFAULT 'ACTIVE',
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_merchant_users_role CHECK (
        role IN ('OWNER', 'ADMIN', 'VIEWER')
    ),
    CONSTRAINT chk_merchant_users_status CHECK (
        status IN ('ACTIVE', 'DISABLED')
    )
);

-- Globally unique email — the auth lookup path is email → user → merchant.
CREATE UNIQUE INDEX IF NOT EXISTS uq_merchant_users_email
    ON merchant_users (email);

-- Fast per-merchant user listing.
CREATE INDEX IF NOT EXISTS idx_merchant_users_merchant_created
    ON merchant_users (merchant_id, created_at DESC);

-- dashboard_sessions stores opaque refresh tokens as their SHA-256 hash.
-- Plaintext refresh tokens are sent to the client ONCE (via HttpOnly cookie)
-- and are NEVER stored in the database.
CREATE TABLE IF NOT EXISTS dashboard_sessions (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_user_id    UUID         NOT NULL REFERENCES merchant_users(id) ON DELETE CASCADE,
    refresh_token_hash  VARCHAR(64)  NOT NULL,   -- SHA-256 hex of the opaque refresh token
    expires_at          TIMESTAMPTZ  NOT NULL,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    last_used_at        TIMESTAMPTZ
);

-- Token lookup must be O(1) and unique.
CREATE UNIQUE INDEX IF NOT EXISTS uq_dashboard_sessions_token_hash
    ON dashboard_sessions (refresh_token_hash);

-- Session list per user for management / audit purposes.
CREATE INDEX IF NOT EXISTS idx_dashboard_sessions_user_created
    ON dashboard_sessions (merchant_user_id, created_at DESC);

-- Index to speed up expiry cleanup (scan expires_at ascending).
CREATE INDEX IF NOT EXISTS idx_dashboard_sessions_expires
    ON dashboard_sessions (expires_at);

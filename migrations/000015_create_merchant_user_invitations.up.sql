-- Migration: 000015_create_merchant_user_invitations (up)
-- Phase 8B: self-service team invitations (API-first — NO email delivery here).
--
-- Design notes:
--   * The invitation token is an opaque 32-byte random value (hex). Only its
--     SHA-256 hash is stored (token_hash) — the plaintext is returned ONCE in
--     the create response so an external system/frontend can build the link.
--   * Status lifecycle: PENDING → ACCEPTED | EXPIRED | REVOKED.
--     EXPIRED is transitioned lazily (checked at read/acceptance time).
--   * A partial unique index guarantees at most ONE active (PENDING)
--     invitation per (merchant, email) — the Phase 8B duplicate rule.
--   * role allows OWNER/ADMIN/VIEWER: only an OWNER caller can create
--     invitations (Phase 8A policy), so OWNER invitations do not bypass the
--     existing role-management rules.

CREATE TABLE IF NOT EXISTS merchant_user_invitations (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id  UUID         NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    email        VARCHAR(254) NOT NULL,            -- normalised (lowercase, trimmed)
    role         VARCHAR(20)  NOT NULL,
    token_hash   VARCHAR(64)  NOT NULL,            -- SHA-256 hex of the opaque token
    status       VARCHAR(20)  NOT NULL DEFAULT 'PENDING',
    expires_at   TIMESTAMPTZ  NOT NULL,
    accepted_at  TIMESTAMPTZ,                      -- NULL until accepted
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_merchant_user_invitations_role CHECK (
        role IN ('OWNER', 'ADMIN', 'VIEWER')
    ),
    CONSTRAINT chk_merchant_user_invitations_status CHECK (
        status IN ('PENDING', 'ACCEPTED', 'EXPIRED', 'REVOKED')
    )
);

-- Token lookup must be O(1) and unique (tokens are single-use).
CREATE UNIQUE INDEX IF NOT EXISTS uq_merchant_user_invitations_token_hash
    ON merchant_user_invitations (token_hash);

-- One active invitation per (merchant, email). Expired rows are transitioned
-- to EXPIRED by the service before a fresh invitation is created, which
-- releases their slot in this index.
CREATE UNIQUE INDEX IF NOT EXISTS uq_merchant_user_invitations_pending
    ON merchant_user_invitations (merchant_id, email)
    WHERE status = 'PENDING';

-- Fast per-merchant invitation listing (future list/revoke endpoints).
CREATE INDEX IF NOT EXISTS idx_merchant_user_invitations_merchant_created
    ON merchant_user_invitations (merchant_id, created_at DESC);

-- Migration: 000017_legacy_credential_state (up)
-- Phase 8D.3 — legacy plaintext credential freeze / migration / disable lifecycle.

-- 1. Legacy credentials become optional: merchants created after the freeze
--    (and merchants already using only Phase 5C credentials) hold no row-level
--    legacy credential, stored as NULL api_key / api_secret.
--    The unique index on api_key keeps working: PostgreSQL allows many NULLs.
ALTER TABLE merchants ALTER COLUMN api_key DROP NOT NULL;
ALTER TABLE merchants ALTER COLUMN api_secret DROP NOT NULL;

-- 2. Deterministic per-merchant migration state.
--      LEGACY          — row-level plaintext credential still exists (flag-gated).
--      MIGRATED        — canonical Phase 5C credential exists; the legacy key (if
--                        any) still authenticates until it is explicitly disabled.
--      LEGACY_DISABLED — never authenticates again; only reachable from MIGRATED.
--    NULL api_key independently means "no legacy credential exists" — auth never
--    matches a NULL key, and the state column is the authoritative gate.
ALTER TABLE merchants
    ADD COLUMN legacy_credential_state VARCHAR(20) NOT NULL DEFAULT 'LEGACY';

ALTER TABLE merchants
    ADD CONSTRAINT chk_merchants_legacy_credential_state
    CHECK (legacy_credential_state IN ('LEGACY', 'MIGRATED', 'LEGACY_DISABLED'));

ALTER TABLE merchants
    ADD COLUMN legacy_credential_disabled_at TIMESTAMPTZ;

-- Migration: 000017_legacy_credential_state (down)
-- Reverts the Phase 8D.3 state columns and constraint.
--
-- NOTE: NOT NULL on api_key / api_secret is intentionally NOT restored —
-- credential-less merchants (created after the freeze) exist, and restoring
-- NOT NULL would fail. Reverting this migration therefore leaves the columns
-- nullable, which the pre-8D.3 code tolerates because it always supplied them.

ALTER TABLE merchants DROP CONSTRAINT IF EXISTS chk_merchants_legacy_credential_state;

ALTER TABLE merchants DROP COLUMN IF EXISTS legacy_credential_disabled_at;
ALTER TABLE merchants DROP COLUMN IF EXISTS legacy_credential_state;

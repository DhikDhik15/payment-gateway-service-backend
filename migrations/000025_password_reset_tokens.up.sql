-- Phase 8E: dashboard password-reset tokens (forgot-password flow).
--
-- One row per reset request. Only the SHA-256 hex of the opaque bearer token
-- is persisted (token_hash); the plaintext token is disclosed once, inside
-- the rendered reset email, and never again. Single-use is enforced by a
-- conditional claim (see repository); at most one PENDING token per user.
--
-- Extends the email_outbox type CHECK for the PASSWORD_RESET category, per
-- the documented extension point on model.EmailOutbox.

CREATE TABLE password_reset_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES merchant_users (id) ON DELETE CASCADE,
    token_hash  VARCHAR(64) NOT NULL,
    status      VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    consumed_at TIMESTAMPTZ,
    CONSTRAINT chk_password_reset_tokens_status
        CHECK (status IN ('PENDING', 'CONSUMED', 'EXPIRED', 'REVOKED'))
);

CREATE UNIQUE INDEX uq_password_reset_tokens_token_hash
    ON password_reset_tokens (token_hash);

-- At most one active (PENDING) reset request per user. A new request revokes
-- prior PENDING rows in the same transaction; this index is the backstop for
-- concurrent requests.
CREATE UNIQUE INDEX uq_password_reset_tokens_user_pending
    ON password_reset_tokens (user_id)
    WHERE status = 'PENDING';

CREATE INDEX idx_password_reset_tokens_user_created
    ON password_reset_tokens (user_id, created_at DESC);

-- PASSWORD_RESET email category for the transactional outbox.
ALTER TABLE email_outbox
    DROP CONSTRAINT chk_email_outbox_type;

ALTER TABLE email_outbox
    ADD CONSTRAINT chk_email_outbox_type
        CHECK (type IN ('INVITATION', 'PASSWORD_RESET'));

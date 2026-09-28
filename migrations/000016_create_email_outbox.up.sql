-- Migration: 000016_create_email_outbox (up)
-- Transactional outbox for outbound transactional email (Phase 8C.3A).
-- Structural mirror of merchant_webhook_deliveries (000011).
--
-- Design notes:
--   * The rendered email (recipient/subject/text/html) is built BEFORE the
--     invitation transaction and committed atomically with the invitation row:
--     invitation exists ⇔ outbox row exists.
--   * SECURITY: text_body/html_body contain the plaintext invitation token by
--     design (audited in the Phase 8C.3 architecture audit). token_hash is
--     NEVER stored here — merchant_user_invitations remains the authoritative
--     token store. Rows must not be exposed via any API and are consumed by
--     the Phase 8C.3B background worker (retention cleanup: Phase 8C.3C).
--   * No delivery/worker columns beyond the webhook pattern are invented:
--     status lifecycle PENDING → PROCESSING → SENT | FAILED | DEAD mirrors
--     merchant_webhook_deliveries (FAILED = non-retryable, DEAD = attempts
--     exhausted; both terminal).

CREATE TABLE IF NOT EXISTS email_outbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id     UUID NOT NULL REFERENCES merchants (id) ON DELETE CASCADE,
    reference_id    UUID,                     -- type-generic correlation (invitation id); no FK by design
    type            VARCHAR(30) NOT NULL,
    recipient       VARCHAR(254) NOT NULL,    -- PII; delivery target
    subject         VARCHAR(998) NOT NULL,
    text_body       TEXT NOT NULL,            -- SENSITIVE: may contain a bearer token URL
    html_body       TEXT NOT NULL,            -- SENSITIVE: same
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    attempt_count   INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processing_at   TIMESTAMPTZ,
    last_attempt_at TIMESTAMPTZ,
    sent_at         TIMESTAMPTZ,
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_email_outbox_status
        CHECK (status IN ('PENDING', 'PROCESSING', 'SENT', 'FAILED', 'DEAD')),
    CONSTRAINT chk_email_outbox_type
        CHECK (type IN ('INVITATION'))
);

-- Worker claim query (Phase 8C.3B): PENDING due for attempt.
CREATE INDEX IF NOT EXISTS idx_email_outbox_claim
    ON email_outbox (next_attempt_at ASC, id ASC)
    WHERE status = 'PENDING';

-- Stale PROCESSING recovery (Phase 8C.3B).
CREATE INDEX IF NOT EXISTS idx_email_outbox_stale
    ON email_outbox (processing_at ASC)
    WHERE status = 'PROCESSING';

CREATE INDEX IF NOT EXISTS idx_email_outbox_merchant_id
    ON email_outbox (merchant_id);

-- Ops/retention sweeps over delivery state.
CREATE INDEX IF NOT EXISTS idx_email_outbox_status
    ON email_outbox (status);

-- Migration: 000018_create_audit_logs (up)
-- Phase 8D.5: append-only, tenant-aware security audit trail.
--
-- Audit rows intentionally have no foreign keys to merchants, users, or other
-- business records. Security history must survive deletion of a referenced
-- business object. Normal application code has INSERT and tenant-scoped read
-- operations only; there are no UPDATE or DELETE APIs.

CREATE TABLE IF NOT EXISTS audit_logs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id  UUID,
    actor_user_id UUID,
    actor_type   VARCHAR(32) NOT NULL,
    action       VARCHAR(64) NOT NULL,
    target_type  VARCHAR(64),
    target_id    UUID,
    request_id   VARCHAR(128),
    ip           VARCHAR(45),
    metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_audit_logs_actor_type CHECK (
        actor_type IN ('DASHBOARD_USER', 'ADMIN', 'API_KEY', 'SYSTEM', 'UNAUTHENTICATED')
    ),
    CONSTRAINT chk_audit_logs_action CHECK (
        action IN (
            'MERCHANT_CREATED', 'MERCHANT_STATUS_CHANGED', 'USER_CREATED',
            'USER_ROLE_CHANGED', 'USER_STATUS_CHANGED', 'PASSWORD_CHANGED',
            'INVITATION_CREATED', 'INVITATION_ACCEPTED', 'API_KEY_CREATED',
            'API_KEY_ROTATED', 'API_KEY_REVOKED', 'LEGACY_CREDENTIAL_MIGRATED',
            'LEGACY_CREDENTIAL_DISABLED', 'WEBHOOK_CONFIG_CHANGED',
            'ADMIN_AUTH_FAILED'
        )
    ),
    CONSTRAINT chk_audit_logs_metadata_object CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT chk_audit_logs_metadata_size CHECK (octet_length(metadata::text) <= 65536)
);

-- Primary access pattern: one tenant's newest security history.
CREATE INDEX IF NOT EXISTS idx_audit_logs_merchant_created
    ON audit_logs (merchant_id, created_at DESC, id DESC);

-- Investigation by authenticated dashboard actor.
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor_created
    ON audit_logs (actor_user_id, created_at DESC, id DESC)
    WHERE actor_user_id IS NOT NULL;

-- Investigation by event category.
CREATE INDEX IF NOT EXISTS idx_audit_logs_action_created
    ON audit_logs (action, created_at DESC, id DESC);

-- Recent global/system/admin events.
CREATE INDEX IF NOT EXISTS idx_audit_logs_created
    ON audit_logs (created_at DESC, id DESC);

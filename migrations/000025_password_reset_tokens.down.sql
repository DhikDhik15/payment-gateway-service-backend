-- Mirrors 000025_password_reset_tokens.up.sql.

ALTER TABLE email_outbox
    DROP CONSTRAINT chk_email_outbox_type;

ALTER TABLE email_outbox
    ADD CONSTRAINT chk_email_outbox_type
        CHECK (type IN ('INVITATION'));

DROP TABLE IF EXISTS password_reset_tokens;

-- Migration: 000016_create_email_outbox (down)
-- Drops ONLY the email outbox. merchants and merchant_user_invitations
-- (and their data) are untouched; indexes drop with the table.
DROP TABLE IF EXISTS email_outbox;

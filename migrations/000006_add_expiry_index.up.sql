-- Migration: 000006_add_expiry_index (up)
-- Adds a partial index to efficiently find PENDING transactions past their
-- expiry time. The expiry worker issues:
--   SELECT ... WHERE status = 'PENDING' AND expired_at IS NOT NULL AND expired_at <= NOW()
-- This index covers that query without scanning the full table.

CREATE INDEX IF NOT EXISTS idx_transactions_pending_expired
    ON transactions (expired_at ASC)
    WHERE status = 'PENDING'
      AND expired_at IS NOT NULL;

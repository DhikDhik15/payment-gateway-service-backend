-- Migration: 000006_add_expiry_index (down)

DROP INDEX IF EXISTS idx_transactions_pending_expired;

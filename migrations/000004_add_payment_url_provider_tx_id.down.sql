-- Migration: 000004_add_payment_url_provider_tx_id (down)
-- Removes the provider_transaction_id and payment_url columns added in the up migration.

DROP INDEX IF EXISTS idx_transactions_provider_tx_id;

ALTER TABLE transactions
    DROP COLUMN IF EXISTS provider_transaction_id,
    DROP COLUMN IF EXISTS payment_url;

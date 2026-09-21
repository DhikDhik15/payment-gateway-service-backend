-- Migration: 000004_add_payment_url_provider_tx_id (up)
-- Adds payment_url and provider_transaction_id columns to the transactions table.
-- These fields are populated when the payment provider accepts the request
-- and the transaction moves from CREATED to PENDING.

ALTER TABLE transactions
    ADD COLUMN IF NOT EXISTS provider_transaction_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS payment_url             TEXT;

-- Index for looking up transactions by provider transaction ID.
CREATE INDEX IF NOT EXISTS idx_transactions_provider_tx_id
    ON transactions (provider_transaction_id)
    WHERE provider_transaction_id IS NOT NULL;

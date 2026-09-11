-- Migration: 000002_create_transactions (down)

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS chk_transactions_amount_positive;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS chk_transactions_status;
DROP INDEX  IF EXISTS idx_transactions_created_at;
DROP INDEX  IF EXISTS idx_transactions_status;
DROP INDEX  IF EXISTS idx_transactions_merchant_id;
DROP INDEX  IF EXISTS uq_transactions_merchant_order;
DROP TABLE  IF EXISTS transactions;

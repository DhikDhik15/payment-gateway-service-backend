DROP INDEX IF EXISTS uq_transactions_order_paid;
DROP INDEX IF EXISTS uq_transactions_order_active;
DROP INDEX IF EXISTS idx_transactions_merchant_order;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS fk_transactions_order;
ALTER TABLE transactions DROP COLUMN IF EXISTS order_id;
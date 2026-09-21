ALTER TABLE idempotency_keys DROP COLUMN IF EXISTS refund_id;

DROP TABLE IF EXISTS refund_attempts;
DROP TABLE IF EXISTS refunds;

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS chk_transactions_refund_totals;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS chk_transactions_reserved_refund_amount_nonneg;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS chk_transactions_refunded_amount_nonneg;
ALTER TABLE transactions DROP COLUMN IF EXISTS reserved_refund_amount;
ALTER TABLE transactions DROP COLUMN IF EXISTS refunded_amount;

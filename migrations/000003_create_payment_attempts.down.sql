-- Migration: 000003_create_payment_attempts (down)

ALTER TABLE payment_attempts DROP CONSTRAINT IF EXISTS chk_payment_attempts_attempt_number_positive;
DROP INDEX  IF EXISTS idx_payment_attempts_transaction_attempt;
DROP INDEX  IF EXISTS idx_payment_attempts_transaction_id;
DROP TABLE  IF EXISTS payment_attempts;

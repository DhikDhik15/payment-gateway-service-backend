-- Migration: 000001_create_merchants (down)

ALTER TABLE merchants DROP CONSTRAINT IF EXISTS chk_merchants_status;
DROP INDEX  IF EXISTS idx_merchants_api_key;
DROP INDEX  IF EXISTS uq_merchants_api_key;
DROP INDEX  IF EXISTS uq_merchants_code;
DROP TABLE  IF EXISTS merchants;

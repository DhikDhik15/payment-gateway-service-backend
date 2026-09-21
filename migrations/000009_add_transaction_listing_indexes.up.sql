CREATE INDEX idx_transactions_merchant_created_at ON transactions (merchant_id, created_at DESC, id DESC);
CREATE INDEX idx_transactions_merchant_status_created_at ON transactions (merchant_id, status, created_at DESC, id DESC);

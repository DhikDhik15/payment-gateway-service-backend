-- Migration: 000014_create_merchant_users (down)
-- Drops Phase 8 dashboard auth tables only. Does not touch Phase 1–7C data.

DROP TABLE IF EXISTS dashboard_sessions;
DROP TABLE IF EXISTS merchant_users;

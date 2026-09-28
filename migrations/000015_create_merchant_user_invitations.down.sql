-- Migration: 000015_create_merchant_user_invitations (down)
-- Drops the Phase 8B invitation table only. Does not touch merchant_users,
-- dashboard_sessions, or any Phase 1–8A data.

DROP TABLE IF EXISTS merchant_user_invitations;

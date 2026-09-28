-- Migration: 000018_create_audit_logs (down)
-- Phase 8D.5: remove the append-only security audit trail.
DROP TABLE IF EXISTS audit_logs;

-- Migration: 000013_create_settlements (down)
-- Drops Phase 7C settlement/reconciliation tables only. Does not touch Phase 1–7B data.

DROP TABLE IF EXISTS reconciliation_results;
DROP TABLE IF EXISTS reconciliation_runs;
DROP TABLE IF EXISTS settlement_items;
DROP TABLE IF EXISTS settlements;

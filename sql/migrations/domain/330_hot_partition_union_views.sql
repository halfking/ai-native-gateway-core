-- Migration 330: hot + partition union views for live analytics
-- Date: 2026-07-06
-- Purpose:
--   1. Expose request_logs_with_current_month as a stable union of hot + parent.
--   2. Expose usage_ledger_with_current_month as a stable union of hot + parent.
--
-- Rationale:
--   Recent writes land in *_hot tables and are promoted to monthly partitions later.
--   Admin analytics pages must see both recent hot rows and historical partition rows
--   without relying on ad-hoc runtime SQL applied only on one environment.

BEGIN;

CREATE OR REPLACE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs_hot
UNION ALL
SELECT * FROM request_logs;

CREATE OR REPLACE VIEW usage_ledger_with_current_month AS
SELECT * FROM usage_ledger_hot
UNION ALL
SELECT * FROM usage_ledger;

COMMIT;

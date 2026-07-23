-- ============================================================================
-- Migration 344 down: drop system_probe_runs
-- Purpose:  回滚 system_probe_runs 表与索引
-- Rollback: N/A (drop 操作不可逆，请先 backup!)
-- ===========================================================================

\set ON_ERROR_STOP on

BEGIN;

DROP TABLE IF EXISTS system_probe_runs CASCADE;

COMMIT;
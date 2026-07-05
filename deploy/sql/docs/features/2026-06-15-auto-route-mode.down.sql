-- 2026-06-15-auto-route-mode.down.sql
-- v2.0 Auto Route Mode 回滚脚本
--
-- 执行顺序：先删 index → 删列 → 删表

BEGIN;

-- ============================================================================
-- (d) request_logs 回滚
-- ============================================================================
DROP INDEX IF EXISTS idx_request_logs_auto;
ALTER TABLE request_logs DROP COLUMN IF EXISTS auto_confidence;
ALTER TABLE request_logs DROP COLUMN IF EXISTS auto_decision;
ALTER TABLE request_logs DROP COLUMN IF EXISTS auto_profile;
ALTER TABLE request_logs DROP COLUMN IF EXISTS task_type;
ALTER TABLE request_logs DROP COLUMN IF EXISTS is_auto_request;

-- ============================================================================
-- (c) api_key_auto_profile 回滚
-- ============================================================================
DROP TABLE IF EXISTS api_key_auto_profile;

-- ============================================================================
-- (b) credential_model_index 回滚
-- ============================================================================
DROP INDEX IF EXISTS idx_cmi_score;
DROP INDEX IF EXISTS idx_cmi_pressure;
DROP TABLE IF EXISTS credential_model_index;

-- ============================================================================
-- (a) model_task_index 回滚
-- ============================================================================
DROP INDEX IF EXISTS idx_mti_canonical_bkt;
DROP INDEX IF EXISTS idx_mti_task_bucket;
DROP TABLE IF EXISTS model_task_index;

COMMIT;
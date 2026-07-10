-- Migration 361 Down: Revert intent_analysis_adjustments table creation
-- Date: 2026-07-09

-- 删除索引
DROP INDEX IF EXISTS idx_adjustments_rolled_back;
DROP INDEX IF EXISTS idx_adjustments_active;
DROP INDEX IF EXISTS idx_adjustments_tenant_type;

-- 删除表
DROP TABLE IF EXISTS intent_analysis_adjustments CASCADE;

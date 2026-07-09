-- Migration 363 Down: Revert security_detector_config table and views
-- Date: 2026-07-09

-- 删除视图
DROP VIEW IF EXISTS intent_adjustment_effectiveness;
DROP VIEW IF EXISTS intent_classification_metrics;

-- 删除索引
DROP INDEX IF EXISTS idx_security_config_version;
DROP INDEX IF EXISTS idx_security_config_tenant;

-- 删除表
DROP TABLE IF EXISTS security_detector_config CASCADE;

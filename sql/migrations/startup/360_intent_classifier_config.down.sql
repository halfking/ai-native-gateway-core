-- Migration 360 Down: Revert intent_classifier_config table creation
-- Date: 2026-07-09

-- 删除索引
DROP INDEX IF EXISTS idx_intent_config_strategy;
DROP INDEX IF EXISTS idx_intent_config_tenant;

-- 删除视图
DROP VIEW IF EXISTS intent_classifier_active_config;

-- 删除表
DROP TABLE IF EXISTS intent_classifier_config CASCADE;

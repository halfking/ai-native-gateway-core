-- Migration: 001_create_billing_tables (ROLLBACK)
-- Description: 回滚分层计费系统数据库表
-- Author: AI Assistant
-- Date: 2024-11-29

-- 删除触发器
DROP TRIGGER IF EXISTS update_booster_packs_updated_at ON booster_packs;
DROP TRIGGER IF EXISTS update_billing_plans_updated_at ON billing_plans;
DROP TRIGGER IF EXISTS update_api_key_billing_map_updated_at ON api_key_billing_map;
DROP TRIGGER IF EXISTS update_billing_configs_updated_at ON billing_configs;

-- 删除函数
DROP FUNCTION IF EXISTS update_updated_at_column();

-- 删除表（按依赖顺序）
DROP TABLE IF EXISTS booster_packs;
DROP TABLE IF EXISTS billing_plans;
DROP TABLE IF EXISTS api_key_billing_map;
DROP TABLE IF EXISTS billing_configs;

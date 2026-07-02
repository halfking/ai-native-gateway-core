-- Migration 330 Down: Drop Model Pricing Configuration
-- 回滚模型价格配置表和相关对象

-- 删除视图
DROP VIEW IF EXISTS v_model_pricing_comparison;

-- 删除函数
DROP FUNCTION IF EXISTS get_model_pricing_summary(VARCHAR);
DROP FUNCTION IF EXISTS calculate_request_cost(VARCHAR, INT, INT, INT, INT);
DROP FUNCTION IF EXISTS log_model_pricing_change();
DROP FUNCTION IF EXISTS update_model_pricing_updated_at();

-- 删除表（级联删除触发器）
DROP TABLE IF EXISTS model_pricing_history CASCADE;
DROP TABLE IF EXISTS model_pricing CASCADE;

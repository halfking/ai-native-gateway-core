-- 311_prompt_injection_detection.down.sql
-- 回滚提示词注入检测

-- 1. 删除视图
DROP VIEW IF EXISTS prompt_injection_stats_today;

-- 2. 删除函数
DROP FUNCTION IF EXISTS get_prompt_injection_policy(VARCHAR);

-- 3. 删除表（级联删除索引和策略）
DROP TABLE IF EXISTS prompt_injection_detections CASCADE;
DROP TABLE IF EXISTS prompt_injection_rules CASCADE;
DROP TABLE IF EXISTS prompt_injection_policies CASCADE;

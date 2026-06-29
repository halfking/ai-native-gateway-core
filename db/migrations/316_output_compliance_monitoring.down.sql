-- 312_output_compliance_monitoring.down.sql
-- 回滚输出合规监控

-- 1. 删除视图
DROP VIEW IF EXISTS output_compliance_stats_today;

-- 2. 删除表（级联删除索引和策略）
DROP TABLE IF EXISTS output_compliance_audit CASCADE;
DROP TABLE IF EXISTS toxic_keywords CASCADE;
DROP TABLE IF EXISTS pii_patterns CASCADE;
DROP TABLE IF EXISTS output_compliance_policies CASCADE;

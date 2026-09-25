-- 750 down: 撤回 usage_facts 按日分区函数（R68 24h 审计轮，2026-09-26）
--
-- 仅删除函数定义，不删除已建的日分区（无应用查询方依赖日分区存在，
-- DEFAULT 仍兜底；运维手动保留/删除按表存储策略走）。

DROP FUNCTION IF EXISTS ensure_usage_facts_daily_partition(DATE);
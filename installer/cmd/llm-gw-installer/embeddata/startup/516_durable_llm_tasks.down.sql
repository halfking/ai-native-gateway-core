-- Down migration for 516_durable_llm_tasks.sql
-- 回滚说明（doc 18 §19.2）：生产回滚优先关闭 request_survival_durable_enabled，
-- 不删除任务表；本脚本仅用于全新试验环境重建。
BEGIN;

DROP TABLE IF EXISTS durable_pending_outbox;
DROP TABLE IF EXISTS durable_llm_task_events;
DROP TABLE IF EXISTS durable_llm_tasks;

COMMIT;

-- Migration 711 down: drop hosted task delegation tables（纯新增三表，倒序删除）
BEGIN;

DROP TABLE IF EXISTS hosted_task_callbacks;
DROP TABLE IF EXISTS hosted_task_events;
DROP TABLE IF EXISTS hosted_tasks;

COMMIT;

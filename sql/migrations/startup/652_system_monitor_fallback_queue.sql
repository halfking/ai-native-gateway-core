-- Migration: 652 system_monitor_fallback_queue 表启动保障
--
-- Background:
--   deploy/sql/migrations/V352__system_monitor_fallback_queue.sql（2026-07-29）
--   创建了 system_monitor_fallback_queue 表：bg/systemmonitor 在 Redis Submit
--   失败进入 fallback 模式时把任务持久化到该表，healthy 恢复时 drain 回
--   Redis（docs/architecture/2026-07-28-routing-state-anomaly-audit.md §4.2
--   审计 follow-up #2）。
--
--   该表此前只存在于 V 系列 SQL 文件，未纳入 sql/migrations/startup 启动
--   迁移集，导致未手工执行过 V352 的环境（如本地）缺失此表。而 cmd/gateway
--   已提交的启动校验要求：ursm.v2 authoritative 模式（Redis 可用时的默认
--   模式）必须 LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true，systemmonitor 启用
--   后即会读写本表——缺表环境在 Redis 故障恢复 drain 时报
--   SQLSTATE 42P01 "relation system_monitor_fallback_queue does not exist"。
--
--   本迁移把 V352 的建表逻辑纳入启动迁移集，保证所有部署（本地/245/154）
--   具备该表；V352 已执行过的环境为 no-op。
--
-- Idempotent: YES（CREATE TABLE IF NOT EXISTS + 索引 IF NOT EXISTS）。

\set ON_ERROR_STOP on

CREATE TABLE IF NOT EXISTS system_monitor_fallback_queue (
    id           BIGSERIAL PRIMARY KEY,
    task_id      BIGINT      NOT NULL,
    task_json    JSONB       NOT NULL,
    worker_id    TEXT,
    enqueued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT system_monitor_fallback_queue_task_id_key UNIQUE (task_id)
);

CREATE INDEX IF NOT EXISTS idx_system_monitor_fallback_queue_enqueued_at
    ON system_monitor_fallback_queue (enqueued_at);

COMMENT ON TABLE system_monitor_fallback_queue IS
    'systemmonitor fallback mode 任务持久化层。Redis Submit 失败时双写,'
    'healthy 恢复时由 healthCheckLoop drain 回 Redis。审计 follow-up #2。';

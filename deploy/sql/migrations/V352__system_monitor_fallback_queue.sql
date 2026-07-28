-- ===========================================================================
-- File:          deploy/sql/migrations/V352__system_monitor_fallback_queue.sql
-- Database:      llm_gateway
-- Purpose:       创建 system_monitor_fallback_queue 表，用于 systemmonitor
--                 fallback 模式下任务持久化，防止进程重启时丢任务。
-- Status:        active
-- Idempotent:    YES (CREATE TABLE IF NOT EXISTS + 索引 IF NOT EXISTS)
--
-- Changelog:
--   2026-07-29  v1.0  Initial creation
-- ===========================================================================
--
-- Background:
--   bg/systemmonitor 在 Redis Submit 失败时会进入 fallback 模式，任务
--   写入 in-memory channel (`fallbackCh`)。如果进程在 fallback 期间
--   重启，所有未消费的任务都会丢失。
--
--   Audit follow-up #2
--   (docs/architecture/2026-07-28-routing-state-anomaly-audit.md §4.2)
--   修复方案：fallback 模式下同时把任务 JSON 写入本表，作为 durable
--   backstop。在 fallback → healthy 转换时，systemmonitor 健康检查 loop
--   扫描本表并把任务 LPUSH 回 Redis 队列，然后 DELETE。
--
-- Schema:
--   - id              : bigserial 主键，drift-safe 单调
--   - task_json       : JSONB,完整 task 定义 (systemmonitor.Task marshaled)
--   - task_id         : bigint,从 task.ID 提取,便于对账/去重
--   - enqueued_at     : timestamptz,fallback 入表时间(供 stale 清理)
--   - worker_id       : text,本机 worker_id(便于 incident 复盘)
--
-- Idempotency:
--   - ON CONFLICT (task_id) DO NOTHING：同一 task 在多个实例 race 时
--     只保留一份
--   - Periodic cleanup (建议 cron): enqueued_at < now() - 24h 的视为
--     已被 Redis 消费或丢失,清理掉
-- ===========================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS system_monitor_fallback_queue (
    id           BIGSERIAL PRIMARY KEY,
    task_id      BIGINT      NOT NULL,
    task_json    JSONB       NOT NULL,
    worker_id    TEXT,
    enqueued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT system_monitor_fallback_queue_task_id_key UNIQUE (task_id)
);

-- 入表时间索引: 用于 stale 清理 + 顺序 drain
CREATE INDEX IF NOT EXISTS idx_system_monitor_fallback_queue_enqueued_at
    ON system_monitor_fallback_queue (enqueued_at);

-- task_id 已经有 UNIQUE 约束,自动建索引,无需额外创建

COMMENT ON TABLE system_monitor_fallback_queue IS
    'systemmonitor fallback mode 任务持久化层。Redis Submit 失败时双写,'
    'healthy 恢复时由 healthCheckLoop drain 回 Redis。审计 follow-up #2。';

COMMENT ON COLUMN system_monitor_fallback_queue.task_id IS
    'task.ID (单调递增)。ON CONFLICT DO NOTHING 保证多次重试幂等。';

COMMENT ON COLUMN system_monitor_fallback_queue.task_json IS
    '完整 task 定义(systemmonitor.Task JSON-encoded)。drain 时反序列化为 Task。';

COMMENT ON COLUMN system_monitor_fallback_queue.worker_id IS
    '本机 worker_id(便于 incident 复盘时追溯哪个实例的 fallback 写入了任务)。';

COMMIT;

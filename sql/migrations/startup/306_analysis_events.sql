-- Migration 306: Create analysis_events table (V4 治理平台 — 异步分析事件总线)
--
-- Purpose:
--   V4 治理平台的异步分析层需要一个统一的事件落库表。
--   domains/analysis/bus 把 request.completed / session.closed / tool.completed /
--   approval.decided / failure.detected 事件写入本表；
--   workers 按 SubscribedTypes 拉取未消费行 (SELECT ... FOR UPDATE SKIP LOCKED)
--   进行意图、主题、提示词质量、失败模式、优化建议的离线/异步分析。
--
-- Design notes:
--   - 纯 append-only，未消费事件由 processed_at IS NULL 识别
--   - 不分区（分区留给后续按 tenant_id 投影或按月分区；现量级不大）
--   - payload JSONB 保持灵活；具体 schema 由发布方负责
--   - 加 RLS-friendly 列（tenant_id）便于后续多租户过滤
--
-- Date: 2026-06-28

CREATE TABLE IF NOT EXISTS analysis_events (
    id           BIGSERIAL PRIMARY KEY,
    event_id     TEXT NOT NULL UNIQUE,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    session_id   TEXT,
    request_id   TEXT,
    payload      JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    worker       TEXT,
    attempts     INT NOT NULL DEFAULT 0,
    last_error   TEXT
);

CREATE INDEX IF NOT EXISTS idx_analysis_events_unprocessed
    ON analysis_events (occurred_at)
    WHERE processed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_analysis_events_tenant_type
    ON analysis_events (tenant_id, type, occurred_at DESC);

CREATE INDEX IF NOT EXISTS idx_analysis_events_session
    ON analysis_events (session_id, occurred_at DESC)
    WHERE session_id IS NOT NULL;

COMMENT ON TABLE analysis_events IS
    'V4 治理平台异步分析层统一事件表；bus 写入，workers 消费。';
COMMENT ON COLUMN analysis_events.type IS
    '事件类型；与 domain/analysis.EventType 对应 (request.completed, session.closed, tool.completed, approval.decided, failure.detected)';
COMMENT ON COLUMN analysis_events.payload IS
    '与 type 对应的强类型 payload；schema 由发布方负责';
COMMENT ON COLUMN analysis_events.processed_at IS
    'NULL 表示未消费；workers 应使用 FOR UPDATE SKIP LOCKED 抢占';

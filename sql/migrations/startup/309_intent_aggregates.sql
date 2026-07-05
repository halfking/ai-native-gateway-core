-- Migration 309: Create intent_aggregates table (V4 治理平台 — 资产沉淀)
--
-- Purpose:
--   V4 异步分析层 IntentWorker 的最终落表：按 (tenant_id, intent_kind) 累计
--   request.completed 事件的意图分类计数，供 admin UI / 资产中心展示。
--
-- Design notes:
--   - 复合主键 (tenant_id, intent_kind)；Upsert 通过 ON CONFLICT
--   - count 是 BIGINT（自动递增；IntentWorker 每次 flush delta=1）
--   - last_updated 用于 stale 检测；admin UI 可过滤 N 天未更新租户
--
-- Date: 2026-06-29

CREATE TABLE IF NOT EXISTS intent_aggregates (
    tenant_id    TEXT NOT NULL,
    intent_kind  TEXT NOT NULL,
    count        BIGINT NOT NULL DEFAULT 0,
    last_updated TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, intent_kind)
);

CREATE INDEX IF NOT EXISTS idx_intent_aggregates_tenant_updated
    ON intent_aggregates (tenant_id, last_updated DESC);

COMMENT ON TABLE intent_aggregates IS
    'V4 治理平台 — 按 (租户, 意图类型) 累计 request.completed 事件分类计数';
COMMENT ON COLUMN intent_aggregates.count IS
    '该 (tenant_id, intent_kind) 累计命中次数；Upsert 时 count = count + delta';
COMMENT ON COLUMN intent_aggregates.last_updated IS
    '最近一次增量时间；admin UI 用于判定"近 7 天活跃意图"';

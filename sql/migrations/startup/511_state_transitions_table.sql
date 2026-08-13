-- Migration 511: Create request_state_transitions table (V3.2 状态变更历史)
--
-- 日期: 2026-08-13
--
-- Purpose
-- ───────
-- 会话优化 V3.2 需要在首页"实时请求流"展示请求生命周期状态变更：
--   到达 → 路由 → 选节点 → 切换节点 → 重试(特殊标) → 回复/错误。
-- 本表记录每次状态变更，供 GET /api/admin/requests/{id}/transitions 查询。
--
-- 关联键: request_id（复用现有 request_id 贯穿机制，ADR-V3-102，不造新 ID）。
--
-- 写入: 旁路异步（ADR-V3-104），不阻塞 relay 主链路。
-- 保留: 7 天，由清理任务定期删除（Task-BE-B1）。
--
-- Idempotent: YES (IF NOT EXISTS)
-- Down: 511_state_transitions_table.down.sql
-- Breaking: NO (纯新增表)

BEGIN;

CREATE TABLE IF NOT EXISTS request_state_transitions (
    id              BIGSERIAL PRIMARY KEY,
    request_id      TEXT NOT NULL,
    transition_type TEXT NOT NULL
                    CHECK (transition_type IN ('route','node_switch','retry','error','state')),
    from_state      TEXT,
    to_state        TEXT,
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE request_state_transitions IS
  'V3.2 (2026-08-13): 请求生命周期状态变更历史。'
  'transition_type: route=路由决策, node_switch=节点切换, retry=重试(特殊标), error=终态错误, state=通用状态变更。'
  'metadata 存决策原因/候选列表/retry_seq/reason_class 等。保留 7 天。';

COMMENT ON COLUMN request_state_transitions.request_id IS
  '关联 request_logs.request_id（复用现有贯穿机制，ADR-V3-102）';
COMMENT ON COLUMN request_state_transitions.metadata IS
  'JSONB：路由决策含 candidates/block_reason；重试含 retry_seq/reason_class；节点切换含 from_node/to_node';

CREATE INDEX IF NOT EXISTS idx_state_transitions_request
    ON request_state_transitions (request_id, created_at DESC);

-- 清理任务按时间删除用索引
CREATE INDEX IF NOT EXISTS idx_state_transitions_created
    ON request_state_transitions (created_at);

COMMIT;

-- POST_CONDITION:
--   SELECT tablename FROM pg_tables WHERE tablename = 'request_state_transitions';
--   SELECT indexname FROM pg_indexes WHERE tablename = 'request_state_transitions';

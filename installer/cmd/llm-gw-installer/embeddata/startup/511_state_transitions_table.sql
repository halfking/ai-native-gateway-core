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
    tenant_id       TEXT NOT NULL,
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
COMMENT ON COLUMN request_state_transitions.tenant_id IS
  '租户 ID，必须与 request_logs.tenant_id 一致，用于 RLS 隔离';
COMMENT ON COLUMN request_state_transitions.metadata IS
  'JSONB：路由决策含 candidates/block_reason；重试含 retry_seq/reason_class；节点切换含 from_node/to_node';

-- 租户隔离索引（支持 RLS 策略高效过滤）
CREATE INDEX IF NOT EXISTS idx_state_transitions_tenant_request
    ON request_state_transitions (tenant_id, request_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_state_transitions_request
    ON request_state_transitions (request_id, created_at DESC);

-- 清理任务按时间删除用索引
CREATE INDEX IF NOT EXISTS idx_state_transitions_created
    ON request_state_transitions (created_at);

-- ══════════════════════════════════════════════════════════════════════════
-- RLS（Row-Level Security）租户隔离
-- ══════════════════════════════════════════════════════════════════════════

ALTER TABLE request_state_transitions ENABLE ROW LEVEL SECURITY;

-- 策略 1：租户隔离（应用连接必须设置 app.current_tenant）
DROP POLICY IF EXISTS state_transitions_tenant_isolation ON request_state_transitions;
CREATE POLICY state_transitions_tenant_isolation ON request_state_transitions
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

-- 策略 2：super_admin 绕过（运维查询全租户数据）
DROP POLICY IF EXISTS state_transitions_super_admin_bypass ON request_state_transitions;
CREATE POLICY state_transitions_super_admin_bypass ON request_state_transitions
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

-- 策略 3：NOSUPERUSER 防护（禁止 SUPERUSER 绕过 RLS）
-- 注意：NOSUPERUSER 是 role 属性，不是 RLS 策略，此处为文档说明
-- 应用连接角色应创建为：CREATE ROLE app_user NOSUPERUSER NOBYPASSRLS;

COMMENT ON POLICY state_transitions_tenant_isolation ON request_state_transitions IS
    'RLS 租户隔离：只允许查询当前租户（app.current_tenant）的状态变更记录';

COMMENT ON POLICY state_transitions_super_admin_bypass ON request_state_transitions IS
    'RLS super_admin 绕过：运维角色（app.current_role=super_admin）可查询全部租户数据';

COMMIT;

-- POST_CONDITION:
--   1. 表已创建：
--      SELECT tablename FROM pg_tables WHERE tablename = 'request_state_transitions';
--   2. tenant_id 列存在：
--      SELECT column_name FROM information_schema.columns 
--      WHERE table_name = 'request_state_transitions' AND column_name = 'tenant_id';
--   3. 索引已创建：
--      SELECT indexname FROM pg_indexes WHERE tablename = 'request_state_transitions';
--      -- 预期：idx_state_transitions_tenant_request, idx_state_transitions_request, idx_state_transitions_created
--   4. RLS 已启用：
--      SELECT relname, relrowsecurity FROM pg_class WHERE relname = 'request_state_transitions';
--      -- 预期：relrowsecurity = true
--   5. RLS 策略已创建：
--      SELECT policyname FROM pg_policies WHERE tablename = 'request_state_transitions';
--      -- 预期：state_transitions_tenant_isolation, state_transitions_super_admin_bypass

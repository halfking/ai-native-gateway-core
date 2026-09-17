-- 722 (R40 durable family schema convergence, 2026-09-18): repair存量库的
-- durable 家族 schema 漂移——把 516 最终形态的幂等体（events/pending outbox
-- 两表 + RLS + durable_llm_tasks.checkpoint_payload 列）重放到早期应用过
-- 516 中间形态的库上。
--
-- 根因（R40 审计，本机 llm_gateway 真库取证）：2026-08-15 19:14 本机以
-- 未经 git 提交的 516 中间形态建表（durable_llm_tasks 缺 checkpoint_payload，
-- durable_llm_task_events / durable_pending_outbox 两表缺失），该中间形态
-- 从未入库；随后 516 以最终形态提交（ca16dedb0/2d0037507 血统）。存量库
-- 由此卡死：516 marker 已登记（sequence 不可重放）、Go ensure 链无 durable
-- 家族条目（搜索 db/db.go 零命中）、代码侧 appendEvent/enqueuePendingOutbox/
-- CheckpointCommitState 直接引用缺失表/列——首次 durable 事件写入即
-- 42P10/42703 运行时失败。
--
-- 全部语句幂等（IF NOT EXISTS / DROP POLICY IF EXISTS / ADD COLUMN IF NOT
-- EXISTS）：全新安装（516 已完整建齐）跑本迁移为零变化；漂移库一次收敛。
-- 保留中间形态的历史列（commit_metadata/connection_attached/last_disconnect_at，
-- 当前代码零引用）为惰性历史数据，清理属运维决策不在本迁移范围。
--
-- RLS 政策与 516 逐字同形（app.current_tenant 隔离 + super_admin/bypass
-- 旁路），superuser 时期零行为变化；配合 R40 durable store GUC 补齐
-- （durable/rls.go），构成 Phase 3 durable 批 FORCE 的前置。

-- ── durable_llm_task_events（append-only 生命周期事件，516 §12.2）──

CREATE TABLE IF NOT EXISTS durable_llm_task_events (
    id            BIGSERIAL PRIMARY KEY,
    task_id       UUID NOT NULL,
    request_id    TEXT NOT NULL,
    session_id    TEXT NOT NULL,
    tenant_id     TEXT NOT NULL,
    attempt       INT NOT NULL DEFAULT 0,
    from_status   TEXT NOT NULL DEFAULT '',
    to_status     TEXT NOT NULL DEFAULT '',
    reason        TEXT NOT NULL DEFAULT '',
    fencing_token BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_durable_task_events_task
    ON durable_llm_task_events (task_id, created_at);
CREATE INDEX IF NOT EXISTS idx_durable_task_events_tenant
    ON durable_llm_task_events (tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_durable_task_events_request
    ON durable_llm_task_events (request_id, created_at);

-- ── durable_pending_outbox（Redis 投影 outbox，516 §11.4）──

CREATE TABLE IF NOT EXISTS durable_pending_outbox (
    task_id         UUID PRIMARY KEY REFERENCES durable_llm_tasks(id) ON DELETE CASCADE,
    tenant_id       TEXT NOT NULL,
    result_version  BIGINT NOT NULL,
    attempts        INT NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT durable_pending_outbox_version_positive CHECK (result_version > 0)
);

CREATE INDEX IF NOT EXISTS idx_durable_pending_outbox_retry
    ON durable_pending_outbox (next_attempt_at, created_at);

-- ── durable_llm_tasks.checkpoint_payload（write-ahead checkpoint 列）──

ALTER TABLE durable_llm_tasks ADD COLUMN IF NOT EXISTS checkpoint_payload JSONB;

-- ── RLS：与 516 逐字同形（幂等重申，漂移方向双向守卫）──

ALTER TABLE durable_llm_tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE durable_llm_task_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE durable_pending_outbox ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS durable_llm_tasks_tenant_isolation ON durable_llm_tasks;
CREATE POLICY durable_llm_tasks_tenant_isolation ON durable_llm_tasks
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS durable_llm_tasks_super_admin_bypass ON durable_llm_tasks;
CREATE POLICY durable_llm_tasks_super_admin_bypass ON durable_llm_tasks
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS durable_llm_task_events_tenant_isolation ON durable_llm_task_events;
CREATE POLICY durable_llm_task_events_tenant_isolation ON durable_llm_task_events
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS durable_llm_task_events_super_admin_bypass ON durable_llm_task_events;
CREATE POLICY durable_llm_task_events_super_admin_bypass ON durable_llm_task_events
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS durable_pending_outbox_tenant_isolation ON durable_pending_outbox;
CREATE POLICY durable_pending_outbox_tenant_isolation ON durable_pending_outbox
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS durable_pending_outbox_super_admin_bypass ON durable_pending_outbox;
CREATE POLICY durable_pending_outbox_super_admin_bypass ON durable_pending_outbox
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

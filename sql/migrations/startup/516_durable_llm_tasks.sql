-- Migration 516: durable_llm_tasks + durable_llm_task_events（doc 18 §11/§12.2，SR-08）
--
-- 日期: 2026-08-15
--
-- Purpose
-- ───────
-- M3 SR-W3 Wave A：供应商耗尽长时保活的持久接管存储层
-- （docs/修订0811/18-供应商耗尽长时保活与持久恢复设计-2026-08-14.md）。
--   - durable_llm_tasks：任务状态与最终结果 SSoT（§11.1），承载 lease/fencing
--     调度、write-ahead commit_state 门禁与原子终态提交；
--   - durable_llm_task_events：append-only 生命周期事件（§12.2）。
--
-- 上线纪律（§19.1 步骤 1）：本 migration 只合入 schema，不启动 worker，
-- durable 行为由 request_survival_durable_enabled（默认 false）门控。
--
-- Idempotent: YES (IF NOT EXISTS / DROP POLICY IF EXISTS)
-- Down: 516_durable_llm_tasks.down.sql
-- Breaking: NO（纯新增表，不触碰 request_wal）

BEGIN;

CREATE TABLE IF NOT EXISTS durable_llm_tasks (
    id                            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                     TEXT NOT NULL,
    request_id                    TEXT NOT NULL,
    session_id                    TEXT NOT NULL,

    -- 重建内部请求所需的最小路由信息（§11.2：候选/凭据/熔断等恢复时重算，
    -- 禁止持久化复用旧候选，故这里不存候选列表）。
    protocol                      TEXT NOT NULL DEFAULT '',
    endpoint                      TEXT NOT NULL DEFAULT '',

    -- 版本化加密快照（§11.2：durable-request 域 AAD，绑定 tenant/task/request hash）
    request_snapshot_ciphertext   TEXT NOT NULL,
    snapshot_version              INT NOT NULL DEFAULT 1,
    encryption_key_id             TEXT NOT NULL,
    request_hash                  TEXT NOT NULL,

    -- 状态机（§11.3）
    status                        TEXT NOT NULL,
    error_kind                    TEXT NOT NULL DEFAULT '',
    reason_code                   TEXT NOT NULL DEFAULT '',
    attempt_count                 INT NOT NULL DEFAULT 0,
    next_retry_at                 TIMESTAMPTZ,
    deadline_at                   TIMESTAMPTZ NOT NULL,
    -- Redis 投影 TTL 基准 = deadline + result_read_window（§12.1）
    expires_at                    TIMESTAMPTZ NOT NULL,

    -- 租约与 fencing（§11.3：过期 worker 的更新因 token 不匹配而失败）
    lease_owner                   TEXT,
    lease_until                   TIMESTAMPTZ,
    fencing_token                 BIGINT NOT NULL DEFAULT 0,

    -- write-ahead commit 门禁（§11.1/§11.3）
    semantic_content_committed    BOOLEAN NOT NULL DEFAULT FALSE,
    commit_state                  TEXT NOT NULL DEFAULT 'none',
    -- tool_call checkpoint 载荷：tool call ID/类型/序号/参数摘要与 hash（§11.3）
    checkpoint_payload            JSONB,

    -- 原子终态结果（§11.3：在 fencing 事务内与 status 一起写入）
    result_ciphertext             TEXT,
    result_object_ref             TEXT,
    result_hash                   TEXT,
    result_version                BIGINT,
    content_type                  TEXT,

    -- 固化请求创建时策略（§11.1）
    policy                        JSONB,

    created_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at                  TIMESTAMPTZ,

    CONSTRAINT durable_llm_tasks_status_check
        CHECK (status IN ('running', 'waiting_recovery', 'retry_scheduled',
                          'completed', 'failed', 'expired', 'canceled',
                          'resume_safety_blocked')),
    CONSTRAINT durable_llm_tasks_commit_state_check
        CHECK (commit_state IN ('none', 'metadata', 'content', 'tool_call', 'terminal')),
    CONSTRAINT durable_llm_tasks_fencing_non_negative
        CHECK (fencing_token >= 0),
    CONSTRAINT durable_llm_tasks_attempt_non_negative
        CHECK (attempt_count >= 0),
    -- 终态必须携带 result_version 与 completed_at（§11.3 原子终态）
    CONSTRAINT durable_llm_tasks_terminal_complete
        CHECK (status NOT IN ('completed', 'failed', 'expired', 'canceled')
               OR (completed_at IS NOT NULL AND result_version IS NOT NULL)),
    CONSTRAINT durable_llm_tasks_tenant_request_unique
        UNIQUE (tenant_id, request_id)
);

COMMENT ON TABLE durable_llm_tasks IS
  'SR-08 (doc 18 §11.1): durable 任务状态与最终结果 SSoT。'
  '快照/结果密文分别用 llm-gateway:durable-request:v1 / durable-result:v1 域 AAD 加密。';

COMMENT ON COLUMN durable_llm_tasks.commit_state IS
  'write-ahead 语义提交门禁：none/metadata 可安全重放重试；'
  'content/tool_call/terminal 之后禁止回到 runnable（全局 claim 门禁）';

COMMENT ON COLUMN durable_llm_tasks.fencing_token IS
  '每次租约变更 +1；所有完成/重排/checkpoint 更新必须携带 (lease_owner, fencing_token)，'
  '过期 worker 更新 0 行即放弃（§11.3）';

-- ════════════════════════════════════════════════════════════════════════
-- 索引（§11.1 下限 + reaper/回源路径）
-- ════════════════════════════════════════════════════════════════════════

-- worker claim 轮询：runnable 状态按 next_retry_at 排序（partial index）
CREATE INDEX IF NOT EXISTS idx_durable_tasks_runnable
    ON durable_llm_tasks (status, next_retry_at)
    WHERE status IN ('waiting_recovery', 'retry_scheduled', 'running');

CREATE INDEX IF NOT EXISTS idx_durable_tasks_tenant_status
    ON durable_llm_tasks (tenant_id, status);

-- lease 过期回收扫描
CREATE INDEX IF NOT EXISTS idx_durable_tasks_lease_expiry
    ON durable_llm_tasks (lease_until)
    WHERE lease_until IS NOT NULL
      AND status NOT IN ('completed', 'failed', 'expired', 'canceled');

-- deadline reaper 扫描（§11.3）
CREATE INDEX IF NOT EXISTS idx_durable_tasks_deadline
    ON durable_llm_tasks (deadline_at)
    WHERE status NOT IN ('completed', 'failed', 'expired', 'canceled');

-- PendingStore 回源（doc 18 §12.1：Redis 丢失时按 session+request 回源）
CREATE INDEX IF NOT EXISTS idx_durable_tasks_session_request
    ON durable_llm_tasks (session_id, request_id, updated_at DESC);

-- safety reaper：非终态但已越过语义 checkpoint 的任务（§11.3）
CREATE INDEX IF NOT EXISTS idx_durable_tasks_unsafe_checkpoint
    ON durable_llm_tasks (updated_at)
    WHERE commit_state IN ('content', 'tool_call', 'terminal')
      AND status NOT IN ('completed', 'failed', 'expired', 'canceled');

-- ════════════════════════════════════════════════════════════════════════
-- durable_llm_task_events：append-only 生命周期事件（§12.2）
-- ════════════════════════════════════════════════════════════════════════

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
    -- connection_detached 等连接属性事件不改 scheduler status（§12.2），
    -- 事件类型/原因记录在 reason。
    fencing_token BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE durable_llm_task_events IS
  'SR-08 (doc 18 §12.2): durable 任务 append-only 生命周期事件；'
  '每条含 task/request/session/tenant、attempt、from/to、reason、时间与 fencing token。';

CREATE INDEX IF NOT EXISTS idx_durable_task_events_task
    ON durable_llm_task_events (task_id, created_at);
CREATE INDEX IF NOT EXISTS idx_durable_task_events_tenant
    ON durable_llm_task_events (tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_durable_task_events_request
    ON durable_llm_task_events (request_id, created_at);

-- Redis PendingStore 投影 outbox（§11.4）：与 PG 终态同事务写入。
-- Redis 不可用时保留行，由 repair projector 重试；同一 task 仅保留最高版本。
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

-- ════════════════════════════════════════════════════════════════════════
-- RLS（doc 18 §11.2：任务表启用 tenant RLS；worker 使用受控服务角色）
-- 跟随 511/322 惯例：app.current_tenant 隔离 + super_admin/bypass。
-- ════════════════════════════════════════════════════════════════════════

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

COMMIT;

-- POST_CONDITION:
--   1. 两表存在：
--      SELECT tablename FROM pg_tables
--      WHERE tablename IN ('durable_llm_tasks','durable_llm_task_events');
--   2. commit_state 约束生效：
--      SELECT conname FROM pg_constraint WHERE conname = 'durable_llm_tasks_commit_state_check';
--   3. RLS 已启用且 4 条策略存在：
--      SELECT policyname FROM pg_policies
--      WHERE tablename IN ('durable_llm_tasks','durable_llm_task_events');

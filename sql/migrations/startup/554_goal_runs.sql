-- Migration 542: goal_runs + goal_run_steps + goal_run_actions（设计 13 §6.2，Wave 2-A）
--
-- 日期: 2026-08-21
--
-- Purpose
-- ───────
-- 统一自动编排插件 Wave 2-A：GoalRun 持久编排账本存储层
-- （docs/03-design/02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md）。
--   - goal_runs：GoalRun 执行状态与策略快照 SSoT（§6.2），承载 lease/CAS 调度、
--     预算计数、单调 version 与终态 sticky；
--   - goal_run_steps：单调 sequence step 关联（root request -> task -> successor）；
--   - goal_run_actions：可幂等、可重试的编排命令 outbox。
--
-- 关键不变量（§1.1）：
--   - GoalRun 不能成为第二个 durable task owner；
--   - content/tool/side-effect checkpoint 后不得透明 replay；
--   - 终态 sticky；一个 GoalRun 同时只有一个有效 successor（CAS 保证）。
--
-- 上线纪律：本 migration 只合入 schema，显式 Goal request 与 status API
-- 由后续 Wave 2-B/C 实现；在 Goal request activation 完成前不启用自动编排。
--
-- Idempotent: YES (IF NOT EXISTS / DROP POLICY IF EXISTS)
-- Down: 554_goal_runs.down.sql
-- Breaking: NO（纯新增表，不触碰 goal_sessions）

BEGIN;

CREATE TABLE IF NOT EXISTS goal_runs (
    id                            TEXT PRIMARY KEY,
    tenant_id                     TEXT NOT NULL,
    api_key_id                    TEXT NOT NULL,
    
    -- Goal 身份与会话关联（§6.2）
    root_goal_id                  TEXT NOT NULL DEFAULT '',
    root_session_id               TEXT NOT NULL,
    current_session_id            TEXT NOT NULL,
    
    -- Request 链路（root -> last -> durable task）
    root_request_id               TEXT NOT NULL,
    last_request_id               TEXT NOT NULL DEFAULT '',
    last_durable_task_id          TEXT NOT NULL DEFAULT '',
    
    -- 状态机（§6.3）
    status                        TEXT NOT NULL,
    
    -- 固化请求创建时策略快照（§6.1：服务端收紧后的 limits）
    policy_version                INT NOT NULL DEFAULT 1,
    policy_snapshot               JSONB,
    
    -- Instruction 摘要（§6.2：ADR-0001 约束，不扩大明文内容）
    instruction_hash              TEXT NOT NULL,
    redacted_instruction_summary  TEXT NOT NULL DEFAULT '',
    
    -- 预算计数器（§8：平台硬上限优先，租户/客户端仅能收紧）
    turn_count                    INT NOT NULL DEFAULT 0,
    follow_up_count               INT NOT NULL DEFAULT 0,
    retry_count                   INT NOT NULL DEFAULT 0,
    model_switch_count            INT NOT NULL DEFAULT 0,
    handoff_count                 INT NOT NULL DEFAULT 0,
    tokens_used                   BIGINT NOT NULL DEFAULT 0,
    
    -- 完成检测（§6.4：重复/无进度 hash 阈值）
    last_progress_hash            TEXT NOT NULL DEFAULT '',
    
    -- 租约与期限（§8）
    deadline_at                   TIMESTAMPTZ NOT NULL,
    lease_owner                   TEXT NOT NULL DEFAULT '',
    lease_until                   TIMESTAMPTZ,
    
    -- CAS version（§6.2：单调递增，所有更新携带 expected_version）
    version                       BIGINT NOT NULL DEFAULT 1,
    
    -- 终态（§6.3：sticky，cancel/terminal 优先于迟到 worker）
    terminal_reason               TEXT NOT NULL DEFAULT '',
    
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at                  TIMESTAMPTZ,
    
    CONSTRAINT goal_runs_status_check
        CHECK (status IN ('created', 'queued', 'running',
                          'waiting_tool', 'waiting_input', 'waiting_handoff',
                          'auditing', 'restoring',
                          'completed', 'failed', 'canceled', 'expired',
                          'manual_required', 'resume_safety_blocked')),
    CONSTRAINT goal_runs_version_positive
        CHECK (version > 0),
    CONSTRAINT goal_runs_counters_non_negative
        CHECK (turn_count >= 0 AND follow_up_count >= 0 AND retry_count >= 0
               AND model_switch_count >= 0 AND handoff_count >= 0 AND tokens_used >= 0),
    -- 终态必须携带 terminal_reason 与 completed_at（§6.3）
    CONSTRAINT goal_runs_terminal_complete
        CHECK (status NOT IN ('completed', 'failed', 'canceled', 'expired', 'manual_required')
               OR (completed_at IS NOT NULL AND terminal_reason != '')),
    -- 幂等键：同 tenant 的 root_request 不可重复创建 GoalRun（§6.2）
    CONSTRAINT goal_runs_tenant_request_unique
        UNIQUE (tenant_id, root_request_id)
);

COMMENT ON TABLE goal_runs IS
  'Wave 2-A (设计 13 §6.2): GoalRun 持久编排账本。'
  'GoalRun 是编排 projection，不替代 goal_sessions、durable task、handoff confirmation 或 approval。';

COMMENT ON COLUMN goal_runs.version IS
  'CAS version：所有更新必须携带 (lease_owner, expected_version)，'
  '更新 0 行即租约失效或版本冲突（§6.2）';

COMMENT ON COLUMN goal_runs.policy_snapshot IS
  '创建时固化的策略快照（服务端收紧后的 limits），不保存动态 credential（§8）';

-- ════════════════════════════════════════════════════════════════════════
-- 索引（§6.2 查询路径 + lease/deadline reaper）
-- ════════════════════════════════════════════════════════════════════════

-- Status API：按 tenant + run ID 查询
CREATE INDEX IF NOT EXISTS idx_goal_runs_tenant_id
    ON goal_runs (tenant_id, id);

-- Status API：按 tenant + session + request 回源
CREATE INDEX IF NOT EXISTS idx_goal_runs_session_request
    ON goal_runs (tenant_id, current_session_id, last_request_id, updated_at DESC);

-- Scheduler claim：runnable 状态按优先级排序
CREATE INDEX IF NOT EXISTS idx_goal_runs_runnable
    ON goal_runs (status, updated_at)
    WHERE status IN ('queued', 'waiting_tool', 'waiting_input', 'restoring');

-- Lease 过期回收扫描（§6.2）
CREATE INDEX IF NOT EXISTS idx_goal_runs_lease_expiry
    ON goal_runs (lease_until)
    WHERE lease_until IS NOT NULL
      AND status NOT IN ('completed', 'failed', 'canceled', 'expired', 'manual_required');

-- Deadline reaper 扫描（§8）
CREATE INDEX IF NOT EXISTS idx_goal_runs_deadline
    ON goal_runs (deadline_at)
    WHERE status NOT IN ('completed', 'failed', 'canceled', 'expired', 'manual_required');

-- ════════════════════════════════════════════════════════════════════════
-- goal_run_steps：单调 sequence step 关联（§6.2）
-- ════════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS goal_run_steps (
    goal_run_id       TEXT NOT NULL,
    sequence          INT NOT NULL,
    request_id        TEXT NOT NULL,
    parent_request_id TEXT NOT NULL DEFAULT '',
    session_id        TEXT NOT NULL,
    durable_task_id   TEXT NOT NULL DEFAULT '',
    action            TEXT NOT NULL,
    status            TEXT NOT NULL,
    response_hash     TEXT NOT NULL DEFAULT '',
    result_version    BIGINT NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at      TIMESTAMPTZ,
    
    PRIMARY KEY (goal_run_id, sequence),
    
    CONSTRAINT goal_run_steps_action_check
        CHECK (action IN ('continue', 'handoff', 'model_switch', 'audit', 'terminate')),
    CONSTRAINT goal_run_steps_status_check
        CHECK (status IN ('pending', 'running', 'completed', 'failed', 'canceled')),
    CONSTRAINT goal_run_steps_sequence_non_negative
        CHECK (sequence >= 0)
);

COMMENT ON TABLE goal_run_steps IS
  'Wave 2-A (设计 13 §6.2): GoalRun 单调 sequence step 关联。'
  'sequence 单调递增保证 root request -> task -> successor 可追溯。';

CREATE INDEX IF NOT EXISTS idx_goal_run_steps_request
    ON goal_run_steps (request_id, created_at);

-- ════════════════════════════════════════════════════════════════════════
-- goal_run_actions：可幂等、可重试的编排命令 outbox（§6.2）
-- ════════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS goal_run_actions (
    action_id         TEXT PRIMARY KEY,
    goal_run_id       TEXT NOT NULL,
    causation_id      TEXT NOT NULL DEFAULT '',
    action_type       TEXT NOT NULL,
    idempotency_key   TEXT NOT NULL,
    expected_version  BIGINT NOT NULL DEFAULT 0,
    status            TEXT NOT NULL,
    retry_at          TIMESTAMPTZ,
    attempts          INT NOT NULL DEFAULT 0,
    last_error        TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    CONSTRAINT goal_run_actions_action_type_check
        CHECK (action_type IN ('continue', 'handoff', 'model_switch', 'audit', 'terminate')),
    CONSTRAINT goal_run_actions_status_check
        CHECK (status IN ('pending', 'running', 'completed', 'failed', 'canceled')),
    CONSTRAINT goal_run_actions_attempts_non_negative
        CHECK (attempts >= 0),
    -- 幂等键：同 goal_run 的 idempotency_key 不可重复
    CONSTRAINT goal_run_actions_idempotency_unique
        UNIQUE (goal_run_id, idempotency_key)
);

COMMENT ON TABLE goal_run_actions IS
  'Wave 2-A (设计 13 §6.2): GoalRun action outbox。'
  '每个 action 有 idempotency_key、causation_id、expected_version 和 retry 状态。';

CREATE INDEX IF NOT EXISTS idx_goal_run_actions_run_status
    ON goal_run_actions (goal_run_id, status, created_at);

CREATE INDEX IF NOT EXISTS idx_goal_run_actions_retry
    ON goal_run_actions (status, retry_at)
    WHERE status = 'pending' AND retry_at IS NOT NULL;

-- ════════════════════════════════════════════════════════════════════════
-- RLS（设计 13 §6.2：GoalRun 启用 tenant RLS；worker 使用受控服务角色）
-- 跟随 516/511 惯例：app.current_tenant 隔离 + super_admin/bypass。
-- ════════════════════════════════════════════════════════════════════════

ALTER TABLE goal_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE goal_run_steps ENABLE ROW LEVEL SECURITY;
ALTER TABLE goal_run_actions ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS goal_runs_tenant_isolation ON goal_runs;
CREATE POLICY goal_runs_tenant_isolation ON goal_runs
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS goal_runs_super_admin_bypass ON goal_runs;
CREATE POLICY goal_runs_super_admin_bypass ON goal_runs
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS goal_run_steps_tenant_isolation ON goal_run_steps;
CREATE POLICY goal_run_steps_tenant_isolation ON goal_run_steps
    USING (EXISTS (
        SELECT 1 FROM goal_runs
        WHERE goal_runs.id = goal_run_steps.goal_run_id
          AND goal_runs.tenant_id = current_setting('app.current_tenant', true)::TEXT
    ));

DROP POLICY IF EXISTS goal_run_steps_super_admin_bypass ON goal_run_steps;
CREATE POLICY goal_run_steps_super_admin_bypass ON goal_run_steps
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS goal_run_actions_tenant_isolation ON goal_run_actions;
CREATE POLICY goal_run_actions_tenant_isolation ON goal_run_actions
    USING (EXISTS (
        SELECT 1 FROM goal_runs
        WHERE goal_runs.id = goal_run_actions.goal_run_id
          AND goal_runs.tenant_id = current_setting('app.current_tenant', true)::TEXT
    ));

DROP POLICY IF EXISTS goal_run_actions_super_admin_bypass ON goal_run_actions;
CREATE POLICY goal_run_actions_super_admin_bypass ON goal_run_actions
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMIT;

-- POST_CONDITION:
--   1. 三表存在：
--      SELECT tablename FROM pg_tables
--      WHERE tablename IN ('goal_runs','goal_run_steps','goal_run_actions');
--   2. status 与 version 约束生效：
--      SELECT conname FROM pg_constraint
--      WHERE conname IN ('goal_runs_status_check','goal_runs_version_positive');
--   3. 幂等键约束存在：
--      SELECT conname FROM pg_constraint
--      WHERE conname IN ('goal_runs_tenant_request_unique','goal_run_actions_idempotency_unique');
--   4. RLS 已启用且 6 条策略存在：
--      SELECT policyname FROM pg_policies
--      WHERE tablename IN ('goal_runs','goal_run_steps','goal_run_actions');

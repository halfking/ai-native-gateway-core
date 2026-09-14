-- Migration 711: hosted_tasks + hosted_task_events + hosted_task_callbacks
-- （任务托管与移交 P0，docs/design/hosted-task-delegation-design.md §4/§6.1）
--
-- 日期: 2026-09-15
--
-- Purpose
-- ──────
-- 任务托管（Hosted Task Delegation）v2 的网关侧投影与通知台账（§1.2 单写者：
-- 执行真相在 ACC，存储真相在 Memora，通知真相在网关；本组表只是关联投影，
-- 不是第二个执行 owner —— D4）：
--   - hosted_tasks：委托任务投影（hosted_id ↔ acc_command_id/acc_run_id/
--     gw_session），CAS revision 投影 ACC 状态，终态 sticky；
--   - hosted_task_events：append-only 事件时间线，唯一 (task_id, seq)（§4.3）；
--   - hosted_task_callbacks：签名回调投递台账（URL/secret 加密存储，
--     attempt/next_at/status/DLQ 字段；独立于 ASM outbox —— D5/F1）。
--
-- 关键约束（§4.2 状态机）：
--   delegated → dispatching → running → completing → completed | failed
--                                      ↘ needs_review (unknown_outcome)
--   任意非终态 → cancelled | expired(deadline reaper)；终态 sticky。
--
-- 纪律（§0 迁移三处同步）：本文件 + .down 必须同步
-- installer/cmd/llm-gw-installer/embeddata/startup/ 与
-- installer/internal/dbinit/runner.go StartupFiles，并记入 docs/db-changelog.md。
--
-- Idempotent: YES (IF NOT EXISTS / DROP POLICY IF EXISTS)
-- Down: 711_hosted_tasks.down.sql
-- Breaking: NO（纯新增三表，不触碰既有表）

BEGIN;

-- ════════════════════════════════════════════════════════════════════════
-- hosted_tasks：托管任务投影（非执行 owner）
-- ════════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS hosted_tasks (
    id                TEXT PRIMARY KEY,            -- hosted_task_id（ht_<rand>）
    tenant_id         TEXT NOT NULL,
    api_key_id        BIGINT NOT NULL DEFAULT 0,

    -- 委托内容（§4.1 body）
    goal              TEXT NOT NULL,
    done_when         TEXT NOT NULL DEFAULT '',
    context           JSONB NOT NULL DEFAULT '{}'::jsonb,   -- {summary, memora, artifacts}
    environment       JSONB NOT NULL DEFAULT '{}'::jsonb,   -- 已白名单映射后的 workspace/model/timeout

    -- 状态机（§4.2）
    status            TEXT NOT NULL DEFAULT 'delegated',

    -- ACC 关联投影（§1.3 不变量 2：网关只投影，不持有 lease/fencing）
    acc_command_id    TEXT NOT NULL DEFAULT '',
    acc_run_id        TEXT NOT NULL DEFAULT '',
    dispatch_key      TEXT NOT NULL DEFAULT '',    -- gw-hosted-<id>-a1（同键重放防双发）
    dispatch_attempts INT NOT NULL DEFAULT 0,
    last_dispatch_at  TIMESTAMPTZ,

    -- 会话线层关联（§2.4：gw_<hosted_task_id>，正文/成本权威在 request_logs）
    gw_session_id     TEXT NOT NULL DEFAULT '',

    -- SSE 游标（§3.1 ④：Last-Event-ID 持久化，重启续传）
    sse_cursor        TEXT NOT NULL DEFAULT '',

    -- 回调（只存 url_hash 供诊断；明文 URL/secret 加密在 hosted_task_callbacks）
    callback_url_hash TEXT NOT NULL DEFAULT '',

    -- 结果（PG 权威；Redis 仅投影 —— §4.1 result 端点）
    result            JSONB,
    result_version    BIGINT NOT NULL DEFAULT 0,

    -- CAS（§3.1 ④：投影 revision，终态 sticky 抢占）
    revision          BIGINT NOT NULL DEFAULT 1,

    -- 幂等（§4.1：同键同体重放 200，同键异体 409）
    idempotency_key   TEXT NOT NULL,
    request_hash      TEXT NOT NULL DEFAULT '',

    -- 期限
    deadline_at       TIMESTAMPTZ NOT NULL,

    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at      TIMESTAMPTZ,

    CONSTRAINT hosted_tasks_status_check
        CHECK (status IN ('delegated', 'dispatching', 'running', 'completing',
                          'completed', 'failed', 'needs_review',
                          'cancelled', 'expired')),
    CONSTRAINT hosted_tasks_revision_positive CHECK (revision > 0),
    CONSTRAINT hosted_tasks_result_version_non_negative CHECK (result_version >= 0),
    -- 终态 sticky：终态必须携带 completed_at；result_version 单调（≥1 当有 result）
    CONSTRAINT hosted_tasks_terminal_complete
        CHECK (status NOT IN ('completed', 'failed', 'needs_review', 'cancelled', 'expired')
               OR (completed_at IS NOT NULL)),
    CONSTRAINT hosted_tasks_idempotency_unique
        UNIQUE (tenant_id, idempotency_key)
);

COMMENT ON TABLE hosted_tasks IS
  '任务托管 P0 (hosted-task-delegation-design §4/§6.1): 委托任务投影。'
  '执行真相在 ACC，本表只做关联投影（hosted_id ↔ acc_command_id/gw_session），'
  '不是第二个执行 owner（单写者原则 D4）。';

COMMENT ON COLUMN hosted_tasks.dispatch_key IS
  'ACC dispatch 幂等键（gw-hosted-<id>-a1）：重试=同键重放，ACC 幂等保证不双发（§3.1 ②）';

COMMENT ON COLUMN hosted_tasks.sse_cursor IS
  'ACC run 事件流 Last-Event-ID 持久游标：网关重启后断点续传（§3.1 ④/G 组）';

COMMENT ON COLUMN hosted_tasks.revision IS
  'CAS revision：reconciler 投影与终态抢占均须 WHERE revision=$n，0 行即竞争失败（§4.2 sticky）';

-- 查询路径：tenant + id
CREATE INDEX IF NOT EXISTS idx_hosted_tasks_tenant_id
    ON hosted_tasks (tenant_id, id);

-- dispatch 扫描：delegated/dispatching 按 deadline 排序
CREATE INDEX IF NOT EXISTS idx_hosted_tasks_dispatch_scan
    ON hosted_tasks (deadline_at)
    WHERE status IN ('delegated', 'dispatching');

-- deadline reaper 扫描（§3.1 ④）
CREATE INDEX IF NOT EXISTS idx_hosted_tasks_deadline
    ON hosted_tasks (deadline_at)
    WHERE status NOT IN ('completed', 'failed', 'needs_review', 'cancelled', 'expired');

-- 轮询兜底/SSE 订阅扫描：非终态且有 acc_run_id
CREATE INDEX IF NOT EXISTS idx_hosted_tasks_active_runs
    ON hosted_tasks (acc_run_id)
    WHERE acc_run_id != ''
      AND status NOT IN ('completed', 'failed', 'needs_review', 'cancelled', 'expired');

-- 回调入队扫描：终态后回调未完成
CREATE INDEX IF NOT EXISTS idx_hosted_tasks_callback_pending
    ON hosted_tasks (completed_at)
    WHERE completed_at IS NOT NULL
      AND status IN ('completed', 'failed', 'needs_review', 'cancelled', 'expired');

-- ════════════════════════════════════════════════════════════════════════
-- hosted_task_events：append-only 事件时间线（§4.3，唯一 (task_id, seq)）
-- ════════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS hosted_task_events (
    task_id    TEXT NOT NULL REFERENCES hosted_tasks(id) ON DELETE CASCADE,
    seq        BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    payload    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (task_id, seq),

    CONSTRAINT hosted_task_events_type_check
        CHECK (event_type IN ('accepted', 'dispatch_degraded', 'running', 'progress',
                              'cancel_requested', 'completed', 'failed', 'expired',
                              'cancelled', 'callback_delivered', 'callback_dlq'))
);

COMMENT ON TABLE hosted_task_events IS
  '任务托管 P0 (§4.3): append-only 事件时间线。accepted/dispatch_degraded/running/'
  'progress/cancel_requested/completed/failed/expired/cancelled/callback_delivered/'
  'callback_dlq（P1 增 budget_exceeded/recalled/phase_changed）。';

-- ════════════════════════════════════════════════════════════════════════
-- hosted_task_callbacks：签名回调投递台账（D5：独立于 ASM outbox）
-- ════════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS hosted_task_callbacks (
    task_id         TEXT PRIMARY KEY REFERENCES hosted_tasks(id) ON DELETE CASCADE,
    url_enc         TEXT NOT NULL DEFAULT '',   -- AES-GCM 加密回调 URL（secret 包 keyring）
    secret_enc      TEXT NOT NULL DEFAULT '',   -- AES-GCM 加密 HMAC signing secret
    event_id        TEXT NOT NULL DEFAULT '',   -- hosted_<id>_ev<seq> 固定（接收方幂等键）
    event_seq       BIGINT NOT NULL DEFAULT 0,  -- 终态事件 seq（重投时重建投递体）
    status          TEXT NOT NULL DEFAULT 'idle',
    attempts        INT NOT NULL DEFAULT 0,
    max_attempts    INT NOT NULL DEFAULT 8,
    next_attempt_at TIMESTAMPTZ,
    last_status_code INT,
    last_error      TEXT NOT NULL DEFAULT '',
    delivered_at    TIMESTAMPTZ,
    dlq_at          TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT hosted_task_callbacks_status_check
        CHECK (status IN ('idle', 'pending', 'delivered', 'dlq')),
    CONSTRAINT hosted_task_callbacks_attempts_non_negative CHECK (attempts >= 0)
);

COMMENT ON TABLE hosted_task_callbacks IS
  '任务托管 P0 (§3.1 ⑤/D5): 签名回调投递台账。URL/secret 经 secret.Keyring AES-GCM '
  '加密落盘；2xx=delivered；4xx 不重试直接 DLQ；5xx/网络退避重试后 DLQ。';

-- deliverer 认领扫描
CREATE INDEX IF NOT EXISTS idx_hosted_task_callbacks_claim
    ON hosted_task_callbacks (status, next_attempt_at)
    WHERE status = 'pending';

-- ════════════════════════════════════════════════════════════════════════
-- RLS（跟随 554 惯例：app.current_tenant 隔离 + super_admin/bypass_rls 放行；
-- bypass 仅 worker/服务角色路径 —— §6.1）
-- ════════════════════════════════════════════════════════════════════════

ALTER TABLE hosted_tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_task_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_task_callbacks ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS hosted_tasks_tenant_isolation ON hosted_tasks;
CREATE POLICY hosted_tasks_tenant_isolation ON hosted_tasks
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS hosted_tasks_super_admin_bypass ON hosted_tasks;
CREATE POLICY hosted_tasks_super_admin_bypass ON hosted_tasks
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS hosted_task_events_tenant_isolation ON hosted_task_events;
CREATE POLICY hosted_task_events_tenant_isolation ON hosted_task_events
    USING (EXISTS (
        SELECT 1 FROM hosted_tasks
        WHERE hosted_tasks.id = hosted_task_events.task_id
          AND hosted_tasks.tenant_id = current_setting('app.current_tenant', true)::TEXT
    ));

DROP POLICY IF EXISTS hosted_task_events_super_admin_bypass ON hosted_task_events;
CREATE POLICY hosted_task_events_super_admin_bypass ON hosted_task_events
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS hosted_task_callbacks_tenant_isolation ON hosted_task_callbacks;
CREATE POLICY hosted_task_callbacks_tenant_isolation ON hosted_task_callbacks
    USING (EXISTS (
        SELECT 1 FROM hosted_tasks
        WHERE hosted_tasks.id = hosted_task_callbacks.task_id
          AND hosted_tasks.tenant_id = current_setting('app.current_tenant', true)::TEXT
    ));

DROP POLICY IF EXISTS hosted_task_callbacks_super_admin_bypass ON hosted_task_callbacks;
CREATE POLICY hosted_task_callbacks_super_admin_bypass ON hosted_task_callbacks
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMIT;

-- POST_CONDITION:
--   1. 三表存在：
--      SELECT tablename FROM pg_tables
--      WHERE tablename IN ('hosted_tasks','hosted_task_events','hosted_task_callbacks');
--   2. 状态机与幂等约束生效：
--      SELECT conname FROM pg_constraint
--      WHERE conname IN ('hosted_tasks_status_check','hosted_tasks_idempotency_unique',
--                        'hosted_task_events_type_check','hosted_task_callbacks_status_check');
--   3. RLS 已启用且 6 条策略存在：
--      SELECT policyname FROM pg_policies
--      WHERE tablename IN ('hosted_tasks','hosted_task_events','hosted_task_callbacks');

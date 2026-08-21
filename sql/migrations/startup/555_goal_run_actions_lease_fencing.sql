-- Migration 555: goal_run_actions 增补 lease + fencing 列（设计 13 §6.2，Wave 3-A）
--
-- 日期: 2026-08-22
--
-- Purpose
-- ───────
-- 统一自动编排插件 Wave 3-A：Durable Continuation Scheduler
-- （docs/03-design/02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md）。
--
-- 复用 durable.ClaimRunnable 的 (lease_owner, lease_until, fencing_token) 模式，
-- 让多个 scheduler worker 可并发扫描、互斥执行 action。
--   - action_id（已是 PK）+ CAS 条件保证互斥；
--   - lease + fencing 防过期 worker 的迟到写入；
--   - 索引覆盖 scheduler claim 路径：status='pending' AND retry_at <= NOW()。
--
-- 上线纪律：本 migration 必须先于 goalrun_scheduler 激活部署；migration 554
-- 仍为 GoalRun 账本基线，本 migration 只追加列/索引，不修改既有约束。
--
-- Idempotent: YES (ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS)
-- Down: 555_goal_run_actions_lease_fencing.down.sql
-- Breaking: NO（纯追加可空列 + 新索引）

BEGIN;

-- ════════════════════════════════════════════════════════════════════════
-- goal_run_actions：增补 lease + fencing 列
-- ════════════════════════════════════════════════════════════════════════

ALTER TABLE goal_run_actions
    ADD COLUMN IF NOT EXISTS lease_owner    TEXT         NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS lease_until    TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fencing_token  BIGINT       NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS claimed_at     TIMESTAMPTZ;

COMMENT ON COLUMN goal_run_actions.lease_owner IS
    '当前持有本 action 租约的 worker 标识（gateway instance id）。'
    '空串表示无 lease。claim 时与 fencing_token 一起作为 CAS 门禁。';

COMMENT ON COLUMN goal_run_actions.lease_until IS
    '租约到期时间。worker 必须在 lease_until 前续租或完成，'
    '过期 lease 可被其他 scheduler worker 抢占。';

COMMENT ON COLUMN goal_run_actions.fencing_token IS
    '单调递增 fencing token；每次成功 claim 时 +1。'
    '旧 worker 即使 lease_owner 错配也无法写入（0 rows → ErrLeaseLost）。';

COMMENT ON COLUMN goal_run_actions.claimed_at IS
    '最近一次 claim 成功的时刻；与 attempts 协同用于审计/可观测。';

-- ════════════════════════════════════════════════════════════════════════
-- 索引：scheduler claim 路径（status='pending' AND retry_at <= NOW）
-- ════════════════════════════════════════════════════════════════════════

-- 替代现有 idx_goal_run_actions_retry：该索引已覆盖 (status, retry_at)，
-- 现升级为 partial index + lease expiry 复合，覆盖「pending + retry_at 过期 + lease 空闲」
-- 三条件扫描路径。
DROP INDEX IF EXISTS idx_goal_run_actions_retry;
CREATE INDEX IF NOT EXISTS idx_goal_run_actions_schedulable
    ON goal_run_actions (retry_at, action_id)
    WHERE status = 'pending';

-- Lease 过期回收扫描：status='running' AND lease_until < NOW()
-- 当 worker 崩溃未释放时由 reaper 抢占。
CREATE INDEX IF NOT EXISTS idx_goal_run_actions_lease_expiry
    ON goal_run_actions (lease_until)
    WHERE status = 'running' AND lease_until IS NOT NULL;

COMMIT;

-- POST_CONDITION:
--   1. 新增列存在：
--      SELECT column_name FROM information_schema.columns
--      WHERE table_name = 'goal_run_actions'
--        AND column_name IN ('lease_owner','lease_until','fencing_token','claimed_at');
--      -- 预期: 4 行
--   2. claim 路径索引存在：
--      SELECT indexname FROM pg_indexes
--      WHERE tablename = 'goal_run_actions'
--        AND indexname IN ('idx_goal_run_actions_schedulable',
--                          'idx_goal_run_actions_lease_expiry');
--      -- 预期: 2 行

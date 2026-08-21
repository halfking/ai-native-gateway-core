-- Migration 555 DOWN: 回退 goal_run_actions 的 lease + fencing 列
--
-- 日期: 2026-08-22

BEGIN;

DROP INDEX IF EXISTS idx_goal_run_actions_lease_expiry;
DROP INDEX IF EXISTS idx_goal_run_actions_schedulable;

-- 重建原 idx_goal_run_actions_retry 索引
CREATE INDEX IF NOT EXISTS idx_goal_run_actions_retry
    ON goal_run_actions (status, retry_at)
    WHERE status = 'pending' AND retry_at IS NOT NULL;

ALTER TABLE goal_run_actions
    DROP COLUMN IF EXISTS claimed_at,
    DROP COLUMN IF EXISTS fencing_token,
    DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS lease_owner;

COMMIT;

-- POST_CONDITION:
--   SELECT column_name FROM information_schema.columns
--   WHERE table_name = 'goal_run_actions'
--     AND column_name IN ('lease_owner','lease_until','fencing_token','claimed_at');
--   -- 预期: 0 行

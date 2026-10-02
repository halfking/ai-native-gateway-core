-- ===========================================================================
-- File:          sql/migrations/startup/818_ursm_snapshot_typed_columns.down.sql
-- Migration:     818 (down)
-- Database:      llm_gateway
-- Purpose:       回滚 818 —— 丢弃 24 个由 payload 提升出来的 typed 列。
--
-- **数据不可逆**：down 之后这 24 列的值随之消失。本迁移刻意**没有**回填
-- 历史行（见 up 头注释），所以：
--   - 若 down 在**新二进制写入之前**执行 ⇒ 零数据损失（列本来就全 NULL）；
--   - 若 down 在**新二进制写入之后**执行 ⇒ 丢失这段时间内新采集的
--     last_err / manual_* / *_count / *_ms 等字段，**但这批数据在
--     payload 里仍然存在**（writer 只剔除 7 个重复键，这 24 个键
--     仍在 payload 的前向兼容仓中）⇒ 可用
--       SELECT payload->>'last_err' FROM ursm_node_snapshot_min ...
--     完整取回，无需人工补数。
--
-- payload 本身不被触碰，历史行的全量 payload 也不受影响。
-- ===========================================================================
BEGIN;

ALTER TABLE public.ursm_node_snapshot_min
  DROP COLUMN IF EXISTS updated_at_ms,
  DROP COLUMN IF EXISTS last_probe_at_ms,
  DROP COLUMN IF EXISTS last_probe_latency_ms,
  DROP COLUMN IF EXISTS last_attempt_ms,
  DROP COLUMN IF EXISTS last_ok_ms,
  DROP COLUMN IF EXISTS last_request_at_ms,
  DROP COLUMN IF EXISTS last_request_error_at_ms,
  DROP COLUMN IF EXISTS manual_at_ms,
  DROP COLUMN IF EXISTS cool_until_ms,
  DROP COLUMN IF EXISTS event_seq,
  DROP COLUMN IF EXISTS disabled,
  DROP COLUMN IF EXISTS last_direct_ok,
  DROP COLUMN IF EXISTS manual_hold,
  DROP COLUMN IF EXISTS success_count,
  DROP COLUMN IF EXISTS failure_count,
  DROP COLUMN IF EXISTS disable_count,
  DROP COLUMN IF EXISTS lat_ewma_ms,
  DROP COLUMN IF EXISTS empty_response_rate_1m,
  DROP COLUMN IF EXISTS empty_response_rate_30m,
  DROP COLUMN IF EXISTS last_err,
  DROP COLUMN IF EXISTS manual_reason,
  DROP COLUMN IF EXISTS manual_actor,
  DROP COLUMN IF EXISTS disabled_reason,
  DROP COLUMN IF EXISTS cool_reason;

-- 恢复 payload 注释的原始语义
COMMENT ON COLUMN public.ursm_node_snapshot_min.payload IS NULL;

COMMIT;

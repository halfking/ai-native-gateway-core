-- Migration 515: idempotent replay for request_state_transitions (OBS-BE7)
--
-- 日期: 2026-08-15
--
-- Purpose
-- ───────
-- V3.3 OBS-BE7 为状态变更旁路写入补齐"失败重放"：写失败的行进入内存
-- 重试队列重放。重放 INSERT 必须幂等，依赖 (request_id, seq) 唯一约束 +
-- ON CONFLICT DO NOTHING（migration 511 建表时没有 seq 列也没有唯一
-- 约束，本迁移补充）。
--
--   - seq: 进程内全局单调计数器分配，入队时一次性固定；同一行重试/重放
--     时 (request_id, seq) 不变 → 重放不产生重复行。
--   - request_id 不跨进程重启存活（请求不跨越进程生命周期），计数器
--     重启归零不会与历史行冲突。
--   - 存量行为 seq = NULL；Postgres 唯一索引视 NULL 为互不相等，不受
--     本索引影响。新写入（Go 侧）总是携带非 NULL seq。
--
-- 关联: 511_state_transitions_table.sql（建表 + RLS）
-- 写入方: domains/dispatch/state_transition_logger.go
--
-- Idempotent: YES (IF NOT EXISTS)
-- Down: 515_state_transitions_seq_unique.down.sql
-- Breaking: NO (纯新增列 + 索引)

BEGIN;

ALTER TABLE request_state_transitions
    ADD COLUMN IF NOT EXISTS seq BIGINT;

COMMENT ON COLUMN request_state_transitions.seq IS
  'V3.3 (2026-08-15): 进程内单调序号，与 request_id 组成唯一键，'
  '供旁路写入失败重放时 ON CONFLICT DO NOTHING 幂等去重（OBS-BE7）。'
  '存量行为 NULL；NULL 互不相等，不参与唯一约束冲突。';

CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_request_seq
    ON request_state_transitions (request_id, seq);

COMMIT;

-- POST_CONDITION:
--   1. seq 列存在：
--      SELECT column_name FROM information_schema.columns
--      WHERE table_name = 'request_state_transitions' AND column_name = 'seq';
--   2. 唯一索引存在：
--      SELECT indexname, indexdef FROM pg_indexes
--      WHERE tablename = 'request_state_transitions'
--        AND indexname = 'uq_state_transitions_request_seq';
--      -- 预期: CREATE UNIQUE INDEX ... ON request_state_transitions (request_id, seq)
--   3. 幂等重放不产生重复行：
--      INSERT INTO request_state_transitions (request_id, tenant_id, transition_type, seq)
--      VALUES ('probe', 'admin', 'state', 1);
--      INSERT INTO request_state_transitions (request_id, tenant_id, transition_type, seq)
--      VALUES ('probe', 'admin', 'state', 1) ON CONFLICT (request_id, seq) DO NOTHING;
--      SELECT count(*) FROM request_state_transitions WHERE request_id = 'probe';
--      -- 预期: 1

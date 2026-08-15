-- Migration 515 (down): drop seq column + unique index
--
-- Used by `bash scripts/sql-rollback.sh 515` (rule 38 §3).
-- Safe: 纯新增列 + 索引，无外部依赖。回滚后旁路写入将退化为非幂等
-- （重放可能产生重复行），需同步回滚 Go 侧 OBS-BE7 改动。

BEGIN;

DROP INDEX IF EXISTS uq_state_transitions_request_seq;

ALTER TABLE request_state_transitions
    DROP COLUMN IF EXISTS seq;

COMMIT;

-- Migration 511 (down): drop request_state_transitions table
--
-- Used by `bash scripts/sql-rollback.sh 511` (rule 38 §3).
-- Safe: 纯新增表，无外部依赖。

BEGIN;

DROP TABLE IF EXISTS request_state_transitions;

COMMIT;

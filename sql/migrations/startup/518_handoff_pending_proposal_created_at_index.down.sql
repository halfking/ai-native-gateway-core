-- Down migration for 518_handoff_pending_proposal_created_at_index.sql
-- 该索引仅加速 TTL DELETE，删除后只会让 HandoffPendingTrimmer 退化到
-- 全表扫描，不会破坏任何业务功能。
BEGIN;

DROP INDEX IF EXISTS idx_handoff_pending_proposal_created_at;

COMMIT;

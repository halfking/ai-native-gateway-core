-- Migration 518: index on handoff_pending_confirmations.proposal_created_at
--
-- 日期: 2026-08-16
--
-- Purpose
-- ───────
-- HandoffPendingTrimmer.TrimOnce (bg/handoff_pending_trimmer.go) issues
--   DELETE FROM handoff_pending_confirmations
--   WHERE id IN (
--     SELECT id FROM handoff_pending_confirmations
--     WHERE proposal_created_at < NOW() - $1::interval
--     ORDER BY proposal_created_at
--     LIMIT 5000
--   )
-- on every tick (default 1 minute). Migration 517 created the table with
-- indexes on (tenant_id, id), (tenant_id, previous_session_id) WHERE
-- status='pending', and (expires_at) WHERE status='pending' — but NOT on
-- proposal_created_at. Without this index the DELETE falls back to a
-- sequential scan + sort, which becomes expensive once the table holds
-- weeks of confirmed/expired proposals.
--
-- Idempotent: YES (CREATE INDEX IF NOT EXISTS)
-- Down: 518_handoff_pending_proposal_created_at_index.down.sql
-- Breaking: NO（纯新增索引，不改 schema / 不动数据）

CREATE INDEX IF NOT EXISTS idx_handoff_pending_proposal_created_at
    ON handoff_pending_confirmations (proposal_created_at);

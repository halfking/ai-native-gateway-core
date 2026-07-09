-- Migration 359 Down: Revert session_intent_evolution table creation
-- Date: 2026-07-09

-- 删除索引
DROP INDEX IF EXISTS idx_session_intent_content_hash;
DROP INDEX IF EXISTS idx_session_intent_primary;
DROP INDEX IF EXISTS idx_session_intent_changed;
DROP INDEX IF EXISTS idx_session_intent_tenant_session;

-- 删除表
DROP TABLE IF EXISTS session_intent_evolution CASCADE;

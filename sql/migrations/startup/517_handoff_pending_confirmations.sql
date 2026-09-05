-- Migration 517: handoff_pending_confirmations
--
-- 日期: 2026-08-16
-- Purpose: 持久化显式 handoff 客户端确认能力，支持 token、幂等、过期和审计关联。
-- Idempotent: YES (IF NOT EXISTS / CREATE INDEX IF NOT EXISTS)
-- Down: 517_handoff_pending_confirmations.down.sql
-- Breaking: NO
-- POST_CONDITION: SELECT 1 FROM information_schema.tables WHERE table_name = 'handoff_pending_confirmations'

BEGIN;

CREATE TABLE IF NOT EXISTS handoff_pending_confirmations (
    id UUID PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    api_key_id BIGINT NOT NULL,
    previous_session_id VARCHAR(255) NOT NULL,
    token_hash CHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'confirmed', 'expired')),
    expires_at TIMESTAMPTZ NOT NULL,
    confirmed_at TIMESTAMPTZ,
    new_session_id VARCHAR(255),
    idempotency_hash CHAR(64),
    handoff_log_id INTEGER REFERENCES handoff_logs(id) ON DELETE SET NULL,
    trigger_mode VARCHAR(32) NOT NULL,
    trigger_reason VARCHAR(64) NOT NULL,
    tokens_at_trigger INTEGER NOT NULL,
    context_window INTEGER,
    messages_at_trigger INTEGER NOT NULL,
    tokens_in_session INTEGER NOT NULL,
    summary_engine VARCHAR(32),
    summary_text TEXT,
    handoff_prompt TEXT,
    skill_name VARCHAR(64),
    duration_ms INTEGER,
    proposal_created_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_handoff_pending_active_session
    ON handoff_pending_confirmations (tenant_id, previous_session_id)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_handoff_pending_tenant_id
    ON handoff_pending_confirmations (tenant_id, id);
CREATE INDEX IF NOT EXISTS idx_handoff_pending_expiry
    ON handoff_pending_confirmations (expires_at)
    WHERE status = 'pending';

COMMIT;

BEGIN;

-- Migration 354: handoff schema fix (supersedes broken 352)
-- 2026-07-06: Migration 352 referenced a 'sessions' master table that does not
-- exist in any branch (sessions are tracked via session_summaries + Redis).
-- This migration does the minimal safe equivalents using the actual schema.

-- 1. Add handoff tracking columns to session_summaries (canonical session store)
ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS handoff_count INT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_handoff_at TIMESTAMP;

CREATE INDEX IF NOT EXISTS idx_session_summaries_handoff
    ON session_summaries(last_handoff_at)
    WHERE handoff_count > 0;

-- 2. Create handoff_logs table (the only NEW artifact migration 352 introduced)
-- 352 tried to create this alongside the broken ALTER; we keep only this part.
CREATE TABLE IF NOT EXISTS handoff_logs (
    id SERIAL PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    trigger_reason VARCHAR(64) NOT NULL,
    tokens_at_handoff INT NOT NULL,
    context_window INT,
    handoff_prompt TEXT,
    new_session_id VARCHAR(64),
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_handoff_logs_session
    ON handoff_logs(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_handoff_logs_tenant
    ON handoff_logs(tenant_id, created_at DESC);

COMMIT;

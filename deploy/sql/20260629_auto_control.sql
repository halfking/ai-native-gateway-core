BEGIN;

-- Extend sessions table with handoff and goal tracking
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS handoff_count INT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_handoff_at TIMESTAMP,
    ADD COLUMN IF NOT EXISTS goal_mode_enabled BOOLEAN DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS total_tokens_used INT DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_sessions_goal_mode ON sessions(goal_mode_enabled, last_activity_at)
    WHERE goal_mode_enabled = true;

-- Handoff logs table
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

CREATE INDEX IF NOT EXISTS idx_handoff_logs_session ON handoff_logs(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_handoff_logs_tenant ON handoff_logs(tenant_id, created_at DESC);

-- Goal sessions table
CREATE TABLE IF NOT EXISTS goal_sessions (
    id SERIAL PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    state VARCHAR(32) NOT NULL DEFAULT 'active',
    original_goal TEXT NOT NULL,
    retry_count INT DEFAULT 0,
    decision_count INT DEFAULT 0,
    auto_continue_count INT DEFAULT 0,
    last_activity_at TIMESTAMP DEFAULT NOW(),
    completed_at TIMESTAMP,
    audit_result JSONB,
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(session_id)
);

CREATE INDEX IF NOT EXISTS idx_goal_sessions_state ON goal_sessions(state, last_activity_at);
CREATE INDEX IF NOT EXISTS idx_goal_sessions_tenant ON goal_sessions(tenant_id, state);
CREATE INDEX IF NOT EXISTS idx_goal_sessions_session ON goal_sessions(session_id);

COMMIT;

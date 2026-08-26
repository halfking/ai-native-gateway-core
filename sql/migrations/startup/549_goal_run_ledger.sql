-- Migration 549: durable GoalRun coordination ledger (Wave 2-A).
-- GoalRun is an orchestration projection; it does not own durable task execution.
BEGIN;

CREATE TABLE IF NOT EXISTS goal_runs (
    goal_run_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id TEXT NOT NULL,
    api_key_id BIGINT NOT NULL DEFAULT 0,
    root_goal_id TEXT NOT NULL DEFAULT '',
    root_session_id TEXT NOT NULL,
    current_session_id TEXT NOT NULL,
    root_request_id TEXT NOT NULL,
    last_request_id TEXT NOT NULL,
    last_durable_task_id UUID,
    root_idempotency_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'created',
    policy_version TEXT NOT NULL,
    policy_snapshot JSONB NOT NULL,
    policy_hash CHAR(64) NOT NULL,
    instruction_hash CHAR(64) NOT NULL,
    redacted_instruction_summary TEXT NOT NULL DEFAULT '',
    turn_count INT NOT NULL DEFAULT 0,
    follow_up_count INT NOT NULL DEFAULT 0,
    retry_count INT NOT NULL DEFAULT 0,
    model_switch_count INT NOT NULL DEFAULT 0,
    handoff_count INT NOT NULL DEFAULT 0,
    tokens_used BIGINT NOT NULL DEFAULT 0,
    last_progress_hash CHAR(64),
    deadline_at TIMESTAMPTZ NOT NULL,
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 0,
    terminal_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT goal_runs_status_check CHECK (status IN (
        'created','queued','running','waiting_tool','waiting_input',
        'waiting_handoff','auditing','restoring','completed','failed',
        'canceled','expired','manual_required','resume_safety_blocked')),
    CONSTRAINT goal_runs_counters_check CHECK (
        turn_count >= 0 AND follow_up_count >= 0 AND retry_count >= 0 AND
        model_switch_count >= 0 AND handoff_count >= 0 AND tokens_used >= 0),
    CONSTRAINT goal_runs_version_check CHECK (version >= 0 AND fencing_token >= 0),
    CONSTRAINT goal_runs_terminal_check CHECK (
        status NOT IN ('completed','failed','canceled','expired','manual_required','resume_safety_blocked')
        OR completed_at IS NOT NULL),
    CONSTRAINT goal_runs_root_idempotency_unique UNIQUE (tenant_id, root_idempotency_key)
);

CREATE TABLE IF NOT EXISTS goal_run_steps (
    goal_run_id UUID NOT NULL REFERENCES goal_runs(goal_run_id) ON DELETE CASCADE,
    tenant_id TEXT NOT NULL,
    sequence BIGINT NOT NULL,
    request_id TEXT NOT NULL,
    parent_request_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL,
    durable_task_id UUID,
    action TEXT NOT NULL,
    status TEXT NOT NULL,
    response_hash CHAR(64),
    result_version BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (goal_run_id, sequence),
    CONSTRAINT goal_run_steps_sequence_check CHECK (sequence >= 0),
    CONSTRAINT goal_run_steps_status_check CHECK (status IN (
        'created','queued','running','waiting_tool','waiting_input',
        'waiting_handoff','auditing','restoring','completed','failed',
        'canceled','expired','manual_required','resume_safety_blocked')),
    CONSTRAINT goal_run_steps_tenant_fk CHECK (tenant_id <> '')
);

CREATE TABLE IF NOT EXISTS goal_run_actions (
    action_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_run_id UUID NOT NULL REFERENCES goal_runs(goal_run_id) ON DELETE CASCADE,
    tenant_id TEXT NOT NULL,
    step_sequence BIGINT NOT NULL,
    causation_id TEXT NOT NULL,
    action_type TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    intent_hash CHAR(64) NOT NULL,
    expected_version BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    retry_at TIMESTAMPTZ,
    attempts INT NOT NULL DEFAULT 0,
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT goal_run_actions_unique_idempotency UNIQUE (tenant_id, goal_run_id, idempotency_key),
    CONSTRAINT goal_run_actions_status_check CHECK (status IN ('pending','claimed','succeeded','failed','canceled')),
    CONSTRAINT goal_run_actions_counts_check CHECK (step_sequence >= 0 AND expected_version >= 0 AND attempts >= 0 AND fencing_token >= 0)
);

CREATE TABLE IF NOT EXISTS goal_run_action_outbox (
    outbox_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    action_id UUID NOT NULL REFERENCES goal_run_actions(action_id) ON DELETE CASCADE,
    goal_run_id UUID NOT NULL REFERENCES goal_runs(goal_run_id) ON DELETE CASCADE,
    tenant_id TEXT NOT NULL,
    payload_hash CHAR(64) NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claim_owner TEXT,
    claim_until TIMESTAMPTZ,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT goal_run_action_outbox_action_unique UNIQUE (action_id),
    CONSTRAINT goal_run_action_outbox_status_check CHECK (status IN ('pending','claimed','delivered','failed')),
    CONSTRAINT goal_run_action_outbox_counts_check CHECK (attempts >= 0 AND fencing_token >= 0)
);

CREATE INDEX IF NOT EXISTS idx_goal_runs_tenant_status ON goal_runs (tenant_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_goal_runs_lease ON goal_runs (lease_until)
    WHERE lease_until IS NOT NULL AND status NOT IN ('completed','failed','canceled','expired','manual_required','resume_safety_blocked');
CREATE INDEX IF NOT EXISTS idx_goal_runs_deadline ON goal_runs (deadline_at)
    WHERE status NOT IN ('completed','failed','canceled','expired','manual_required','resume_safety_blocked');
CREATE INDEX IF NOT EXISTS idx_goal_run_steps_tenant_request ON goal_run_steps (tenant_id, request_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_goal_run_actions_retry ON goal_run_actions (tenant_id, status, retry_at);
CREATE INDEX IF NOT EXISTS idx_goal_run_actions_lease ON goal_run_actions (lease_until)
    WHERE lease_until IS NOT NULL AND status = 'claimed';
CREATE INDEX IF NOT EXISTS idx_goal_run_outbox_retry ON goal_run_action_outbox (tenant_id, status, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_goal_run_outbox_lease ON goal_run_action_outbox (claim_until)
    WHERE claim_until IS NOT NULL AND status = 'claimed';

ALTER TABLE goal_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE goal_run_steps ENABLE ROW LEVEL SECURITY;
ALTER TABLE goal_run_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE goal_run_action_outbox ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS goal_runs_tenant_isolation ON goal_runs;
CREATE POLICY goal_runs_tenant_isolation ON goal_runs
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS goal_runs_super_admin_bypass ON goal_runs;
CREATE POLICY goal_runs_super_admin_bypass ON goal_runs
    USING (current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS goal_run_steps_tenant_isolation ON goal_run_steps;
CREATE POLICY goal_run_steps_tenant_isolation ON goal_run_steps
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS goal_run_steps_super_admin_bypass ON goal_run_steps;
CREATE POLICY goal_run_steps_super_admin_bypass ON goal_run_steps
    USING (current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS goal_run_actions_tenant_isolation ON goal_run_actions;
CREATE POLICY goal_run_actions_tenant_isolation ON goal_run_actions
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS goal_run_actions_super_admin_bypass ON goal_run_actions;
CREATE POLICY goal_run_actions_super_admin_bypass ON goal_run_actions
    USING (current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS goal_run_action_outbox_tenant_isolation ON goal_run_action_outbox;
CREATE POLICY goal_run_action_outbox_tenant_isolation ON goal_run_action_outbox
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS goal_run_action_outbox_super_admin_bypass ON goal_run_action_outbox;
CREATE POLICY goal_run_action_outbox_super_admin_bypass ON goal_run_action_outbox
    USING (current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true');

COMMIT;

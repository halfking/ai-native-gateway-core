-- Migration 520: durable task settlement intents
-- A crash-safe write-ahead record for terminal settlement.
BEGIN;

CREATE TABLE IF NOT EXISTS durable_task_settlement_intents (
    task_id              UUID PRIMARY KEY REFERENCES durable_llm_tasks(id) ON DELETE CASCADE,
    tenant_id            TEXT NOT NULL,
    request_id           TEXT NOT NULL,
    session_id           TEXT NOT NULL,
    request_hash         TEXT NOT NULL,
    source_lease_owner   TEXT NOT NULL,
    source_fencing_token BIGINT NOT NULL,
    outcome              TEXT NOT NULL,
    result_ciphertext    TEXT,
    encryption_key_id    TEXT,
    content_type         TEXT NOT NULL DEFAULT '',
    reason_code          TEXT NOT NULL DEFAULT '',
    error_kind           TEXT NOT NULL DEFAULT '',
    attempt              INT NOT NULL DEFAULT 0,
    result_hash          TEXT NOT NULL,

    -- Independent outbox lease/fencing. The source task lease is immutable.
    claim_owner          TEXT,
    claim_until          TIMESTAMPTZ,
    claim_fencing_token  BIGINT NOT NULL DEFAULT 0,
    attempts             INT NOT NULL DEFAULT 0,
    next_attempt_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error           TEXT NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT durable_settlement_outcome_check
        CHECK (outcome IN ('completed', 'failed', 'expired', 'canceled')),
    CONSTRAINT durable_settlement_fencing_non_negative
        CHECK (source_fencing_token >= 0 AND claim_fencing_token >= 0),
    CONSTRAINT durable_settlement_attempt_non_negative
        CHECK (attempt >= 0 AND attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_durable_settlement_claim
    ON durable_task_settlement_intents (next_attempt_at, claim_until, created_at);

ALTER TABLE durable_task_settlement_intents ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS durable_task_settlement_tenant_isolation ON durable_task_settlement_intents;
DROP POLICY IF EXISTS durable_task_settlement_super_admin_bypass ON durable_task_settlement_intents;
CREATE POLICY durable_task_settlement_access ON durable_task_settlement_intents
    USING (
        tenant_id = current_setting('app.current_tenant', true)::TEXT
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    )
    WITH CHECK (
        tenant_id = current_setting('app.current_tenant', true)::TEXT
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );

COMMIT;

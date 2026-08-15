-- Migration 516: durable LLM task schema + survival transition dimensions
-- M3 SR-W3 Wave 2. request_state_transitions remains the single event stream.
-- durable_llm_tasks.id is repository-supplied UUID with no sequence/default.

BEGIN;

CREATE TABLE IF NOT EXISTS durable_llm_tasks (
    id                          UUID PRIMARY KEY,
    tenant_id                   TEXT NOT NULL,
    request_id                  TEXT NOT NULL,
    session_id                  TEXT NOT NULL,
    protocol                    TEXT NOT NULL,
    endpoint                    TEXT NOT NULL,
    request_snapshot_ciphertext TEXT NOT NULL,
    snapshot_version            INTEGER NOT NULL,
    encryption_key_id           TEXT NOT NULL,
    request_hash                TEXT NOT NULL,
    status                      TEXT NOT NULL DEFAULT 'accepted',
    error_kind                  TEXT,
    reason_code                 TEXT,
    attempt_count               INTEGER NOT NULL DEFAULT 0,
    next_retry_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deadline_at                 TIMESTAMPTZ NOT NULL,
    lease_owner                 TEXT,
    lease_until                 TIMESTAMPTZ,
    fencing_token               BIGINT NOT NULL DEFAULT 0,
    semantic_content_committed  BOOLEAN NOT NULL DEFAULT FALSE,
    commit_state                TEXT NOT NULL DEFAULT 'none',
    commit_metadata             JSONB NOT NULL DEFAULT '{}'::JSONB,
    result_ciphertext           TEXT,
    result_object_ref           TEXT,
    result_hash                 TEXT,
    result_version              BIGINT NOT NULL DEFAULT 0,
    content_type                TEXT,
    policy                      JSONB NOT NULL DEFAULT '{}'::JSONB,
    connection_attached         BOOLEAN NOT NULL DEFAULT TRUE,
    last_disconnect_at          TIMESTAMPTZ,
    expires_at                  TIMESTAMPTZ,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at                TIMESTAMPTZ
);

-- Normalize any pre-existing trial table before enforcing the recovery contract.
-- Identity, ciphertext and deadline values cannot be fabricated: incomplete rows
-- fail with an explicit exception after all missing columns have been added.
ALTER TABLE durable_llm_tasks
    ADD COLUMN IF NOT EXISTS id UUID,
    ADD COLUMN IF NOT EXISTS tenant_id TEXT,
    ADD COLUMN IF NOT EXISTS request_id TEXT,
    ADD COLUMN IF NOT EXISTS session_id TEXT,
    ADD COLUMN IF NOT EXISTS protocol TEXT,
    ADD COLUMN IF NOT EXISTS endpoint TEXT,
    ADD COLUMN IF NOT EXISTS request_snapshot_ciphertext TEXT,
    ADD COLUMN IF NOT EXISTS snapshot_version INTEGER,
    ADD COLUMN IF NOT EXISTS encryption_key_id TEXT,
    ADD COLUMN IF NOT EXISTS request_hash TEXT,
    ADD COLUMN IF NOT EXISTS status TEXT,
    ADD COLUMN IF NOT EXISTS error_kind TEXT,
    ADD COLUMN IF NOT EXISTS reason_code TEXT,
    ADD COLUMN IF NOT EXISTS attempt_count INTEGER,
    ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS deadline_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS lease_owner TEXT,
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fencing_token BIGINT,
    ADD COLUMN IF NOT EXISTS semantic_content_committed BOOLEAN,
    ADD COLUMN IF NOT EXISTS commit_state TEXT,
    ADD COLUMN IF NOT EXISTS commit_metadata JSONB,
    ADD COLUMN IF NOT EXISTS result_ciphertext TEXT,
    ADD COLUMN IF NOT EXISTS result_object_ref TEXT,
    ADD COLUMN IF NOT EXISTS result_hash TEXT,
    ADD COLUMN IF NOT EXISTS result_version BIGINT,
    ADD COLUMN IF NOT EXISTS content_type TEXT,
    ADD COLUMN IF NOT EXISTS policy JSONB,
    ADD COLUMN IF NOT EXISTS connection_attached BOOLEAN,
    ADD COLUMN IF NOT EXISTS last_disconnect_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

UPDATE durable_llm_tasks SET status = 'accepted' WHERE status IS NULL;
UPDATE durable_llm_tasks SET attempt_count = 0 WHERE attempt_count IS NULL;
UPDATE durable_llm_tasks SET next_retry_at = NOW() WHERE next_retry_at IS NULL;
UPDATE durable_llm_tasks SET fencing_token = 0 WHERE fencing_token IS NULL;
UPDATE durable_llm_tasks SET commit_state = 'none' WHERE commit_state IS NULL;
UPDATE durable_llm_tasks SET semantic_content_committed = FALSE WHERE semantic_content_committed IS NULL;
UPDATE durable_llm_tasks SET commit_metadata = '{}'::JSONB WHERE commit_metadata IS NULL;
UPDATE durable_llm_tasks SET result_version = 0 WHERE result_version IS NULL;
UPDATE durable_llm_tasks SET policy = '{}'::JSONB WHERE policy IS NULL;
UPDATE durable_llm_tasks SET connection_attached = TRUE WHERE connection_attached IS NULL;
UPDATE durable_llm_tasks
SET created_at = CASE
    WHEN deadline_at IS NOT NULL THEN LEAST(NOW(), deadline_at - INTERVAL '1 microsecond')
    ELSE NOW()
END
WHERE created_at IS NULL;
UPDATE durable_llm_tasks SET updated_at = created_at WHERE updated_at IS NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM durable_llm_tasks
        WHERE id IS NULL
           OR NULLIF(tenant_id, '') IS NULL
           OR NULLIF(request_id, '') IS NULL
           OR NULLIF(session_id, '') IS NULL
           OR NULLIF(protocol, '') IS NULL
           OR NULLIF(endpoint, '') IS NULL
           OR NULLIF(request_snapshot_ciphertext, '') IS NULL
           OR snapshot_version IS NULL OR snapshot_version <= 0
           OR NULLIF(encryption_key_id, '') IS NULL
           OR NULLIF(request_hash, '') IS NULL
           OR deadline_at IS NULL
    ) THEN
        RAISE EXCEPTION
            '516: incomplete durable_llm_tasks trial rows require identity, encrypted snapshot, version/key/hash and deadline backfill';
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'durable_llm_tasks'::REGCLASS
          AND contype = 'p'
    ) THEN
        ALTER TABLE durable_llm_tasks
            ADD CONSTRAINT durable_llm_tasks_pkey PRIMARY KEY (id);
    ELSIF NOT EXISTS (
        SELECT 1
        FROM pg_constraint c
        JOIN unnest(c.conkey) WITH ORDINALITY AS k(attnum, ordinality) ON TRUE
        JOIN pg_attribute a
          ON a.attrelid = c.conrelid AND a.attnum = k.attnum
        WHERE c.conrelid = 'durable_llm_tasks'::REGCLASS
          AND c.contype = 'p'
        GROUP BY c.oid
        HAVING array_agg(a.attname ORDER BY k.ordinality) = ARRAY['id']::NAME[]
    ) THEN
        RAISE EXCEPTION '516: durable_llm_tasks trial primary key must be exactly (id)';
    END IF;
END $$;

ALTER TABLE durable_llm_tasks
    ALTER COLUMN id SET NOT NULL,
    ALTER COLUMN tenant_id SET NOT NULL,
    ALTER COLUMN request_id SET NOT NULL,
    ALTER COLUMN session_id SET NOT NULL,
    ALTER COLUMN protocol SET NOT NULL,
    ALTER COLUMN endpoint SET NOT NULL,
    ALTER COLUMN request_snapshot_ciphertext SET NOT NULL,
    ALTER COLUMN snapshot_version SET NOT NULL,
    ALTER COLUMN encryption_key_id SET NOT NULL,
    ALTER COLUMN request_hash SET NOT NULL,
    ALTER COLUMN status SET DEFAULT 'accepted',
    ALTER COLUMN status SET NOT NULL,
    ALTER COLUMN attempt_count SET DEFAULT 0,
    ALTER COLUMN attempt_count SET NOT NULL,
    ALTER COLUMN next_retry_at SET DEFAULT NOW(),
    ALTER COLUMN next_retry_at SET NOT NULL,
    ALTER COLUMN deadline_at SET NOT NULL,
    ALTER COLUMN fencing_token SET DEFAULT 0,
    ALTER COLUMN fencing_token SET NOT NULL,
    ALTER COLUMN semantic_content_committed SET DEFAULT FALSE,
    ALTER COLUMN semantic_content_committed SET NOT NULL,
    ALTER COLUMN commit_state SET DEFAULT 'none',
    ALTER COLUMN commit_state SET NOT NULL,
    ALTER COLUMN commit_metadata SET DEFAULT '{}'::JSONB,
    ALTER COLUMN commit_metadata SET NOT NULL,
    ALTER COLUMN result_version SET DEFAULT 0,
    ALTER COLUMN result_version SET NOT NULL,
    ALTER COLUMN policy SET DEFAULT '{}'::JSONB,
    ALTER COLUMN policy SET NOT NULL,
    ALTER COLUMN connection_attached SET DEFAULT TRUE,
    ALTER COLUMN connection_attached SET NOT NULL,
    ALTER COLUMN created_at SET DEFAULT NOW(),
    ALTER COLUMN created_at SET NOT NULL,
    ALTER COLUMN updated_at SET DEFAULT NOW(),
    ALTER COLUMN updated_at SET NOT NULL;

ALTER TABLE durable_llm_tasks
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_status,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_commit_state,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_attempt_count,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_fencing_token,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_result_version,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_deadline,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_lease_pair,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_running_lease,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_runnable_commit,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_semantic_commit,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_result_source,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_terminal,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_completed_result,
    DROP CONSTRAINT IF EXISTS chk_durable_llm_tasks_identity;

ALTER TABLE durable_llm_tasks
    ADD CONSTRAINT chk_durable_llm_tasks_status CHECK (status IN (
        'accepted', 'running', 'streaming', 'waiting_recovery', 'retry_scheduled',
        'completed', 'permanent_failed', 'expired', 'cancelled', 'resume_safety_blocked'
    )),
    ADD CONSTRAINT chk_durable_llm_tasks_commit_state CHECK (
        commit_state IN ('none', 'metadata', 'content', 'tool_call', 'terminal')
    ),
    ADD CONSTRAINT chk_durable_llm_tasks_attempt_count CHECK (attempt_count >= 0),
    ADD CONSTRAINT chk_durable_llm_tasks_fencing_token CHECK (fencing_token >= 0),
    ADD CONSTRAINT chk_durable_llm_tasks_result_version CHECK (result_version >= 0),
    ADD CONSTRAINT chk_durable_llm_tasks_deadline CHECK (deadline_at > created_at),
    ADD CONSTRAINT chk_durable_llm_tasks_lease_pair CHECK (
        (lease_owner IS NULL) = (lease_until IS NULL)
    ),
    ADD CONSTRAINT chk_durable_llm_tasks_running_lease CHECK (
        status NOT IN ('running', 'streaming')
        OR (lease_owner IS NOT NULL AND lease_until IS NOT NULL AND fencing_token > 0)
    ),
    ADD CONSTRAINT chk_durable_llm_tasks_runnable_commit CHECK (
        status NOT IN ('waiting_recovery', 'retry_scheduled')
        OR commit_state IN ('none', 'metadata')
    ),
    ADD CONSTRAINT chk_durable_llm_tasks_semantic_commit CHECK (
        commit_state NOT IN ('content', 'tool_call') OR semantic_content_committed
    ),
    ADD CONSTRAINT chk_durable_llm_tasks_result_source CHECK (
        result_ciphertext IS NULL OR result_object_ref IS NULL
    ),
    ADD CONSTRAINT chk_durable_llm_tasks_terminal CHECK (
        (status IN ('completed', 'permanent_failed', 'expired', 'cancelled', 'resume_safety_blocked'))
        = (completed_at IS NOT NULL)
    ),
    ADD CONSTRAINT chk_durable_llm_tasks_completed_result CHECK (
        status <> 'completed' OR (
            result_version > 0
            AND NULLIF(result_hash, '') IS NOT NULL
            AND NULLIF(content_type, '') IS NOT NULL
            AND ((NULLIF(result_ciphertext, '') IS NOT NULL)::INTEGER
                 + (NULLIF(result_object_ref, '') IS NOT NULL)::INTEGER) = 1
        )
    ),
    ADD CONSTRAINT chk_durable_llm_tasks_identity CHECK (
        NULLIF(tenant_id, '') IS NOT NULL
        AND NULLIF(request_id, '') IS NOT NULL
        AND NULLIF(session_id, '') IS NOT NULL
        AND NULLIF(protocol, '') IS NOT NULL
        AND NULLIF(endpoint, '') IS NOT NULL
        AND NULLIF(request_snapshot_ciphertext, '') IS NOT NULL
        AND snapshot_version > 0
        AND NULLIF(encryption_key_id, '') IS NOT NULL
        AND NULLIF(request_hash, '') IS NOT NULL
    );

CREATE UNIQUE INDEX IF NOT EXISTS uq_durable_llm_tasks_tenant_request
    ON durable_llm_tasks (tenant_id, request_id);
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_runnable
    ON durable_llm_tasks (status, next_retry_at)
    WHERE status IN ('waiting_recovery', 'retry_scheduled', 'running')
      AND commit_state IN ('none', 'metadata');
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_tenant_status
    ON durable_llm_tasks (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_lease_expiration
    ON durable_llm_tasks (lease_until) WHERE lease_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_session_request
    ON durable_llm_tasks (session_id, request_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_deadline
    ON durable_llm_tasks (deadline_at)
    WHERE status IN ('accepted', 'running', 'streaming', 'waiting_recovery', 'retry_scheduled');

ALTER TABLE durable_llm_tasks ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS durable_llm_tasks_tenant_isolation ON durable_llm_tasks;
CREATE POLICY durable_llm_tasks_tenant_isolation ON durable_llm_tasks
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS durable_llm_tasks_super_admin_bypass ON durable_llm_tasks;
CREATE POLICY durable_llm_tasks_super_admin_bypass ON durable_llm_tasks
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE durable_llm_tasks IS
    'M3 SR-W3 durable task/result SSoT; claim, checkpoint, reschedule and terminal writes require lease/fencing CAS.';
COMMENT ON COLUMN durable_llm_tasks.id IS
    'Repository-supplied UUID; no sequence/default, including schema-dump deployments.';
COMMENT ON COLUMN durable_llm_tasks.request_snapshot_ciphertext IS
    'Versioned normalized request encrypted with durable-request AAD; no API key plaintext.';
COMMENT ON COLUMN durable_llm_tasks.commit_state IS
    'none/metadata may be reclaimed; content/tool_call/terminal must not be replayed.';
COMMENT ON COLUMN durable_llm_tasks.commit_metadata IS
    'Write-ahead checkpoint details such as tool call identity and argument digest.';
COMMENT ON COLUMN durable_llm_tasks.result_ciphertext IS
    'Normalized final response encrypted with durable-result AAD.';
COMMENT ON COLUMN durable_llm_tasks.result_object_ref IS
    'Server-encrypted immutable object reference, validated with result_hash.';
COMMENT ON COLUMN durable_llm_tasks.fencing_token IS
    'Monotonic claim generation compared with id and lease_owner on every mutation.';

-- Extend migration 515's table without changing seq replay uniqueness.
ALTER TABLE request_state_transitions
    ADD COLUMN IF NOT EXISTS attempt_no INTEGER;
ALTER TABLE request_state_transitions
    DROP CONSTRAINT IF EXISTS request_state_transitions_transition_type_check,
    DROP CONSTRAINT IF EXISTS chk_request_state_transitions_transition_type,
    DROP CONSTRAINT IF EXISTS chk_request_state_transitions_attempt_no;
ALTER TABLE request_state_transitions
    ADD CONSTRAINT chk_request_state_transitions_transition_type CHECK (
        transition_type IN (
            'route', 'node_switch', 'retry', 'error', 'state',
            'survival_accepted', 'survival_running', 'survival_waiting',
            'survival_connection_detached', 'survival_retry', 'survival_recovered',
            'survival_completed', 'survival_permanent_failed', 'survival_expired',
            'survival_cancelled', 'survival_resume_safety_blocked'
        )
    ),
    ADD CONSTRAINT chk_request_state_transitions_attempt_no CHECK (
        attempt_no IS NULL OR attempt_no >= 1
    );
CREATE INDEX IF NOT EXISTS idx_state_transitions_request_attempt
    ON request_state_transitions (request_id, attempt_no, created_at)
    WHERE attempt_no IS NOT NULL;
COMMENT ON COLUMN request_state_transitions.attempt_no IS
    'M3 SR-W3 1-based attempt number; candidate outcomes are stored in metadata.';

COMMIT;

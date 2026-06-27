-- Migration 120: Session Audit & Approval Queue (2026-06-27)
-- Purpose: Persist session audit records and the approval queue used by
--   the SessionAuditHook / ApprovalGateHook pipeline
--   (services/llm-gateway-go/domains/sessionaudit).
--
-- Both tables are tenant-scoped (Pattern A) and protected by PostgreSQL
-- Row-Level Security (RLS) so that the application-layer WHERE
-- tenant_id = $N filter in admin/session_audit.go and
-- domains/sessionaudit/approval_manager.go is backed up by the database
-- refusing to leak rows that don't match the GUC `app.current_tenant`.
--
-- Why two tables, not one:
--   session_audit_records  — write-heavy (every request → one row),
--                            append-only, no per-row decisions.
--   approval_queue         — much smaller, holds pending approvals
--                            (awaiting admin approval). Updated to
--                            approved/rejected/timeout.
--
-- Both tables keep `created_at` / `expires_at` / `approved_at` indexed
-- so the Admin API list/stats endpoints can answer "show pending" and
-- "show last N records for tenant X" cheaply.

-- ====================================================================
-- session_audit_records — append-only per-request audit trail
-- ====================================================================

CREATE TABLE IF NOT EXISTS session_audit_records (
    id                  BIGSERIAL PRIMARY KEY,
    session_id          TEXT        NOT NULL,
    tenant_id           TEXT        NOT NULL,
    request_id          TEXT        NOT NULL,

    -- Client context (denormalized for fast read; PII is REDACTED at write time)
    client_ip           TEXT,
    client_user_agent   TEXT,
    client_model        TEXT,

    -- Async-summary fields (filled by LLM-backed worker; nullable on insert)
    content_summary     TEXT,
    content_title       TEXT,
    content_hash        TEXT,

    -- Async-intent (filled by LLM-backed worker)
    intent_type         TEXT,
    intent_score        DOUBLE PRECISION,
    intent_reason       TEXT,

    -- Multi-dimension scores (filled by background scorer)
    security_score      INTEGER,
    danger_score        INTEGER,
    trust_score         INTEGER,
    sensitive_score     INTEGER,

    -- Real-time detection results
    detect_score        INTEGER     NOT NULL DEFAULT 0,
    detect_decision     TEXT        NOT NULL DEFAULT 'pass',
    threats             JSONB       NOT NULL DEFAULT '[]'::jsonb,
    sensitive_words     JSONB       NOT NULL DEFAULT '[]'::jsonb,

    -- Final status: pass / warn / blocked / need_approval
    status              TEXT        NOT NULL DEFAULT 'pass',
    approval_status     TEXT,                       -- pending / approved / rejected / timeout

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Index: tenant-scoped time-ordered lookup (admin list + stats)
CREATE INDEX IF NOT EXISTS idx_session_audit_records_tenant_created
    ON session_audit_records (tenant_id, created_at DESC);

-- Index: per-session lookup (admin "show history of this session")
CREATE INDEX IF NOT EXISTS idx_session_audit_records_session
    ON session_audit_records (session_id, created_at DESC);

-- Index: pending approvals queue (approval worker poll)
CREATE INDEX IF NOT EXISTS idx_session_audit_records_status
    ON session_audit_records (status, created_at DESC)
    WHERE status = 'need_approval';

-- ====================================================================
-- approval_queue — pending approvals awaiting human decision
-- ====================================================================

CREATE TABLE IF NOT EXISTS approval_queue (
    id            UUID        PRIMARY KEY,
    session_id    TEXT        NOT NULL,
    tenant_id     TEXT        NOT NULL,
    request_id    TEXT        NOT NULL,

    -- Snapshot of the detection result + full request body, so the
    -- admin can replay / inspect without re-running detection.
    detect_result JSONB       NOT NULL,
    snapshot      JSONB       NOT NULL,

    status        TEXT        NOT NULL DEFAULT 'pending',
                  -- pending / approved / rejected / timeout
    approved_by   TEXT,
    approved_at   TIMESTAMPTZ,
    reason        TEXT,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,

    CONSTRAINT approval_queue_status_chk CHECK (
        status IN ('pending', 'approved', 'rejected', 'timeout')
    )
);

-- Index: pending by tenant (admin "show my pending")
CREATE INDEX IF NOT EXISTS idx_approval_queue_tenant_pending
    ON approval_queue (tenant_id, created_at DESC)
    WHERE status = 'pending';

-- Index: timeout sweep (background worker finds pending rows past expires_at)
CREATE INDEX IF NOT EXISTS idx_approval_queue_expires
    ON approval_queue (expires_at)
    WHERE status = 'pending';

-- Index: per-session history
CREATE INDEX IF NOT EXISTS idx_approval_queue_session
    ON approval_queue (session_id, created_at DESC);

-- ====================================================================
-- Row-Level Security (RLS) — Pattern A defense-in-depth
-- ====================================================================
-- Both tables have tenant_id. The policy reads the GUC `app.current_tenant`
-- (set per-connection by withTenantTx in admin/tenant_ctx.go). The default
-- value 'default' matches no real tenant row, so the policy is fail-closed
-- if the GUC is never set.

ALTER TABLE session_audit_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE session_audit_records FORCE  ROW LEVEL SECURITY;

-- tenant 隔离 + super_admin bypass：
--   正常路径：tenant_id 等于 app.current_tenant
--   super_admin 路径：app.current_role = 'super_admin'，无 tenant 限制
DROP POLICY IF EXISTS tenant_isolation_session_audit_records ON session_audit_records;
CREATE POLICY tenant_isolation_session_audit_records ON session_audit_records
    USING (
        COALESCE(NULLIF(current_setting('app.current_role', true), ''), '') = 'super_admin'
        OR (tenant_id)::text = COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default')
    )
    WITH CHECK (
        COALESCE(NULLIF(current_setting('app.current_role', true), ''), '') = 'super_admin'
        OR (tenant_id)::text = COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default')
    );

ALTER TABLE approval_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_queue FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation_approval_queue ON approval_queue;
CREATE POLICY tenant_isolation_approval_queue ON approval_queue
    USING (
        COALESCE(NULLIF(current_setting('app.current_role', true), ''), '') = 'super_admin'
        OR (tenant_id)::text = COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default')
    )
    WITH CHECK (
        COALESCE(NULLIF(current_setting('app.current_role', true), ''), '') = 'super_admin'
        OR (tenant_id)::text = COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default')
    );

-- ====================================================================
-- updated_at trigger on session_audit_records (audit-only fields rarely
-- change but admin "approve" might patch approval_status later).
-- ====================================================================
CREATE OR REPLACE FUNCTION trg_session_audit_records_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS session_audit_records_updated_at ON session_audit_records;
CREATE TRIGGER session_audit_records_updated_at
    BEFORE UPDATE ON session_audit_records
    FOR EACH ROW
    EXECUTE FUNCTION trg_session_audit_records_updated_at();
-- 912_session_recording_trash.sql
-- W1.5: session body trash + hidden ledger for trash/restore/purge.
-- Usage/ledger tables are intentionally untouched.
-- Idempotent: CREATE TABLE IF NOT EXISTS + indexes IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS public.session_bodies_trash (
    tenant_id              TEXT NOT NULL,
    session_id             TEXT NOT NULL,
    turn_no                INT  NOT NULL DEFAULT 0,
    request_id             TEXT NOT NULL,
    ts                     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    request_delta          JSONB,
    response_delta         JSONB,
    outbound_body          JSONB,
    request_attachments    JSONB,
    response_attachments   JSONB,
    kind                   TEXT NOT NULL DEFAULT 'turn_delta',
    partition_date         DATE NOT NULL DEFAULT CURRENT_DATE,
    trashed_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, session_id, request_id, partition_date)
);

CREATE INDEX IF NOT EXISTS idx_session_bodies_trash_trashed_at
    ON public.session_bodies_trash (trashed_at);
CREATE INDEX IF NOT EXISTS idx_session_bodies_trash_session
    ON public.session_bodies_trash (tenant_id, session_id);

COMMENT ON TABLE public.session_bodies_trash IS
    'W1.5 recycle bin for session_bodies rows. Usage ledgers never enter here. '
    'Hourly cleanup erases rows past session_body_retention_hours. '
    'Created: 2026-10-28, Migration 912';

CREATE TABLE IF NOT EXISTS public.session_hidden (
    tenant_id     TEXT NOT NULL,
    session_id    TEXT NOT NULL,
    hidden_until  TIMESTAMPTZ NOT NULL,
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason        TEXT NOT NULL DEFAULT 'trash',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, session_id)
);

COMMENT ON TABLE public.session_hidden IS
    'W1.5: sessions hidden from list after trash/purge (magpie gateway-sessions-hidden). '
    'Permanent purge keeps the row so the session stays out of lists. '
    'Created: 2026-10-28, Migration 912';

ALTER TABLE public.session_bodies_trash ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_hidden ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS session_bodies_trash_tenant ON public.session_bodies_trash;
CREATE POLICY session_bodies_trash_tenant ON public.session_bodies_trash
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS session_hidden_tenant ON public.session_hidden;
CREATE POLICY session_hidden_tenant ON public.session_hidden
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

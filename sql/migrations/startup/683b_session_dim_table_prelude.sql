-- Migration 683b: create public.session_dim, the table that 683 widens.
--
-- Why this file exists (2026-10-10, host-first local deploy):
-- 683_session_dim_ownership_columns.sql does
--     ALTER TABLE session_dim ADD COLUMN IF NOT EXISTS ...
-- for seven ownership columns, plus three partial indexes on it. The table
-- itself is created by 350_session_analytics_fix.sql, which was never part of
-- this sequence, so on a host where the gateway's Go ensure chain had not
-- booted yet the sequence aborted with
--     ERROR: relation "session_dim" does not exist
-- and, because the ledger never advanced, every later file was blocked too.
--
-- Registering 350 directly does not work: 350 also does
--     DROP FUNCTION IF EXISTS update_session_summary();
-- so it can re-attach its trigger. Once 563 has created that trigger the drop
-- fails with
--     ERROR: cannot drop function update_session_summary() because other
--            objects depend on it
-- (2BP01) — a dependency ordering 350 cannot satisfy from inside the same
-- run, and it would abort before reaching any useful work.
--
-- So this file does the minimum: create session_dim exactly as 350 defines
-- it (same columns, types, defaults, indexes and RLS policies) and nothing
-- else. The trigger work stays with 350/358, which run later in the Go ensure
-- chain where their ordering assumptions hold.
--
-- Idempotent: CREATE TABLE/INDEX/POLICY are all IF NOT EXISTS, and the RLS
-- block is skipped entirely when the table is already present.

CREATE TABLE IF NOT EXISTS session_dim (
    gw_session_id VARCHAR(128) PRIMARY KEY,
    session_key   VARCHAR(255) NOT NULL,  -- = gw_session_id（兼容 session_summaries）
    tenant_id     VARCHAR(255) NOT NULL,
    task_id       VARCHAR(128),
    status        VARCHAR(20)  NOT NULL DEFAULT 'active',  -- active|closed|idle
    first_request_at TIMESTAMPTZ,
    last_active_at  TIMESTAMPTZ,
    closed_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_session_dim_tenant
    ON session_dim(tenant_id, last_active_at DESC);
CREATE INDEX IF NOT EXISTS idx_session_dim_task
    ON session_dim(tenant_id, task_id) WHERE task_id IS NOT NULL;

-- 350 enables RLS unconditionally, which is fine for a fresh table. Guard it
-- so re-running against a populated database never re-issues ALTER ... ENABLE
-- on a table whose policies already exist.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_policies
    WHERE schemaname = 'public' AND tablename = 'session_dim'
  ) THEN
    ALTER TABLE session_dim ENABLE ROW LEVEL SECURITY;
    CREATE POLICY session_dim_tenant_isolation ON session_dim
        USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
    CREATE POLICY session_dim_super_admin_bypass ON session_dim
        USING (
            current_setting('app.current_role', true) = 'super_admin'
            OR current_setting('app.bypass_rls', true) = 'true'
        );
  END IF;
END
$$;

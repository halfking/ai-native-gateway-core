-- 385_session_turn_snapshots.down.sql
-- Reverse the per-turn audit snapshot table from migration 385. Use only
-- after confirming no production reads still depend on it (see
-- docs/拆分/08b-会话展示与实施.md S9).
--
-- Order: drop RLS policies → drop table. Policies reference the table name
-- and must be removed before the table is dropped to avoid dangling
-- objects in the migration runner.

BEGIN;

DROP POLICY IF EXISTS session_turn_snapshots_tenant_isolation
    ON public.session_turn_snapshots;
DROP POLICY IF EXISTS session_turn_snapshots_super_admin_bypass
    ON public.session_turn_snapshots;

DROP TABLE IF EXISTS public.session_turn_snapshots;

COMMIT;

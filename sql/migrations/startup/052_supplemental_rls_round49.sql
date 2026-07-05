-- Round 49 (2026-06-26) — supplemental RLS for candidate_failure_logs + request_wal.
--
-- Why this is a separate migration instead of being appended to the
-- original migrations:
--
--   1. candidate_failure_logs lives in 037_candidate_failure_logs.sql
--      (candidate-failure-monitor project, untouched).
--   2. request_wal lives in 032_request_wal.sql (request-WAL project,
--      untouched).
--
-- Per the deployment protocol for Round 49 (mirrors Round 48 pattern in
-- 026_supplemental_rls.sql), we ship a single RLS-supplement migration
-- rather than editing files owned by other projects. Each ALTER TABLE /
-- CREATE POLICY here is idempotent (DROP POLICY IF EXISTS guard) so
-- re-running the migration is safe.
--
-- Without this migration, pg-rls-lint flags L1 FAIL for both tables
-- (tenant_id column exists but no ENABLE ROW LEVEL SECURITY + policy).

-- ── From migration 037 (candidate_failure_logs) ────────────────────
ALTER TABLE candidate_failure_logs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_candidate_failure_logs ON public.candidate_failure_logs;
CREATE POLICY tenant_isolation_candidate_failure_logs ON public.candidate_failure_logs
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ── From migration 032 (request_wal) ──────────────────────────────
-- request_wal is a partitioned table (PARTITION BY RANGE on created_at).
-- PostgreSQL 15+ propagates RLS from the parent to all child partitions
-- automatically, so enabling RLS on the parent is sufficient.
ALTER TABLE request_wal ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_request_wal ON public.request_wal;
CREATE POLICY tenant_isolation_request_wal ON public.request_wal
    USING ((tenant_id)::text = (public.get_current_tenant())::text);
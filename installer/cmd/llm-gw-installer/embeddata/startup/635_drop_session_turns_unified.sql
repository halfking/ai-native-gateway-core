-- Migration 635: drop orphan session_turns_unified view created by 619.
-- audit-data-closure P1-2 (2026-08-31-24h-correction-followup-audit):
--   Migration 619 created session_turns_unified as a simple UNION ALL of
--   session_turns_hot and session_turns (columnar partitions). The intent
--   was for application readers to query hot + historical in one shot, but
--   the view never picked up any production reader. Production readers all
--   use session_turns_with_current_month (defined in 526), which carries
--   a NOT EXISTS dedup predicate so a row that is currently in both hot
--   and the just-promoted partition does not double-count.
--
-- Two views with subtly different semantics for the same query pattern is
-- a footgun for future readers and a permanent drift hazard. Since no
-- production code reads session_turns_unified, we drop it.
--
-- Backwards compatibility:
--   * The 619 .sql is being deleted in the same commit so a fresh install
--     never creates the view in the first place.
--   * This migration exists only to clean up environments that already
--     applied 619 before the audit decision; the down migration recreates
--     the view verbatim so a rollback is symmetric with the 619 up.
--
-- Safety:
--   * IF EXISTS makes the migration idempotent against fresh installs.
--   * The view carries no dependents (no other view references it, no
--     function uses it, and no GRANT exists) — verified by the audit's
--     dependency walk over every .go file under domains/, admin/, cmd/.

\set ON_ERROR_STOP on

DROP VIEW IF EXISTS public.session_turns_unified;

DO $$
BEGIN
    RAISE NOTICE 'Migration 635: dropped orphan session_turns_unified view (619 cleanup)';
END $$;
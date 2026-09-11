-- ===========================================================================
-- File:          sql/migrations/startup/696_request_logs_view_system_fingerprint.sql
-- Migration:     696
-- Database:      llm_gateway
-- Purpose:       Ensure the canonical request_logs_with_current_month view
--                exposes system_fingerprint (4th lateral stage, same shape as
--                customer_id 577 / request_class+due_at 610) so
--                bg/integrity_fingerprint_drift.go can read the view per the
--                recent-window read-surface doctrine.
--
-- Status:        active
-- Idempotent:    YES (column-presence guard; CREATE OR REPLACE only appends)
-- Dependencies:  system_fingerprint on BOTH request_logs_hot and request_logs
--                (migration 487 added the parent side, 603 repaired the hot
--                side). Wrapper chain from 577/610/680 untouched.
--
-- Background:
--   The 7-day fingerprint drift window is a recent-window read, so per the
--   2026-09-10 minimax-prod-v2 doctrine it must read
--   request_logs_with_current_month, not the bare parent (which only holds
--   promoted cold rows). It stayed on the bare parent (pinned by
--   bg/recent_surface_reads_test.go) because the view lacked the column: the
--   base wrapper's hot∩parent intersection was frozen before 603 added the
--   column to the hot table, and no later migration rebuilt the chain.
--
--   Two wrapper shapes exist in the wild and BOTH must converge to "the
--   canonical view exposes system_fingerprint exactly once":
--     a) frozen pre-603 chain (production): base wrapper lacks the column →
--        append it via the lateral stage (CREATE OR REPLACE, column added
--        last);
--     b) dynamically rebuilt chain (680 bootstrap / db.go self-heal on a
--        post-603 database): the base intersection already carries the
--        column, the canonical inherits it via v.* → nothing to do
--        (an unconditional CREATE OR REPLACE would fail with "column
--        already exists" because the lateral would move the column after
--        request_class/due_at).
--
-- Safety:
--   - CREATE OR REPLACE VIEW only: no DROP, readers never see a missing
--     relation (concurrent readers block briefly, never fail with 42P01).
--   - All 50+ existing view consumers use explicit column lists (audited
--     2026-09-12); a trailing appended column cannot break positional scans.
--   - Column ORDER may differ between shape (a) and (b) — consumers are
--     name-based, so this is cosmetic.
--
-- Rollback Script: none (CREATE OR REPLACE the previous 610-shape canonical
--   view to restore; the column is additive).
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  canonical_has_fingerprint boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'request_logs_with_current_month'
      AND column_name = 'system_fingerprint'
  ) INTO canonical_has_fingerprint;

  IF canonical_has_fingerprint THEN
    RAISE NOTICE '696: request_logs_with_current_month already exposes system_fingerprint; nothing to do';
    RETURN;
  END IF;

  CREATE OR REPLACE VIEW public.request_logs_with_current_month AS
  SELECT v.*, source.request_class, source.due_at, source.system_fingerprint
  FROM public.request_logs_with_current_month_without_request_class_due_at v
  LEFT JOIN LATERAL (
      SELECT h.request_class, h.due_at, h.system_fingerprint
      FROM public.request_logs_hot h
      WHERE h.request_id = v.request_id AND h.ts = v.ts
      UNION ALL
      SELECT p.request_class, p.due_at, p.system_fingerprint
      FROM public.request_logs p
      WHERE p.request_id = v.request_id AND p.ts = v.ts
      LIMIT 1
  ) source ON true;

  COMMENT ON VIEW public.request_logs_with_current_month IS
    'Hot + monthly partitions UNION with customer_id (577), request_class/due_at (610) '
    'and system_fingerprint (696) appended. Bootstrap-recreated by 680 / '
    'db.ensureRequestLogsCurrentMonthView when dropped out-of-band.';
END $$;

COMMIT;

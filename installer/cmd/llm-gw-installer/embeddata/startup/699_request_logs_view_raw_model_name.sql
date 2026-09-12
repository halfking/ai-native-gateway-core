-- ===========================================================================
-- File:          sql/migrations/startup/699_request_logs_view_raw_model_name.sql
-- Migration:     699
-- Database:      llm_gateway
-- Purpose:       Ensure the canonical request_logs_with_current_month view
--                exposes raw_model_name. The 7-day fingerprint drift scanner
--                (bg/integrity_fingerprint_drift.go, read surface moved to
--                this view by the 83bf582dd / startup-696 recent-window
--                doctrine) selects credential_id, raw_model_name,
--                system_fingerprint from the view — but the wrapper chain's
--                base intersection was frozen before 485 added raw_model_name
--                to request_logs, so the column never surfaced: every scan
--                failed with "column \"raw_model_name\" does not exist
--                (SQLSTATE 42703)". Verified live on the local deploy DB
--                (hourly WARN since the view switch shipped) and on the
--                shared 252 production PG (view = 112 columns, raw_model_name
--                absent — latent until the next scanner-bearing binary
--                deploys there).
--
-- Status:        active
-- Idempotent:    YES (column-presence guard; CREATE OR REPLACE only appends)
-- Dependencies:  raw_model_name on request_logs (485) AND request_logs_hot
--                (603 consistency repair); wrapper chain from 577/610/680;
--                system_fingerprint lateral from 696 (preserved).
--
-- Shape notes:
--   a) frozen pre-485 chain (local + production today): canonical lacks
--      raw_model_name → rebuild the canonical stage carrying the 696
--      fingerprint column PLUS raw_model_name on the same hot-first lateral;
--   b) dynamically rebuilt chain (680 bootstrap / db.go self-heal on a
--      post-485 database): the base intersection already carries the column
--      → the guard no-ops (an unconditional lateral re-add would fail with
--      "column already exists" — same dual-shape trap 696 documented).
--   Guard-passing chains never carry raw_model_name in the reused wrapper,
--   and 696 always precedes 699 in the channel, so the static select list
--   (request_class, due_at, system_fingerprint, raw_model_name) is safe in
--   every reachable state.
--
-- Safety:
--   - CREATE OR REPLACE VIEW only: no DROP, readers never see a missing
--     relation (concurrent readers block briefly, never fail with 42P01).
--   - All 50+ existing view consumers use explicit column lists (audited
--     2026-09-12 for 696; same consumer set); a trailing appended column
--     cannot break positional scans.
--   - The appended column lands after system_fingerprint; consumers are
--     name-based, so column order is cosmetic.
--
-- Rollback Script: none (CREATE OR REPLACE the 696-shape canonical view to
--   restore; the column is additive).
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  canonical_has_raw_model_name boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'request_logs_with_current_month'
      AND column_name = 'raw_model_name'
  ) INTO canonical_has_raw_model_name;

  IF canonical_has_raw_model_name THEN
    RAISE NOTICE '699: request_logs_with_current_month already exposes raw_model_name; nothing to do';
    RETURN;
  END IF;

  CREATE OR REPLACE VIEW public.request_logs_with_current_month AS
  SELECT v.*, source.request_class, source.due_at, source.system_fingerprint, source.raw_model_name
  FROM public.request_logs_with_current_month_without_request_class_due_at v
  LEFT JOIN LATERAL (
      SELECT h.request_class, h.due_at, h.system_fingerprint, h.raw_model_name
      FROM public.request_logs_hot h
      WHERE h.request_id = v.request_id AND h.ts = v.ts
      UNION ALL
      SELECT p.request_class, p.due_at, p.system_fingerprint, p.raw_model_name
      FROM public.request_logs p
      WHERE p.request_id = v.request_id AND p.ts = v.ts
      LIMIT 1
  ) source ON true;

  COMMENT ON VIEW public.request_logs_with_current_month IS
    'Hot + monthly partitions UNION with customer_id (577), request_class/due_at (610), '
    'system_fingerprint (696) and raw_model_name (699) appended. Bootstrap-recreated by '
    '680 / db.ensureRequestLogsCurrentMonthView when dropped out-of-band.';
END $$;

COMMIT;

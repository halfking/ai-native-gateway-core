BEGIN;

-- Migration 466: Relax chk_compression_parent_single to allow loopback correlation
--
-- Background:
--   2026-08-06 auto-title/auto-summary loopback feature reuses
--   request_logs_hot.parent_request_id to correlate a loopback child request with
--   its parent user request (X-Gw-Parent-Request-Id header). The handler writes
--   parent_request_id + origin_actor (see applyParentCorrelationFields in
--   domains/streaming/request_log_pipeline.go) but never compression_reason.
--
--   The original constraint (013_compression_columns.sql, Round 47) was designed
--   under the assumption "parent_request_id = compression child marker" and forced
--   every child to explain WHY it was created (compression_reason). Loopback rows
--   violate it -> SQLSTATE 23514 -> telemetry fallback (the loopback log row is
--   lost, though title generation itself still works).
--
-- Fix:
--   Relax the CHECK to also allow a row with a parent when it carries an
--   origin_actor (loopback correlation). Compression children still must explain
--   why (compression_reason NOT NULL), so the compression invariant is preserved;
--   the correlation case is cleanly distinguished by origin_actor IS NOT NULL.
--
--   Old: CHECK (parent_request_id IS NULL OR compression_reason IS NOT NULL)
--   New: CHECK (parent_request_id IS NULL OR compression_reason IS NOT NULL
--               OR origin_actor IS NOT NULL)
--
-- Scope (relations carrying the constraint today):
--   request_logs (partitioned parent), request_logs_hot,
--   request_logs_2026_07, request_logs_2026_08 (monthly partitions).
--
-- Semantics (verified on PG17):
--   - ALTER TABLE request_logs DROP CONSTRAINT (no ONLY) drops the constraint
--     recursively on the partitioned parent AND all its partitions.
--   - ALTER TABLE request_logs ADD CONSTRAINT propagates the CHECK to existing
--     and future partitions.
--   - request_logs_hot is a plain heap table, handled separately.
--
-- Idempotency: existence guards before each DROP; re-running is safe.
--
-- Rollback: sql/migrations/startup/466_relax_compression_parent_check.down.sql
-- restores the strict CHECK.

DO $$
BEGIN
  -- Partitioned parent (drops recursively across all monthly partitions).
  IF EXISTS (SELECT 1 FROM pg_constraint
             WHERE conname = 'chk_compression_parent_single'
               AND conrelid = 'public.request_logs'::regclass) THEN
    ALTER TABLE public.request_logs DROP CONSTRAINT chk_compression_parent_single;
  END IF;

  -- Hot heap table (where loopback rows land).
  IF EXISTS (SELECT 1 FROM pg_constraint
             WHERE conname = 'chk_compression_parent_single'
               AND conrelid = 'public.request_logs_hot'::regclass) THEN
    ALTER TABLE public.request_logs_hot DROP CONSTRAINT chk_compression_parent_single;
  END IF;

  -- Re-add relaxed on the partitioned parent (propagates to partitions).
  ALTER TABLE public.request_logs
    ADD CONSTRAINT chk_compression_parent_single
    CHECK (parent_request_id IS NULL OR compression_reason IS NOT NULL OR origin_actor IS NOT NULL);

  -- Re-add relaxed on the hot heap table.
  ALTER TABLE public.request_logs_hot
    ADD CONSTRAINT chk_compression_parent_single
    CHECK (parent_request_id IS NULL OR compression_reason IS NOT NULL OR origin_actor IS NOT NULL);
END $$;

-- Verification: the relaxed definition must be present on parent + hot
-- (partition copies follow automatically).
DO $$
DECLARE
  strict_left int;
BEGIN
  SELECT count(*) INTO strict_left
  FROM pg_constraint c
  WHERE c.conname = 'chk_compression_parent_single'
    AND NOT (c.convalidated
             AND pg_get_constraintdef(c.oid) LIKE '%origin_actor IS NOT NULL%');
  IF strict_left > 0 THEN
    RAISE EXCEPTION 'chk_compression_parent_single still strict on % relation(s)', strict_left;
  END IF;
END $$;

COMMIT;

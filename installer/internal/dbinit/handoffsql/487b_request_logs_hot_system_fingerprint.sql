-- Migration 487b: add system_fingerprint to request_logs_hot as well.
--
-- Why this file exists (2026-10-10, host-first local deploy):
-- 487_request_logs_add_system_fingerprint.sql only alters the partitioned
-- parent `request_logs`:
--     ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS system_fingerprint TEXT;
--
-- 696_request_logs_view_system_fingerprint.sql then rebuilds
-- request_logs_with_current_month with a LATERAL that selects
--     h.system_request_class, h.due_at, h.system_fingerprint
-- from public.request_logs_hot `h` UNION ALL the parent `p`. On a fresh
-- database the hot table is created by the snapshot without that column (the
-- hot shape is maintained separately from the parent), so the rebuild failed
-- with
--     ERROR: column h.system_fingerprint does not exist
-- and the sequence never advanced past 696.
--
-- Adding the column to the parent does not add it to the hot table: the hot
-- table is an independent heap with its own column list.
--
-- Idempotent: ADD COLUMN IF NOT EXISTS, wrapped in an existence guard so it is
-- a silent no-op on databases that do not have the hot table.

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_tables
    WHERE schemaname = 'public' AND tablename = 'request_logs_hot'
  ) THEN
    RAISE NOTICE '487b: public.request_logs_hot absent; nothing to add';
    RETURN;
  END IF;

  ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS system_fingerprint TEXT;

  COMMENT ON COLUMN public.request_logs_hot.system_fingerprint IS
    'Per-(credential, model) system fingerprint; mirrors request_logs.system_fingerprint (487) for the hot heap';
END
$$;

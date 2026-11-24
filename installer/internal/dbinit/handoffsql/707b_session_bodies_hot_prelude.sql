-- Migration 707b: create public.session_bodies_hot before 708 widens it.
--
-- Why this file exists (2026-10-10, host-first local deploy):
-- 708_session_bodies_s1a.sql does
--     ALTER TABLE public.session_bodies_hot ADD COLUMN IF NOT EXISTS kind ...
-- but session_bodies_hot is created by 614_session_bodies_hot.sql, which is
-- not in this sequence — on a normal host the gateway's Go ensure chain
-- creates the hot table during first boot. This sequence runs before that
-- chain has ever booted on a host-first deploy, so 708 aborted with
--     ERROR: relation "public.session_bodies_hot" does not exist
-- and the ledger never advanced, blocking every later file.
--
-- Same shape as 706b for session_turns_hot: derive the table from the live
-- public.session_bodies catalog (so names/types/defaults cannot drift) and
-- re-create the keys 614 declares under 614's names, because
-- CREATE TABLE ... LIKE copies the parent's keys under parent-derived names
-- and 614's own post-condition looks them up by 614's names.
--
-- Idempotent: guarded by an existence check on the table, and every rename is
-- skipped when the target name is already present.

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_tables
    WHERE schemaname = 'public' AND tablename = 'session_bodies_hot'
  ) THEN
    RAISE NOTICE '707b: public.session_bodies_hot already exists; nothing to do';
    RETURN;
  END IF;

  RAISE NOTICE '707b: creating public.session_bodies_hot from the current session_bodies shape';

  EXECUTE 'CREATE TABLE public.session_bodies_hot (LIKE public.session_bodies INCLUDING ALL) WITH (fillfactor=90)';
  EXECUTE 'ALTER TABLE public.session_bodies_hot ENABLE ROW LEVEL SECURITY';

  -- Keys, named as 614 declares them.
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_bodies_hot'::regclass AND conname='session_bodies_hot_pkey') THEN
    EXECUTE 'ALTER TABLE public.session_bodies_hot ADD CONSTRAINT session_bodies_hot_pkey PRIMARY KEY (id, partition_date)';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_bodies_hot'::regclass AND conname='session_bodies_hot_unique') THEN
    EXECUTE 'ALTER TABLE public.session_bodies_hot ADD CONSTRAINT session_bodies_hot_unique UNIQUE (session_id, turn_no, tenant_id, partition_date)';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_bodies_hot'::regclass AND conname='session_bodies_with_current_month') THEN
    EXECUTE 'ALTER TABLE public.session_bodies_hot ADD CONSTRAINT session_bodies_with_current_month UNIQUE (tenant_id, request_id, partition_date)';
  END IF;
END
$$;

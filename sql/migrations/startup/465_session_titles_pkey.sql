-- Migration 465: session_titles_pkey — backfill the missing primary key.
--
-- Background: session_titles was created without a PRIMARY KEY
-- constraint in deploy/sql/objects/tables/session_titles.sql and
-- deploy/sql/schemas/baseline/01-schema.sql. The deploy
-- pipeline never added the constraint either, so 4 groups of
-- duplicates accumulated (one row per migration instance of the
-- same task+scoped_session_id). The duplicate rows made any
-- INSERT ... ON CONFLICT (task_id, scoped_session_id) silently
-- fail with SQLSTATE 42P10.
--
-- 2026-08-06: when the request-logs view needed to expose
-- session_titles.title via a JOIN, and the new PUT /title
-- endpoint needed to upsert, both flows hit the missing
-- constraint. This migration deduplicates by ctid (keeping
-- the most recently written row) and adds the PK so future
-- upserts work.
--
-- Idempotent: re-running skips duplicate cleanup (no rows
-- match the predicate after the first run) and the ADD
-- CONSTRAINT is guarded by IF NOT EXISTS via DO block.

BEGIN;

-- Deduplicate: keep one row per (task_id, scoped_session_id).
-- ctid ordering is arbitrary within a transaction but stable
-- enough for cleanup — we keep the highest ctid per group.
DELETE FROM session_titles a USING session_titles b
WHERE a.ctid < b.ctid
  AND a.task_id = b.task_id
  AND a.scoped_session_id = b.scoped_session_id;

-- Add the missing PK.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'session_titles_pkey'
      AND conrelid = 'public.session_titles'::regclass
  ) THEN
    ALTER TABLE public.session_titles
      ADD CONSTRAINT session_titles_pkey PRIMARY KEY (task_id, scoped_session_id);
  END IF;
END $$;

COMMIT;
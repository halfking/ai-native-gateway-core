-- Migration 474: Apply deferred migrations 431 (indexes) + 432 (CHECK constraint)
--
-- Background:
--   431_session_turns_add_attachment_columns.sql + 431_task_default_routing_tenant_code.sql
--   share version 431. 431_task_default_routing_tenant_code was applied first (recorded
--   in schema_migrations as description='task_default_routing_tenant_code'), and the
--   deploy script's reconcile logic matched by description — so subsequent deploys
--   see 431_session_turns_add_attachment_columns as "already applied" via the
--   description cache and silently skip INSERT INTO schema_migrations.
--
--   Net result: public.session_turns got attachment_count / attachment_total_bytes /
--   multimodal_types COLUMNS (via other migration), but the 2 GIN/BTREE INDEXES
--   from 431 were never created:
--     - idx_session_turns_multimodal_types (GIN on multimodal_types)
--     - idx_session_turns_attachment_count (BTREE on attachment_count WHERE > 0)
--
--   Same pattern for 432: 432_route_incident_events_evidence_jsonb applied, but
--   432_fix_submit_mode_constraint silently skipped. The CHECK constraint on
--   public.session_turns.submit_mode was never extended to include
--   'attachment_only', so any INSERT with submit_mode='attachment_only'
--   will fail with constraint violation (code at domains/session/v2/turn_writer.go:57,
--   domains/session/v2/submit_mode_detector.go:15 all reference this mode).
--
-- This migration:
--   1. Creates the 2 missing GIN/BTREE indexes on public.session_turns (idempotent)
--   2. Drops + recreates session_turns_submit_mode_check on BOTH public + gateway
--      schemas with the full 5-mode list (full / delta / snapshot /
--      inferred_compressed / attachment_only)
--
-- Idempotent: IF NOT EXISTS / DROP IF EXISTS / DO block guard.

BEGIN;

-- 1. Indexes from migration 431 second file (public.session_turns only —
--    public.session_turns was never given the multimodal_types / attachment
--    columns in the original migration, so no index can be created on those.
--    Verified: public.session_turns has columns, public.session_turns does not.)
CREATE INDEX IF NOT EXISTS idx_session_turns_multimodal_types_gw
  ON public.session_turns USING gin(multimodal_types)
  WHERE multimodal_types != '{}';

CREATE INDEX IF NOT EXISTS idx_session_turns_attachment_count_gw
  ON public.session_turns(attachment_count)
  WHERE attachment_count > 0;

-- 2. CHECK constraint extension from migration 432 second file
ALTER TABLE public.session_turns
  DROP CONSTRAINT IF EXISTS session_turns_submit_mode_check;

ALTER TABLE public.session_turns
  ADD CONSTRAINT session_turns_submit_mode_check
  CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed', 'attachment_only'));

ALTER TABLE public.session_turns
  DROP CONSTRAINT IF EXISTS session_turns_submit_mode_check;

ALTER TABLE public.session_turns
  ADD CONSTRAINT session_turns_submit_mode_check
  CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed', 'attachment_only'));

-- 3. Verify the constraint contains exactly the five supported modes.
-- Checking only for attachment_only is insufficient: a malformed or stale
-- constraint could still contain that token while omitting another supported
-- mode or allowing an unintended value. Extract the quoted literals from the
-- rendered CHECK expression and compare the complete set on each schema.
DO $$
DECLARE
  expected text[] := ARRAY[
    'attachment_only', 'delta', 'full', 'inferred_compressed', 'snapshot'
  ];
  actual text[];
  relation_name regclass;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'public.session_turns'::regclass,
    'public.session_turns'::regclass
  ] LOOP
    SELECT COALESCE(array_agg(match[1] ORDER BY match[1]), ARRAY[]::text[])
      INTO actual
    FROM pg_constraint c
    CROSS JOIN LATERAL regexp_matches(
      pg_get_constraintdef(c.oid),
      $regex$'([^']+)'$regex$,
      'g'
    ) AS match
    WHERE c.conname = 'session_turns_submit_mode_check'
      AND c.conrelid = relation_name;

    IF actual IS DISTINCT FROM expected THEN
      RAISE EXCEPTION
        '% has unexpected submit_mode constraint values: %, expected %',
        relation_name, actual, expected;
    END IF;

    RAISE NOTICE 'submit_mode constraint OK on %: %', relation_name, actual;
  END LOOP;
END $$;

COMMIT;
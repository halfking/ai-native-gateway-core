-- 2026-07-06: SUPERSEDED by migration 354.
-- This migration references a 'sessions' master table that does not exist
-- in any branch of the codebase (sessions are tracked via session_summaries +
-- Redis). Migration 354 handles the actual safe equivalents:
--   - adds handoff_count + last_handoff_at columns to session_summaries
--   - creates handoff_logs table (the only NEW artifact this migration intended)
--
-- To prevent accidental re-application via the run-migrations script, this
-- file is intentionally a no-op: all original (broken) statements below the
-- BEGIN/COMMIT pair have been removed. The file is preserved so historical
-- version numbers stay stable (schema_migrations never recorded this number
-- on production anyway, since it has never been applied successfully).
--
-- Detect-and-skip: scripts/deploy.sh run_migrations_184() parses the first
-- 10 lines of each .sql file. The "SUPERSEDED" marker in line 1 of this
-- file causes the script to skip applying it.
--
-- If you need to revive the ORIGINAL schema design (a master `sessions`
-- table), DO NOT do it via this migration. Instead:
--   1. Review commit history of this migration to see what was originally
--      intended.
--   2. Build the sessions table via a NEW migration (e.g. 355_sessions.sql).
--   3. Migrate any existing data from session_summaries + Redis to it.
--   4. Then update trigger_hook.go (currently in
--      _to-be-deprecated/hooks-handoff-20260706/) to use the new schema.

BEGIN;
-- no-op: see header comment
COMMIT;

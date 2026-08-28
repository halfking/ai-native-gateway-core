-- V360__credential_probe_queue_automatic.down.sql
-- Rollback for V360: Drop the `automatic` column and its partial claim index
-- from credential_probe_queue, restoring the pre-V360 schema.
--
-- Mirror of V360__credential_probe_queue_automatic.sql (forward):
--   forward added   ADD COLUMN IF NOT EXISTS automatic BOOLEAN
--                    CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_automatic_claim
--   this rollback   DROP INDEX IF EXISTS (must precede DROP COLUMN; safe either order here
--                    because IF EXISTS swallows the absent case)
--                    DROP COLUMN IF EXISTS automatic
--
-- Idempotent: every statement uses IF EXISTS / IF NOT EXISTS, so re-running
-- this script after a partial rollback is safe.
--
-- Reverse-safety note:
--   The automatic gating logic in bg/probe_queue.go and bg/probe_service.go
--   reads `q.automatic` from this column. If this rollback is applied to a
--   live gateway, those queries will fail until the gateway is reverted to
--   a build that predates V360. Do NOT run this on a production gateway that
--   is still running post-V360 code.

\set ON_ERROR_STOP on

\echo '=== V360 DOWN: drop automatic column + partial claim index ==='

DROP INDEX IF EXISTS public.idx_credential_probe_queue_automatic_claim;

ALTER TABLE public.credential_probe_queue
    DROP COLUMN IF EXISTS automatic;

\echo '=== V360 DOWN complete ==='

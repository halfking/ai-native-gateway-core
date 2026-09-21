-- Migration 600: Body Storage Phase 1 — outbound_body routes to bodies_hot only
--
-- Purpose:
--   Eliminate duplicate TOAST/WAL/storage cost of outbound_body on request_logs_hot.
--   The dedicated request_logs_bodies_hot side table is the sole outbound_body
--   owner; outbound_body is written via upsertRequestLogBodies in the same tx.
--
-- 2026-08-24 phase 1 contract (alignment with code change in client.go):
--   1. New writes stop populating request_logs_hot.outbound_body. The legacy
--      column remains during the compatibility window because older readers
--      and rollback releases still reference it.
--   2. request_logs_bodies_hot gains tenant_id so admin/data-lifecycle blob
--      cleanup can target a tenant's bodies without joining back to
--      request_logs_hot per row.
--
-- Migration steps:
--   (1) Add and backfill request_logs_bodies_hot.tenant_id from request_logs_hot for
--       every existing row (one-shot UPDATE keyed by request_id; the hot body
--       table uses request_id as its unique key after migration 455).
--   (2) Keep request_logs_hot.outbound_body nullable for compatibility.
--
-- Down migration: no destructive schema rollback is required. Reverting the
-- application restores future writes to the legacy column; tenant_id remains
-- additive on request_logs_bodies_hot.

\set ON_ERROR_STOP on

BEGIN;

-- ---------------------------------------------------------------------------
-- Step 1: Add tenant_id to the independent heap hot table. It is not a
--         column of the partitioned archive parent because lifecycle cleanup
--         targets only the hot table and the archive view has a frozen five-
--         column projection.
-- ---------------------------------------------------------------------------

ALTER TABLE public.request_logs_bodies_hot
    ADD COLUMN IF NOT EXISTS tenant_id text;

-- ---------------------------------------------------------------------------
-- Step 2: Backfill tenant_id from the main table. request_id+ts is the PK
--          on the side table; the join is deterministic.
-- ---------------------------------------------------------------------------

UPDATE public.request_logs_bodies_hot AS rb
   SET tenant_id = rl.tenant_id
  FROM public.request_logs_hot AS rl
 WHERE rl.request_id = rb.request_id
   AND rb.tenant_id IS NULL;

-- ---------------------------------------------------------------------------
-- Step 2: Keep tenant_id nullable for legacy orphan body rows. A later
--         evidence-backed migration may tighten this after coverage is known.
--
-- request_logs_hot.outbound_body is intentionally retained. All new writes
-- route outbound_body to request_logs_bodies_hot, while existing SQL readers
-- remain backward-compatible until their migrations are separately completed.
--
-- Step 3: Drop NOT NULL candidate constraint for now (some legacy rows may
--          not have a matching main row). In the next phase we will tighten
--          to NOT NULL once the join coverage is verified.
-- ---------------------------------------------------------------------------

-- Verification queries (run manually after migration):
--   SELECT count(*) FROM public.request_logs_hot
--     WHERE outbound_body IS NOT NULL;          -- legacy rows only
--   SELECT count(*) FROM public.request_logs_bodies_hot
--     WHERE outbound_body IS NOT NULL
--       AND tenant_id IS NULL;                  -- expect 0 for matched rows
-- ---------------------------------------------------------------------------

COMMIT;

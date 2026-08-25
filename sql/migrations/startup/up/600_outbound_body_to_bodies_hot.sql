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
--   2. request_logs_bodies_hot stores only body payloads. Tenant and user
--      metadata remains owned by request_logs_hot and is resolved by request_id.
--
-- Migration steps:
--   (1) Keep request_logs_hot.outbound_body nullable for compatibility.
--
-- Down migration: no destructive schema rollback is required. Reverting the
-- application restores future writes to the legacy column.

\set ON_ERROR_STOP on

BEGIN;

-- request_logs_hot.outbound_body is intentionally retained. All new writes
-- route outbound_body to request_logs_bodies_hot, while existing SQL readers
-- remain backward-compatible until their migrations are separately completed.

-- Verification queries (run manually after migration):
--   SELECT count(*) FROM public.request_logs_hot
--     WHERE outbound_body IS NOT NULL;          -- legacy rows only
--   SELECT count(*) FROM public.request_logs_bodies_hot
--     WHERE outbound_body IS NOT NULL;          -- body rows only

COMMIT;

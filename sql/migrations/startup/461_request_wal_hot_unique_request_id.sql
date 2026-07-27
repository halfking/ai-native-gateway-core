-- Migration 461: request_wal_hot unique index on request_id (one row per request)
--
-- Background (2026-07-27 audit of commit 7d07c58f / L-1):
--   request_wal_hot's primary key is (request_id, created_at). CreateInitial
--   writes a row with created_at = NOW() and `ON CONFLICT (request_id,
--   created_at) DO NOTHING`. L-1 added an EARLY CreateInitial call at request
--   arrival (handler.go ~985) in addition to the existing later, fuller call
--   (~2402). Because each call runs its own Exec, NOW()/transaction_timestamp
--   differs between the two statements, so (request_id, created_at) almost
--   never collides. The second INSERT therefore succeeds, leaving an ORPHANED
--   early row stuck at status='pending', tenant_id='default' forever. The
--   commit comment claimed "the later CreateInitial is a no-op" — that only
--   holds when created_at matches, which it does not across separate Execs.
--
-- Fix:
--   Enforce the true invariant — one WAL row per request — with a UNIQUE
--   index on request_id alone. CreateInitial then switches to
--   `ON CONFLICT (request_id) DO UPDATE` so the later, fuller call enriches
--   the early row instead of creating a second one. The UPDATE only writes
--   non-empty / non-default values (COALESCE first-write-wins), so the early
--   row's created_at is preserved and enrichment is additive.
--
-- Pre-existing duplicates:
--   If orphaned rows already exist (created by L-1 before this migration),
--   the UNIQUE index creation would fail on them. We dedupe first: keep the
--   newest row per request_id (the one with the real tenant/stage), delete
--   older orphaned 'pending' rows. This is safe because the canonical
--   request lifecycle is tracked in request_logs_hot; request_wal_hot is a
--   best-effort operator drill-down table.

BEGIN;

-- 1. Dedupe pre-existing duplicate request_id rows (keep newest created_at).
--    Only runs if duplicates exist; idempotent.
DELETE FROM request_wal_hot w
WHERE ctid IN (
    SELECT ctid FROM (
        SELECT ctid,
               ROW_NUMBER() OVER (
                   PARTITION BY request_id
                   ORDER BY created_at DESC
               ) AS rn
        FROM request_wal_hot
    ) t
    WHERE rn > 1
);

-- 2. Unique index on request_id: one WAL row per request, enforced by the DB.
CREATE UNIQUE INDEX IF NOT EXISTS udx_request_wal_hot_request_id
    ON request_wal_hot (request_id);

COMMENT ON INDEX udx_request_wal_hot_request_id IS
    'Enforces one request_wal_hot row per request so the early (arrival) and '
    'later (post-routing) CreateInitial calls collapse onto the same row '
    'instead of orphaning a pending row. Added by migration 461 (2026-07-27).';

COMMIT;

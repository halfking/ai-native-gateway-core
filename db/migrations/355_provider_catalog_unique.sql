-- Migration 355: provider_catalog uniqueness + cleanup duplicate rows
--
-- 2026-07-07: provider_catalog has no UNIQUE constraint on `code`, so
-- `ON CONFLICT (code) DO UPDATE` (in admin/free_pool_extra.go and
-- admin/routing.go::registerFreeProvider) silently falls through and
-- inserts duplicate rows every time. By 2026-07-07 the production DB
-- had 74 rows for 37 unique codes (every code doubled).
--
-- Symptom: pgxpool.Pool.QueryRow(...).Scan() on the getProvider SQL
-- (admin/providers.go:672) returns ErrNoRows because the `pc.code =
-- p.catalog_code` LEFT JOIN expands to N rows when N duplicates exist
-- for the same code. Handler translates the error to 404.
--
-- Fix:
--   1. Deduplicate (keep the earliest row by ctid) — preserves any
--      legitimate per-row edits that happened to the later copy.
--   2. Add UNIQUE INDEX on (code) so future ON CONFLICT (code) calls
--      actually do something (previously they were no-ops).
--
-- Note: this migration only touches the unique constraint and the
-- catalogue-row table. The separate getProvider bug (// comment in
-- SQL template, admin/providers.go:726) is fixed in the Go code.

BEGIN;

-- 1. Deduplicate provider_catalog, keep earliest row per code.
--    ROW_NUMBER() + ctid tiebreak is deterministic and O(n log n).
WITH ranked AS (
  SELECT ctid,
         ROW_NUMBER() OVER (PARTITION BY code ORDER BY ctid) AS rn
  FROM provider_catalog
)
DELETE FROM provider_catalog pc
USING ranked
WHERE pc.ctid = ranked.ctid AND ranked.rn > 1;

-- 2. Enforce uniqueness going forward. UNIQUE INDEX (not constraint)
--    so we can add it concurrently in production without blocking
--    reads. (CREATE UNIQUE INDEX CONCURRENTLY can NOT run inside a
--    transaction block, so the production fix was two steps: DELETE
--    first, then CREATE INDEX CONCURRENTLY.)
--
-- Cannot be CONCURRENTLY here because the migration runner wraps the
-- whole file in a transaction. For prod, the operator ran it as two
-- separate statements.
CREATE UNIQUE INDEX IF NOT EXISTS provider_catalog_code_key
  ON provider_catalog (code);

-- 3. Sanity: the count should now equal the count of distinct codes.
--    If it doesn't, the migration is unsafe to commit.
DO $$
DECLARE
  total_count BIGINT;
  distinct_count BIGINT;
BEGIN
  SELECT COUNT(*) INTO total_count FROM provider_catalog;
  SELECT COUNT(DISTINCT code) INTO distinct_count FROM provider_catalog;
  IF total_count <> distinct_count THEN
    RAISE EXCEPTION 'provider_catalog still has duplicates: total=%, distinct=%',
      total_count, distinct_count;
  END IF;
END $$;

COMMIT;

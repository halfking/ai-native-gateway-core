BEGIN;

-- Migration 455: Hot tables UNIQUE constraint change (request_id, ts) → (request_id)
--
-- Background:
--   request_logs_hot and request_logs_bodies_hot previously used (request_id, ts)
--   as composite unique key. This caused two problems:
--     1. Subquery-based bodies INSERT could return 0 rows under concurrent access
--     2. Same request_id could have multiple rows, making UPDATEs non-deterministic
--
-- Solution:
--   Change both hot tables to use request_id as the unique key.
--   request_id is server-generated (middleware/requestid_mw.go) and globally unique.
--   This simplifies all INSERT/UPDATE code paths and eliminates the subquery bug.
--
-- Note: The partitioned tables (request_logs, request_logs_bodies) retain their
-- (request_id, ts) composite key because they store data from all months and
-- ts is needed for partition routing.
--
-- Author: ACC team (2026-07-23)

-- ============================================================
-- 1. request_logs_hot: Drop composite PK, add PK on request_id
-- ============================================================

DO $$
DECLARE
  pk_exists boolean;
  dup_count bigint;
BEGIN
  -- Check if the old PK exists
  SELECT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'request_logs_hot_pkey'
    AND conrelid = 'request_logs_hot'::regclass
  ) INTO pk_exists;

  IF NOT pk_exists THEN
    RAISE NOTICE 'request_logs_hot_pkey already removed, skipping';
    RETURN;
  END IF;

  -- Step 1: Deduplicate — keep the row with latest ts for each request_id
  WITH dupes AS (
    SELECT request_id
    FROM request_logs_hot
    GROUP BY request_id
    HAVING COUNT(*) > 1
  )
  SELECT COUNT(*) INTO dup_count FROM dupes;
  RAISE NOTICE 'Found % duplicate request_ids in request_logs_hot', dup_count;

  DELETE FROM request_logs_hot
  WHERE (request_id, ts) IN (
    SELECT request_id, ts FROM (
      SELECT request_id, ts,
        ROW_NUMBER() OVER (PARTITION BY request_id ORDER BY ts DESC) AS rn
      FROM request_logs_hot
    ) sub
    WHERE rn > 1
  );
  RAISE NOTICE 'Deduplicated request_logs_hot, kept latest row per request_id';

  -- Step 2: Drop the composite primary key
  ALTER TABLE request_logs_hot DROP CONSTRAINT request_logs_hot_pkey;
  RAISE NOTICE 'Dropped request_logs_hot_pkey constraint';

  -- Step 3: Drop the old unique index
  DROP INDEX IF EXISTS idx_request_logs_hot_request_id_ts_unique;

  -- Step 4: Add new primary key on request_id alone
  ALTER TABLE request_logs_hot ADD PRIMARY KEY (request_id);
  RAISE NOTICE 'Added PRIMARY KEY (request_id) on request_logs_hot';
END $$;

-- ============================================================
-- 2. request_logs_bodies_hot: Drop composite UNIQUE, add UNIQUE on request_id
-- ============================================================

DO $$
DECLARE
  idx_exists boolean;
  dup_count bigint;
BEGIN
  -- Check if the old unique index exists
  SELECT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE indexname = 'idx_request_logs_bodies_hot_request_id_ts_unique'
    AND tablename = 'request_logs_bodies_hot'
  ) INTO idx_exists;

  IF NOT idx_exists THEN
    RAISE NOTICE 'idx_request_logs_bodies_hot_request_id_ts_unique already removed, skipping';
    RETURN;
  END IF;

  -- Step 1: Deduplicate — keep the row with latest ts for each request_id
  WITH dupes AS (
    SELECT request_id
    FROM request_logs_bodies_hot
    GROUP BY request_id
    HAVING COUNT(*) > 1
  )
  SELECT COUNT(*) INTO dup_count FROM dupes;
  RAISE NOTICE 'Found % duplicate request_ids in request_logs_bodies_hot', dup_count;

  DELETE FROM request_logs_bodies_hot
  WHERE (request_id, ts) IN (
    SELECT request_id, ts FROM (
      SELECT request_id, ts,
        ROW_NUMBER() OVER (PARTITION BY request_id ORDER BY ts DESC) AS rn
      FROM request_logs_bodies_hot
    ) sub
    WHERE rn > 1
  );
  RAISE NOTICE 'Deduplicated request_logs_bodies_hot, kept latest row per request_id';

  -- Step 2: Drop the old unique index
  DROP INDEX IF EXISTS idx_request_logs_bodies_hot_request_id_ts_unique;
  RAISE NOTICE 'Dropped idx_request_logs_bodies_hot_request_id_ts_unique';

  -- Step 3: Add new unique index on request_id alone
  CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_bodies_hot_request_id
    ON request_logs_bodies_hot (request_id);
  RAISE NOTICE 'Added UNIQUE (request_id) on request_logs_bodies_hot';
END $$;

-- ============================================================
-- 3. Update promote functions: simplify DELETE WHERE clause
-- ============================================================

-- promote_request_logs_hot_to_partition already uses WHERE id IN (...)
-- which does not depend on the (request_id, ts) composite key. No change needed.

-- promote_request_logs_bodies_hot_to_partition uses WHERE (request_id, ts) IN (...)
-- This still works (request_id is now unique, so (request_id, ts) pairs are still
-- unique), but we simplify it to use WHERE request_id IN (...) for clarity.

CREATE OR REPLACE FUNCTION promote_request_logs_bodies_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  v_moved bigint := 0;
BEGIN
  WITH batch AS (
    SELECT request_id, ts, request_body, outbound_body, response_body
    FROM request_logs_bodies_hot
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size
  ),
  deleted AS (
    DELETE FROM request_logs_bodies_hot
    WHERE request_id IN (SELECT request_id FROM batch)
    RETURNING *
  )
  INSERT INTO request_logs_bodies (request_id, ts, request_body, outbound_body, response_body)
  SELECT request_id, ts, request_body, outbound_body, response_body FROM deleted;

  GET DIAGNOSTICS v_moved = ROW_COUNT;
  RETURN v_moved;
END;
$$;

-- ============================================================
-- 4. Verify
-- ============================================================

DO $$
DECLARE
  hot_pk_cols text;
  bodies_uk_cols text;
BEGIN
  -- Verify request_logs_hot primary key is on request_id only
  SELECT string_agg(pa.attname::text, ', ' ORDER BY pa.attname)
  INTO hot_pk_cols
  FROM pg_index pi
  JOIN pg_attribute pa ON pa.attrelid = pi.indrelid AND pa.attnum = ANY(pi.indkey)
  WHERE pi.indrelid = 'request_logs_hot'::regclass
    AND pi.indisprimary;

  RAISE NOTICE 'request_logs_hot PK columns: %', hot_pk_cols;

  -- Verify request_logs_bodies_hot unique index is on request_id only
  SELECT string_agg(pa.attname::text, ', ' ORDER BY pa.attname)
  INTO bodies_uk_cols
  FROM pg_index pi
  JOIN pg_attribute pa ON pa.attrelid = pi.indrelid AND pa.attnum = ANY(pi.indkey)
  WHERE pi.indrelid = 'request_logs_bodies_hot'::regclass
    AND pi.indisunique;

  RAISE NOTICE 'request_logs_bodies_hot UNIQUE columns: %', bodies_uk_cols;
END $$;

COMMIT;

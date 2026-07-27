-- Migration 461: make request_wal_hot idempotent by request_id.
--
-- CreateInitial is called at request arrival and again after routing with
-- enriched fields. A timestamp-based primary key gives those calls different
-- rows, leaving the early pending row orphaned. Keep the newest row per request
-- and use request_id as the single-row identity, matching the hot request-log
-- tables.

BEGIN;

DO $$
DECLARE
  pk_name text;
  pk_cols text;
BEGIN
  SELECT c.conname, string_agg(a.attname, ',' ORDER BY k.n)
    INTO pk_name, pk_cols
  FROM pg_constraint c
  JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, n) ON true
  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
  WHERE c.conrelid = 'request_wal_hot'::regclass
    AND c.contype = 'p'
  GROUP BY c.conname;

  -- Deduplicate before adding or changing the primary key. This also handles
  -- installations where the hot table was created without a primary key.
  DELETE FROM request_wal_hot
  WHERE ctid IN (
    SELECT ctid
    FROM (
      SELECT ctid, row_number() OVER (PARTITION BY request_id ORDER BY created_at DESC) AS rn
      FROM request_wal_hot
    ) ranked
    WHERE rn > 1
  );

  IF pk_name IS NULL THEN
    ALTER TABLE request_wal_hot ADD PRIMARY KEY (request_id);
  ELSIF pk_cols <> 'request_id' THEN
    EXECUTE format('ALTER TABLE request_wal_hot DROP CONSTRAINT %I', pk_name);
    ALTER TABLE request_wal_hot ADD PRIMARY KEY (request_id);
  END IF;
END $$;

COMMIT;

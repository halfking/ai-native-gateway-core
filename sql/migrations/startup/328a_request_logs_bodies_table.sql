-- Migration 328a: Add request_logs_bodies (body-table split — phase 1)
--
-- Background:
--   As of 2026-07-02, request_logs holds three large JSONB columns
--   (request_body 170 KB/row, outbound_body 51 KB/row, response_body 902 B/row)
--   that prevent the table from joining the columnar invariant.
--   Migration 318b explicitly built request_logs_archive as heap because
--   Citus columnar can't serialize multi-MB JSONB in a single stripe buffer.
--
-- This migration (phase 1):
--   - Creates request_logs_bodies sibling table, partitioned by RANGE(ts).
--   - Adds ensure_request_logs_bodies_partition() paired with
--     ensure_request_logs_partition() in bg/partition_manager.go.
--   - Adds backfill_request_logs_bodies() stored procedure that backfills
--     rows in independent transactions (one batch per CALL) so memory is
--     bounded regardless of how large individual JSONB rows are.
--
-- Phase 2 (Go change, separate PR):
--   - PersistRequestLog dual-writes bodies to request_logs_bodies.
--
-- Phase 3 (migration 328b, separate file):
--   - DROP COLUMN request_body / outbound_body / response_body on request_logs.
--   - Run columnar_heal() to convert metadata partitions to columnar.
--
-- Why split:
--   request_logs_default 1.2 GB + request_logs_archive_2026_06 2.5 GB is
--   recoverable. A columnar metadata table shrinks to ~ 60 MB (40×). The
--   body table itself is also columnar-eligible (zstd compression on JSONB
--   is excellent, typically 5–10×). Total saving: ~ 75 %.
--
-- Idempotent: re-running is safe.
--
-- Author: llm-gateway-ops (2026-07-02)

-- ============================================================
-- 0. Pre-flight
-- ============================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = 'request_logs'
                     AND relnamespace = 'public'::regnamespace) THEN
        RAISE EXCEPTION 'request_logs parent not found';
    END IF;
END
$$;

-- ============================================================
-- 1. Create request_logs_bodies (partitioned RANGE(ts))
-- ============================================================

CREATE TABLE IF NOT EXISTS public.request_logs_bodies (
    request_id    text                     NOT NULL,
    ts            timestamp with time zone NOT NULL DEFAULT now(),
    request_body  jsonb,
    outbound_body jsonb,
    response_body jsonb,
    PRIMARY KEY (request_id, ts)
) PARTITION BY RANGE (ts);

COMMENT ON TABLE public.request_logs_bodies IS
'Sibling of request_logs holding the three large JSONB body columns
(request_body, outbound_body, response_body). Split out by migration
328a (2026-07-02) so request_logs can join the columnar invariant
without overflowing Citus columnar 1 GB serialization buffer.';

-- ============================================================
-- 2. ensure_request_logs_bodies_partition() — paired with
--    ensure_request_logs_partition() in bg/partition_manager.go.
--    Listed below alongside the other ensure_* to make the
--    invariant visible alongside phase 23.
-- ============================================================

CREATE OR REPLACE FUNCTION ensure_request_logs_bodies_partition(target_ts timestamptz DEFAULT now())
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_bodies
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as columnar', partition_name;
    END IF;
END;
$$;

COMMENT ON FUNCTION ensure_request_logs_bodies_partition(timestamptz) IS
'Ensure monthly partition for request_logs_bodies (columnar).
Paired with ensure_request_logs_partition(); both are called from
bg.PartitionManager. Added 2026-07-02 by migration 328a.';

-- ============================================================
-- 3. Ensure partitions exist for current ± 1 month
-- ============================================================

SELECT ensure_request_logs_bodies_partition((now() - interval '1 month')::timestamptz);
SELECT ensure_request_logs_bodies_partition(now());
SELECT ensure_request_logs_bodies_partition((now() + interval '1 month')::timestamptz);

-- ============================================================
-- 4. Backfill stored procedure
--
-- The procedure backfills one batch and exits. Run it repeatedly
-- via:
--   CALL backfill_request_logs_bodies(200);
-- Each CALL is its own implicit transaction so memory is released
-- between batches. Trigger callers (cron / DBA) loop until the
-- procedure returns rows_inserted=0.
-- ============================================================

CREATE OR REPLACE PROCEDURE backfill_request_logs_bodies(p_batch int DEFAULT 200)
LANGUAGE plpgsql AS $$
DECLARE
    rec record;
    inserted int := 0;
BEGIN
    FOR rec IN
        SELECT id
        FROM request_logs
        WHERE (request_body IS NOT NULL OR outbound_body IS NOT NULL OR response_body IS NOT NULL)
          AND NOT EXISTS (
              SELECT 1 FROM request_logs_bodies b
              WHERE b.request_id = request_logs.request_id AND b.ts = request_logs.ts)
        ORDER BY id
        LIMIT p_batch
    LOOP
        INSERT INTO request_logs_bodies (request_id, ts, request_body, outbound_body, response_body)
        SELECT request_id, ts, request_body, outbound_body, response_body
        FROM request_logs
        WHERE id = rec.id;
        inserted := inserted + 1;
    END LOOP;

    RAISE NOTICE 'backfill_request_logs_bodies: inserted % rows (batch %)',
        inserted, p_batch;
END;
$$;

COMMENT ON PROCEDURE backfill_request_logs_bodies(int) IS
'Backfill one batch of body rows from request_logs to request_logs_bodies.
Each CALL is its own implicit transaction (memory bounded). Idempotent: rows
already present are skipped via NOT EXISTS. Added 2026-07-02 by migration 328a.';

-- Helper view: current backfill progress
CREATE OR REPLACE VIEW request_logs_bodies_progress AS
SELECT
    (SELECT count(*) FROM request_logs
       WHERE request_body IS NOT NULL OR outbound_body IS NOT NULL OR response_body IS NOT NULL) AS source_rows_with_body,
    (SELECT count(*) FROM request_logs_bodies) AS bodies_rows,
    (SELECT count(*) FROM request_logs
       WHERE (request_body IS NOT NULL OR outbound_body IS NOT NULL OR response_body IS NOT NULL)
         AND NOT EXISTS (
             SELECT 1 FROM request_logs_bodies b
             WHERE b.request_id = request_logs.request_id AND b.ts = request_logs.ts)) AS rows_pending_backfill;

COMMENT ON VIEW request_logs_bodies_progress IS
'Live progress of body-table backfill: how many rows still need migration.
Returned by the daily cron. Added 2026-07-02 by migration 328a.';

-- ============================================================
-- 5. NOTE: We intentionally do NOT run the backfill here.
--    The first run is executed from scripts/backfill-bodies.sh
--    (one CALL per loop, committing per batch) so memory stays
--    bounded even for multi-MB JSONB rows.
-- ============================================================

\echo ''
\echo 'Migration 328a complete:'
\echo '  request_logs_bodies table created, partitioned, columnar'
\echo '  ensure_request_logs_bodies_partition() installed (paired with request_logs)'
\echo '  backfill_request_logs_bodies(batch) procedure installed'
\echo '  request_logs_bodies_progress view installed'
\echo ''
\echo 'Next steps:'
\echo '  - bash scripts/backfill-bodies.sh 184     # drain source rows in batches'
\echo '  - register ensure_request_logs_bodies_partition() in bg/partition_manager.go::ensureSpecs()'
\echo '  - add dual-write INSERT in client.go::PersistRequestLog'
\echo '  - add JOIN reads in admin/logs.go detail drawer'
\echo '  - rerun columnar_heal() after migration 328b drops body columns'

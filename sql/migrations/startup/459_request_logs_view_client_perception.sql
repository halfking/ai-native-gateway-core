-- Migration 459: Rebuild request_logs_with_current_month to expose client
-- perception columns (agent_name / agent_type / client_protocol / canonical_model)
-- so /api/logs/{id} stops failing with "query failed" after ingestion adopts
-- 443 + 458.
--
-- Date: 2026-07-27
--
-- Root cause:
--   V350 / 443 / 458 ADD COLUMN on request_logs_hot + request_logs, but
--   Postgres views freeze the column list at CREATE OR REPLACE time. Once
--   448 recreated the view (adding routing_attempts / routing_summary),
--   no automatic propagation picks up the four new columns.
--   → admin.handleLogs → getLog SELECTs rl.canonical_model / rl.agent_name /
--     rl.agent_type / rl.client_protocol, which fail with
--     SQLSTATE 42703 (column does not exist), and the handler returns
--     {"error": {"detail": "query failed", "db_error": "..."}}.
--   (Mirror image of the 448 bug, documented at
--   sql/migrations/startup/448_request_logs_view_routing_attempts.sql.)
--
-- Strategy:
--   Reuse the 448 pattern: read the view's existing column list, append the
--   four new columns, and recreate the UNION ALL view. This keeps us safe
--   from the historical hot/parent type drift (customer_id text vs bigint,
--   etc.) because we never build the column list from scratch.
--
-- Idempotent: YES — early-return when the view already exposes the four
-- columns, otherwise recreate.

BEGIN;

-- 1. Ensure the four columns exist on both sides (in case 443/458 did not
--    run on this deployment). ADD COLUMN IF NOT EXISTS is idempotent.
ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS agent_name      VARCHAR(255),
    ADD COLUMN IF NOT EXISTS agent_type      VARCHAR(50),
    ADD COLUMN IF NOT EXISTS client_protocol VARCHAR(50),
    ADD COLUMN IF NOT EXISTS canonical_model TEXT;

ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS agent_name      VARCHAR(255),
    ADD COLUMN IF NOT EXISTS agent_type      VARCHAR(50),
    ADD COLUMN IF NOT EXISTS client_protocol VARCHAR(50),
    ADD COLUMN IF NOT EXISTS canonical_model TEXT;

-- 2. Rebuild the union view with the new columns. The block is a copy of
--    448's append-pattern, parameterized on the four target columns.
DO $$
DECLARE
  new_cols text[] := ARRAY['agent_name', 'agent_type', 'client_protocol', 'canonical_model'];
  base_cols text;
  final_cols text;
  missing_count int;
  view_exists boolean;
  vc record;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_logs_with_current_month'
  ) INTO view_exists;

  IF view_exists THEN
    SELECT count(*) INTO missing_count
      FROM unnest(new_cols) nc
     WHERE NOT EXISTS (
       SELECT 1
         FROM information_schema.columns
        WHERE table_name = 'request_logs_with_current_month'
          AND column_name = nc
     );

    IF missing_count = 0 THEN
      RAISE NOTICE '459: VIEW already exposes 4 new columns — skip recreate';
      RETURN;
    END IF;

    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON a.attrelid = c.oid
     WHERE c.relname = 'request_logs_with_current_month'
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '459: existing VIEW has no columns';
    END IF;
    final_cols := base_cols || ', ' ||
                  array_to_string(new_cols, ', ');
  ELSE
    -- Cold-start fallback: same schema-drift protection as 448 — only
    -- columns present on BOTH sides with matching types.
    SELECT string_agg(quote_ident(h.attname), ', ' ORDER BY h.attnum)
      INTO base_cols
      FROM pg_attribute h
      JOIN pg_class ch ON h.attrelid = ch.oid
      JOIN pg_attribute p ON p.attname = h.attname
      JOIN pg_class cp ON p.attrelid = cp.oid
     WHERE ch.relname = 'request_logs_hot'
       AND cp.relname = 'request_logs'
       AND h.attnum > 0 AND NOT h.attisdropped
       AND p.attnum > 0 AND NOT p.attisdropped
       AND h.atttypid = p.atttypid
       AND h.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '459: cannot build fallback column list';
    END IF;
    final_cols := base_cols || ', ' ||
                  array_to_string(new_cols, ', ');
  END IF;

  EXECUTE 'DROP VIEW IF EXISTS request_logs_with_current_month';
  EXECUTE format($sql$
    CREATE VIEW request_logs_with_current_month AS
    SELECT %s FROM request_logs_hot
    UNION ALL
    SELECT %s FROM request_logs
  $sql$, final_cols, final_cols);

  COMMENT ON VIEW request_logs_with_current_month IS
    'Hot + monthly partitions UNION. Recreated by migration 459 (2026-07-27) '
    'to expose agent_name / agent_type / client_protocol / canonical_model '
    'after ADD COLUMN via migrations 443 + 458. Preserves prior VIEW column '
    'set to avoid hot/parent type drift (see 448).';

  -- 3. Verify the four columns are now visible via the view.
  FOR vc IN
    SELECT unnest(new_cols) AS col
  LOOP
    IF NOT EXISTS (
      SELECT 1
        FROM information_schema.columns
       WHERE table_name = 'request_logs_with_current_month'
         AND column_name = vc.col
    ) THEN
      RAISE EXCEPTION '459: VIEW missing column % after recreate', vc.col;
    END IF;
  END LOOP;

  -- Smoke: select the four columns to confirm the view is queryable.
  PERFORM agent_name, agent_type, client_protocol, canonical_model
    FROM request_logs_with_current_month
   LIMIT 1;

  RAISE NOTICE 'Migration 459 completed: VIEW exposes 4 client-perception columns';
END $$;

COMMIT;

-- Migration 561: Expose origin_actor on request_logs_with_current_month
--
-- Date: 2026-08-22
--
-- Purpose
-- ───────
-- request_logs_hot / request_logs have origin_actor (341) but the UNION view
-- was never rebuilt to include it. Admin session turns tree child query needs
-- origin_actor fallback when request_type is still 'main' on legacy rows.
--
-- Idempotent: YES (IF NOT EXISTS append + view early-return)
-- Down: 561_request_logs_view_origin_actor.down.sql

BEGIN;

DO $$
DECLARE
  new_cols text[] := ARRAY['origin_actor'];
  base_cols text;
  final_cols text;
  missing_count int;
  view_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views
     WHERE viewname = 'request_logs_with_current_month'
       AND schemaname = 'public'
  ) INTO view_exists;

  IF view_exists THEN
    SELECT count(*) INTO missing_count
      FROM unnest(new_cols) nc
     WHERE NOT EXISTS (
       SELECT 1
         FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'request_logs_with_current_month'
          AND column_name = nc
     );

    IF missing_count = 0 THEN
      RAISE NOTICE '561: VIEW already exposes origin_actor — skip recreate';
      RETURN;
    END IF;

    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON a.attrelid = c.oid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname = 'request_logs_with_current_month'
       AND c.relkind = 'v'
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '561: existing VIEW has no columns';
    END IF;
    final_cols := base_cols || ', ' || array_to_string(new_cols, ', ');
  ELSE
    SELECT string_agg(quote_ident(h.attname), ', ' ORDER BY h.attnum)
      INTO base_cols
      FROM pg_attribute h
      JOIN pg_class ch ON h.attrelid = ch.oid
      JOIN pg_namespace nh ON nh.oid = ch.relnamespace
      JOIN pg_attribute p ON p.attname = h.attname
      JOIN pg_class cp ON p.attrelid = cp.oid
      JOIN pg_namespace np ON np.oid = cp.relnamespace
     WHERE nh.nspname = 'public' AND ch.relname = 'request_logs_hot'
       AND np.nspname = 'public' AND cp.relname = 'request_logs'
       AND h.attnum > 0 AND NOT h.attisdropped
       AND p.attnum > 0 AND NOT p.attisdropped
       AND h.atttypid = p.atttypid
       AND h.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '561: cannot build fallback column list';
    END IF;
    final_cols := base_cols || ', ' || array_to_string(new_cols, ', ');
  END IF;

  EXECUTE 'DROP VIEW IF EXISTS request_logs_with_current_month';
  EXECUTE format($sql$
    CREATE VIEW request_logs_with_current_month AS
    SELECT %s FROM request_logs_hot
    UNION ALL
    SELECT %s FROM request_logs
  $sql$, final_cols, final_cols);

  COMMENT ON VIEW request_logs_with_current_month IS
    'Hot + monthly partitions UNION. Recreated by migration 561 (2026-08-22) '
    'to expose origin_actor for session turns tree child request_type fallback. '
    'Preserves prior VIEW column set (see 448/459/491/510/532).';

  IF NOT EXISTS (
    SELECT 1
      FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'request_logs_with_current_month'
       AND column_name = 'origin_actor'
  ) THEN
    RAISE EXCEPTION '561: VIEW missing column origin_actor after recreate';
  END IF;

  PERFORM origin_actor FROM request_logs_with_current_month LIMIT 1;

  RAISE NOTICE 'Migration 561 completed: origin_actor exposed on VIEW';
END $$;

COMMIT;

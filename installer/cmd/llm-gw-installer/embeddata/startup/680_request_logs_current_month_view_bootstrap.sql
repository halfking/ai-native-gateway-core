-- ===========================================================================
-- File:          sql/migrations/startup/680_request_logs_current_month_view_bootstrap.sql
-- Migration:     680
-- Database:      llm_gateway
-- Purpose:       Bootstrap (re)create the canonical request_logs query view
--                request_logs_with_current_month when it is missing.
--
-- Status:        active
-- Idempotent:    YES (every stage checks pg_views before acting; no-op on a
--                healthy database)
-- Dependencies:  request_logs_hot + request_logs exist (migration 341);
--                request_class/due_at columns (migration 608/610);
--                customer_id column on hot + parent (migrations 575/576).
--
-- Background (2026-09-07 incident):
--   /request-logs list queries failed with
--     ERROR: relation "request_logs_with_current_month" does not exist
--     (SQLSTATE 42P01)
--   The shared local PG still held both wrapper views
--   (…_without_customer_id, …_without_request_class_due_at) but the top
--   canonical view was gone. Migration 610 (2026-08-27) is recorded applied,
--   and no repo migration after 610 touches the view, so the drop happened
--   out-of-band against the shared instance. The plausible mechanism is a
--   non-transactional replay of an old migration whose leading
--   "DROP VIEW IF EXISTS request_logs_with_current_month" autocommitted and
--   whose trailing "CREATE VIEW … SELECT * FROM request_logs_hot UNION ALL
--   SELECT * FROM request_logs" then failed on the hot/parent column-count
--   mismatch (hot carries ~45 extra columns since 603) — DROP applied,
--   CREATE never did, leaving the classic "renamed intermediates present,
--   canonical absent" state. The gateway had no self-heal for this view, so
--   every /api/logs request kept failing until a repair ran.
--
-- Fix shape:
--   Staged rebuild mirroring the 577 + 610 wrapper chain so the view column
--   contract (base 108 + customer_id + request_class + due_at) is restored
--   exactly. The base stage derives its column list as the hot∩parent
--   intersection (minus customer_id/request_class/due_at) so HOT_ONLY hot
--   columns can never break the UNION — unlike the 341-style "SELECT *"
--   replay that caused the incident.
--
-- Safety Check:
--   - Healthy DB: canonical view exists → RETURN before any DDL.
--   - Purely additive; never drops or renames existing relations.
--   - Single transaction.
-- Rollback Script: 680_request_logs_current_month_view_bootstrap.down.sql
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  canonical_exists boolean;
  class_wrapper_exists boolean;
  base_wrapper_exists boolean;
  base_cols text;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views
    WHERE schemaname = 'public'
      AND viewname = 'request_logs_with_current_month'
  ) INTO canonical_exists;
  IF canonical_exists THEN
    RAISE NOTICE '680: request_logs_with_current_month already exists; nothing to do';
    RETURN;
  END IF;

  SELECT EXISTS (
    SELECT 1 FROM pg_views
    WHERE schemaname = 'public'
      AND viewname = 'request_logs_with_current_month_without_request_class_due_at'
  ) INTO class_wrapper_exists;

  SELECT EXISTS (
    SELECT 1 FROM pg_views
    WHERE schemaname = 'public'
      AND viewname = 'request_logs_with_current_month_without_customer_id'
  ) INTO base_wrapper_exists;

  IF NOT base_wrapper_exists THEN
    -- Base 108-column UNION. hot∩parent minus the three later-appended
    -- columns keeps the pre-575 view contract; HOT_ONLY hot columns are
    -- excluded by construction so the UNION cannot drift on column count.
    SELECT string_agg(quote_ident(h.attname), ', ' ORDER BY h.attnum)
      INTO base_cols
      FROM pg_attribute h
      JOIN pg_class hc ON hc.oid = h.attrelid
      JOIN pg_namespace hn ON hn.oid = hc.relnamespace
      JOIN pg_attribute p ON p.attrelid = ('public.request_logs')::regclass
                         AND p.attname = h.attname
      JOIN pg_class pc ON pc.oid = p.attrelid
      JOIN pg_namespace pn ON pn.oid = pc.relnamespace
     WHERE hn.nspname = 'public'
       AND hc.relname = 'request_logs_hot'
       AND pn.nspname = 'public'
       AND pc.relname = 'request_logs'
       AND h.attnum > 0 AND NOT h.attisdropped
       AND p.attnum > 0 AND NOT p.attisdropped
       AND h.attname NOT IN ('customer_id', 'request_class', 'due_at');
    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '680: cannot derive base column list for request_logs view chain';
    END IF;
    EXECUTE format(
      'CREATE VIEW public.request_logs_with_current_month_without_customer_id AS
       SELECT %1$s FROM public.request_logs_hot
       UNION ALL
       SELECT %1$s FROM public.request_logs',
      base_cols
    );
  END IF;

  IF NOT class_wrapper_exists THEN
    -- Migration 577 stage: append customer_id via hot-first lateral lookup.
    CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
    SELECT v.*, m.customer_id
    FROM public.request_logs_with_current_month_without_customer_id v
    LEFT JOIN LATERAL (
        SELECT customer_id FROM public.request_logs_hot
        WHERE request_id = v.request_id AND ts = v.ts
        UNION ALL
        SELECT customer_id FROM public.request_logs
        WHERE request_id = v.request_id AND ts = v.ts
        LIMIT 1
    ) m ON true;
  END IF;

  -- Migration 610 stage: append request_class/due_at (the canonical view).
  CREATE VIEW public.request_logs_with_current_month AS
  SELECT v.*, source.request_class, source.due_at
  FROM public.request_logs_with_current_month_without_request_class_due_at v
  LEFT JOIN LATERAL (
    SELECT h.request_class, h.due_at
    FROM public.request_logs_hot h
    WHERE h.request_id = v.request_id AND h.ts = v.ts
    UNION ALL
    SELECT p.request_class, p.due_at
    FROM public.request_logs p
    WHERE p.request_id = v.request_id AND p.ts = v.ts
    LIMIT 1
  ) source ON true;

  COMMENT ON VIEW public.request_logs_with_current_month IS
    'Hot + monthly partitions UNION with customer_id (577) and request_class/due_at (610) appended. Bootstrap-recreated by 680 when dropped out-of-band; runtime self-heal mirrors db.ensureRequestLogsCurrentMonthView.';
END $$;

COMMIT;

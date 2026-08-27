-- Migration 608: Persist request class (immediate|scheduled) + due_at.
--
-- The current-month view is extended as a wrapper rather than reconstructed
-- from request_logs_hot/request_logs. Earlier migrations may already wrap that
-- view (for example, migration 575 adds customer metadata); preserving the
-- prior definition keeps those columns and semantics intact.

BEGIN;

ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS request_class text NOT NULL DEFAULT 'immediate',
    ADD COLUMN IF NOT EXISTS due_at timestamptz;

ALTER TABLE public.request_logs
    ADD COLUMN IF NOT EXISTS request_class text NOT NULL DEFAULT 'immediate',
    ADD COLUMN IF NOT EXISTS due_at timestamptz;

COMMENT ON COLUMN public.request_logs_hot.request_class IS
  'V6-W1.6 R8 request class: immediate|scheduled (ir.InternalRequest.Class via X-Gw-Due-At)';
COMMENT ON COLUMN public.request_logs_hot.due_at IS
  'V6-W1.6 R8 scheduled execution time; NULL for immediate requests';

-- The invariant is enforced on both write target and parent so direct writes,
-- future partitions, and the view all observe the same domain contract.
DO $$
DECLARE
  tbl text;
  constraint_name text;
BEGIN
  FOR tbl IN SELECT unnest(ARRAY['request_logs_hot', 'request_logs']) LOOP
    constraint_name := tbl || '_request_class_due_at_check';
    IF NOT EXISTS (
      SELECT 1 FROM pg_constraint c
      JOIN pg_class r ON r.oid = c.conrelid
      JOIN pg_namespace n ON n.oid = r.relnamespace
      WHERE n.nspname = 'public' AND r.relname = tbl AND c.conname = constraint_name
    ) THEN
      EXECUTE format(
        'ALTER TABLE public.%I ADD CONSTRAINT %I CHECK ((request_class = ''immediate'' AND due_at IS NULL) OR (request_class = ''scheduled'' AND due_at IS NOT NULL))',
        tbl, constraint_name
      );
    END IF;
  END LOOP;
END $$;

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_request_class
    ON public.request_logs_hot (request_class)
    WHERE request_class = 'scheduled';

-- Preserve every existing view wrapper and append the two new columns. A
-- lateral lookup is necessary because the old view can be a 575-style wrapper
-- rather than a direct hot/parent UNION.
DO $$
DECLARE
  view_exists boolean;
  already_extended boolean;
  saved_view_exists boolean;
  base_cols text;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views
    WHERE schemaname = 'public' AND viewname = 'request_logs_with_current_month'
  ) INTO view_exists;

  IF view_exists THEN
    SELECT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public'
        AND table_name = 'request_logs_with_current_month'
        AND column_name = 'request_class'
    ) INTO already_extended;
    IF already_extended THEN
      RAISE NOTICE '608: current-month view already exposes request_class';
      RETURN;
    END IF;

    SELECT EXISTS (
      SELECT 1 FROM pg_views
      WHERE schemaname = 'public'
        AND viewname = 'request_logs_with_current_month_without_request_class_due_at'
    ) INTO saved_view_exists;
    IF saved_view_exists THEN
      RAISE EXCEPTION '608: saved pre-extension view already exists while canonical view lacks request_class';
    END IF;

    ALTER VIEW public.request_logs_with_current_month
      RENAME TO request_logs_with_current_month_without_request_class_due_at;
  ELSE
    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON c.oid = a.attrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname = 'request_logs_hot'
       AND a.attnum > 0
       AND NOT a.attisdropped
       AND a.attname NOT IN ('request_class', 'due_at');
    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '608: cannot build cold-start base column list';
    END IF;
    EXECUTE format(
      'CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS SELECT %1$s FROM public.request_logs_hot UNION ALL SELECT %1$s FROM public.request_logs',
      base_cols
    );
  END IF;

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
    'Current-month request log view with request_class/due_at appended by migration 608; preserves its pre-608 wrapper as request_logs_with_current_month_without_request_class_due_at.';
END $$;

COMMIT;

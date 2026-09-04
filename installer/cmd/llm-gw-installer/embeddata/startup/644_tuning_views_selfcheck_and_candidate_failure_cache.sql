-- Migration 644: catch-all rollup of stale schema / cache fixes observed
-- in pg log on 2026-09-03 across the local + 252 paths.
--
-- Each section below fixes one (or a closely related family of) ERROR
-- lines from the live pg log. None of these changes require a Go
-- code update; they are additive DDL or one-time data refreshes and
-- are safe to run on every deployment that already has migration 642
-- applied.
--
-- Sections covered:
--   A. Re-populate the four materialized views that
--      `bg/tuning_view_refresher.go` and `apply-routing-mv-fixup.sh`
--      hit with `REFRESH MATERIALIZED VIEW CONCURRENTLY` even though
--      they have never been populated. The first CONCURRENTLY call
--      per view raises SQLSTATE 0A000 "materialized view is not
--      populated"; the error is non-fatal (the worker logs it as
--      WARN) but it has been firing every 5 minutes for months on
--      environments where the views were created without ever being
--      seeded.
--
--   B. Drop and recreate `self_check_runs_selection_strategy_check`
--      to allow the canonical taxonomy values
--      ('featured', 'recent', 'common_7d', 'failed_model',
--      'no_eligible_model') that bg/credential_selfcheck.go started
--      writing in commits after the original baseline schema. The
--      current CHECK only permits ('most_used', 'random', 'fallback_%')
--      so every self-check run after the first cycle now fails with
--      SQLSTATE 23514 "violates check constraint". The replacement
--      CHECK matches the canonical taxonomy in
--      `deploy/sql/migrations/V361__self_check_runs_canonical_taxonomy.sql`
--      so 252 and local stay in sync.
--
--   C. Recreate the candidate_failure_logs_unified view so its
--      catalog cache no longer references a dropped or replaced
--      column on the 2025_12 columnar partition. The view UNION ALLs
--      candidate_failure_logs_hot with candidate_failure_logs (the
--      partitioned parent); the 2025_12 leaf is a columnar table
--      created by Citus. Citus's catalog cache occasionally holds
--      stale attribute references after partition attach/detach,
--      which surfaces as
--      "cache lookup failed for attribute source of relation
--      <oid>" (SQLSTATE XX000). Re-CREATE OR REPLACE the view so
--      its plan cache and pg_class dependencies are rebuilt against
--      the current columnar metadata.
--
--   D. Sanitize malformed JSON before insert into
--      model_integrity_events. Upstream clients occasionally send
--      payloads containing a literal backslash that fails the jsonb
--      typecast. Rather than rejecting the row (which would lose
--      audit signal), a BEFORE INSERT trigger rewrites the
--      payload columns so the literal-backslash case is escaped
--      to a valid JSON string and the row lands. Real corruption is
--      still possible (e.g. unbalanced quotes); the trigger is a
--      last-resort sanitizer, not a substitute for upstream
--      validation.

BEGIN;

-- ── A. Re-populate materialized views ───────────────────────────────────────
-- For every materialized view we care about, if it is not yet
-- populated, refresh it once with the non-CONCURRENTLY variant so
-- subsequent CONCURRENTLY refreshes succeed. The
-- relispopulated check avoids the cost when the view is already
-- populated; the operation is idempotent.
DO $$
DECLARE
    v_view   text;
    v_views  text[] := ARRAY[
        'tuning_signals_5m',
        'tuning_signals_daily',
        'routing_analytics_7d',
        'routing_audit_summary_7d'
    ];
    v_populated boolean;
BEGIN
    FOREACH v_view IN ARRAY v_views LOOP
        SELECT relispopulated INTO v_populated
        FROM pg_class
        WHERE relname = v_view
          AND relkind = 'm';
        IF v_populated IS NOT NULL AND NOT v_populated THEN
            EXECUTE format('REFRESH MATERIALIZED VIEW %I', v_view);
            RAISE NOTICE 'Migration 644: populated materialized view %', v_view;
        ELSE
            RAISE NOTICE 'Migration 644: % already populated, skipping', v_view;
        END IF;
    END LOOP;
END
$$;

-- ── B. self_check_runs_selection_strategy_check ────────────────────────────
-- The current CHECK constraint only accepts ('most_used', 'random',
-- 'fallback_%'), but bg/credential_selfcheck.go now writes a wider
-- taxonomy: ('featured', 'recent', 'common_7d', 'failed_model',
-- 'no_eligible_model'). Recreate the CHECK with the extended list
-- so future self-check runs do not violate the constraint.
--
-- Note: 'fallback_%' is preserved via LIKE; the explicit list and
-- LIKE are joined by OR. NULL is still allowed (legacy rows).
ALTER TABLE public.self_check_runs DROP CONSTRAINT IF EXISTS self_check_runs_selection_strategy_check;

ALTER TABLE public.self_check_runs
    ADD CONSTRAINT self_check_runs_selection_strategy_check CHECK (
        selection_strategy IS NULL
        OR selection_strategy LIKE 'fallback_%'
        OR selection_strategy = ANY (ARRAY[
            'most_used',
            'random',
            'featured',
            'recent',
            'common_7d',
            'failed_model',
            'no_eligible_model'
        ])
    );

COMMENT ON CONSTRAINT self_check_runs_selection_strategy_check ON public.self_check_runs IS
'Canonical taxonomy per deploy/sql/migrations/V361__self_check_runs_canonical_taxonomy.sql. NULL is reserved for legacy rows. Migration 644 aligns the runtime CHECK with the canonical list.';

-- ── C. Recreate candidate_failure_logs_unified ─────────────────────────────
-- The view UNION ALLs the hot window with the partitioned parent.
-- Citus columnar partition metadata can drift from the planner's
-- cache after attach/detach cycles; re-creating the view rebuilds
-- the plan cache and pg_depend entries so the
-- "cache lookup failed for attribute source of relation <oid>"
-- error stops recurring. Source of truth for the definition lives
-- in sql/migrations/startup/627_candidate_failure_logs_aggregation_id_unified.sql;
-- this block preserves the canonical definition by re-running the
-- CREATE OR REPLACE VIEW from that file's intent.
--
-- We deliberately do NOT recreate the historical partition's
-- columnar metadata; the intent is to force the view's plan cache
-- to drop, not to manipulate the partition.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class
        WHERE relname = 'candidate_failure_logs_unified'
          AND relkind = 'v'
    ) THEN
        EXECUTE $SQL$
            CREATE OR REPLACE VIEW public.candidate_failure_logs_unified AS
            SELECT 'hot'::text AS source,
                candidate_failure_logs_hot.id,
                candidate_failure_logs_hot.request_id,
                candidate_failure_logs_hot.ts,
                candidate_failure_logs_hot.tenant_id,
                candidate_failure_logs_hot.credential_id,
                candidate_failure_logs_hot.provider_id,
                candidate_failure_logs_hot.raw_model_name,
                candidate_failure_logs_hot.attempt_index,
                candidate_failure_logs_hot.error_kind,
                candidate_failure_logs_hot.error_message,
                candidate_failure_logs_hot.upstream_status_code,
                candidate_failure_logs_hot.upstream_response_body,
                candidate_failure_logs_hot.upstream_response_preview,
                candidate_failure_logs_hot.latency_ms,
                candidate_failure_logs_hot.retryable,
                candidate_failure_logs_hot.context,
                candidate_failure_logs_hot.per_attempt_latency_ms,
                candidate_failure_logs_hot.extracted_upstream_status_code,
                candidate_failure_logs_hot.diagnosed_error_kind,
                candidate_failure_logs_hot.session_id,
                candidate_failure_logs_hot.aggregation_id
            FROM public.candidate_failure_logs_hot
            UNION ALL
            SELECT 'historical'::text AS source,
                candidate_failure_logs.id,
                candidate_failure_logs.request_id,
                candidate_failure_logs.ts,
                candidate_failure_logs.tenant_id,
                candidate_failure_logs.credential_id,
                candidate_failure_logs.provider_id,
                candidate_failure_logs.raw_model_name,
                candidate_failure_logs.attempt_index,
                candidate_failure_logs.error_kind,
                candidate_failure_logs.error_message,
                candidate_failure_logs.upstream_status_code,
                candidate_failure_logs.upstream_response_body,
                candidate_failure_logs.upstream_response_preview,
                candidate_failure_logs.latency_ms,
                candidate_failure_logs.retryable,
                candidate_failure_logs.context,
                candidate_failure_logs.per_attempt_latency_ms,
                candidate_failure_logs.extracted_upstream_status_code,
                candidate_failure_logs.diagnosed_error_kind,
                candidate_failure_logs.session_id,
                COALESCE(candidate_failure_logs.aggregation_id,
                         - candidate_failure_logs.id) AS aggregation_id
            FROM public.candidate_failure_logs
        $SQL$;
        ALTER VIEW public.candidate_failure_logs_unified
            SET (security_invoker = true);
        RAISE NOTICE 'Migration 644: rebuilt candidate_failure_logs_unified view';
    ELSE
        RAISE NOTICE 'Migration 644: candidate_failure_logs_unified view not present, skipping';
    END IF;
END
$$;

-- ── E. Invalidate Citus catalog cache ────────────────────────────────────────
-- The "cache lookup failed for attribute source of relation
-- <oid>" errors that bg/provider_error_aggregator.go's
-- provider_error_aggregator_worker hits on the columnar partition
-- candidate_failure_logs_2025_12 are caused by Citus's internal
-- columnar catalog holding a stale attribute reference. The
-- candidate_failure_logs_unified view rebuild in section C fixes the
-- view's plan cache, but the worker process's existing session also
-- keeps a cached pg_attribute entry for the columnar relation until
-- it reconnects. Forcing pg_reload_conf sends a SIGHUP-equivalent
-- signal to all backend processes; combined with ANALYZE on the
-- candidate_failure_logs columnar partitions, the next refresh
-- re-loads attribute metadata from the catalog instead of the
-- session-local cache.
--
-- We only run pg_reload_conf if the citus extension is present
-- (the catalog functions live in the citus namespace). On a vanilla
-- PG the call still succeeds as a no-op for backend config reload.
DO $$
BEGIN
    -- Refresh columnar partition stats so Citus picks up the latest
    -- attribute metadata on the next query plan.
    IF EXISTS (
        SELECT 1
        FROM pg_class
        WHERE relname = 'candidate_failure_logs'
          AND relkind = 'p'
    ) THEN
        EXECUTE 'ANALYZE public.candidate_failure_logs';
        RAISE NOTICE 'Migration 644: ANALYZE candidate_failure_logs done';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_class
        WHERE relname = 'candidate_failure_logs_2025_12'
          AND relkind = 'r'
    ) THEN
        EXECUTE 'ANALYZE public.candidate_failure_logs_2025_12';
        RAISE NOTICE 'Migration 644: ANALYZE 2025_12 columnar partition done';
    END IF;
    -- pg_reload_conf asks every backend to re-read postgresql.conf.
    -- On Citus this also drops internal catalog caches that hold
    -- stale attribute references on long-lived connections, which is
    -- exactly the "cache lookup failed for attribute source of
    -- relation <oid>" failure mode.
    PERFORM pg_reload_conf();
    RAISE NOTICE 'Migration 644: pg_reload_conf sent';
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'Migration 644: pg_reload_conf failed (%): %', SQLSTATE, SQLERRM;
END
$$;

-- ── D. model_integrity_events JSON sanitizer trigger ─────────────────────
-- The table stores one JSON-bearing column, context. Keep the sanitizer
-- aligned with the canonical schema; request_payload/response_payload belong
-- to other tables and must not be dereferenced from this trigger.
CREATE OR REPLACE FUNCTION public.sanitize_model_integrity_jsonb()
RETURNS TRIGGER AS $$
DECLARE
    v_text text;
BEGIN
    IF NEW.context IS NOT NULL THEN
        v_text := NEW.context::text;
        IF v_text ~ '\\(?![bfnrtu"/\\])' THEN
            v_text := regexp_replace(v_text, '\\(?![bfnrtu"/\\])', '\\u005c', 'g');
            BEGIN
                NEW.context := v_text::jsonb;
            EXCEPTION WHEN others THEN
                RAISE NOTICE 'Migration 644: invalid context JSON left unchanged';
            END;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;


DROP TRIGGER IF EXISTS trg_model_integrity_events_sanitize_jsonb
    ON public.model_integrity_events;

-- Only attach the trigger if the table actually has jsonb columns.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'model_integrity_events'
          AND data_type = 'jsonb'
    ) THEN
        EXECUTE $SQL$
            CREATE TRIGGER trg_model_integrity_events_sanitize_jsonb
                BEFORE INSERT ON public.model_integrity_events
                FOR EACH ROW
                EXECUTE FUNCTION public.sanitize_model_integrity_jsonb()
        $SQL$;
        RAISE NOTICE 'Migration 644: attached sanitize trigger to model_integrity_events';
    ELSE
        RAISE NOTICE 'Migration 644: model_integrity_events has no jsonb columns, skipping trigger';
    END IF;
END
$$;

-- ── F. Schema migration record ───────────────────────────────────────────────
INSERT INTO public.schema_migrations (version, description)
VALUES ('644',
        'rollup: populate unfilled tuning_signals_* and routing_*_7d materialized views; expand self_check_runs_selection_strategy_check to the canonical taxonomy; rebuild candidate_failure_logs_unified to drop its stale Citus columnar cache; attach a JSON sanitizer trigger on model_integrity_events')
ON CONFLICT (version) DO NOTHING;

COMMIT;

-- Migration 644 rollback: revert each section's changes in reverse.
--
-- A. Leave materialized views alone (REFRESH MATERIALIZED VIEW has no
--    destructive side effect to undo).
-- B. Restore the original narrow CHECK constraint.
-- C. The CREATE OR REPLACE VIEW is forward-only; recreating from
--    history would require importing the original 627 view
--    definition. Best-effort: drop the view if it still has the
--    644-era signature (post-migration signature has COALESCE with
--    negation). Operators running this down should reapply 627 if
--    they need to roll back.
-- D. Drop the JSON sanitizer trigger and helper function.
BEGIN;

-- ── D. JSON sanitizer trigger ─────────────────────────────────────────────
DROP TRIGGER IF EXISTS trg_model_integrity_events_sanitize_jsonb
    ON public.model_integrity_events;
DROP FUNCTION IF EXISTS public.sanitize_model_integrity_jsonb();

-- ── C. candidate_failure_logs_unified ──────────────────────────────────────
-- Forward-only operation; the migration replaces the view body. A
-- proper rollback requires re-applying 627_candidate_failure_logs_aggregation_id_unified.sql.
-- We do not silently drop the view because that breaks every reader;
-- leaving the 644-era body is the safer default.

-- ── B. self_check_runs constraint ───────────────────────────────────────────
ALTER TABLE public.self_check_runs DROP CONSTRAINT IF EXISTS self_check_runs_selection_strategy_check;

ALTER TABLE public.self_check_runs
    ADD CONSTRAINT self_check_runs_selection_strategy_check CHECK (
        selection_strategy IS NULL
        OR selection_strategy = 'most_used'
        OR selection_strategy LIKE 'fallback_%'
        OR selection_strategy = 'random'
    );

-- ── E. Schema migration record removal ─────────────────────────────────────
DELETE FROM public.schema_migrations WHERE version = '644';

COMMIT;

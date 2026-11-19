-- Down migration for 980_pi_analysis_stats_view_alignment
-- ★ 只回退视图形状到 977 口径（去掉三个新列）；977 建的视图本身保留。
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

DO $$
BEGIN
    IF to_regclass('public.v_error_analysis_stats') IS NULL THEN
        RAISE NOTICE '980 down: public.v_error_analysis_stats 不在场 ⇒ 无需回退';
        RETURN;
    END IF;

    DROP VIEW IF EXISTS public.v_error_analysis_stats;

    CREATE VIEW public.v_error_analysis_stats AS
    WITH per_queue AS (
        SELECT
            q.id                     AS queue_id,
            q.error_type             AS error_type,
            q.status                 AS status,
            q.created_at             AS created_at,
            r.is_false_positive      AS is_false_positive,
            r.confidence_score       AS confidence_score,
            r.analysis_duration_ms   AS analysis_duration_ms,
            r.llm_tokens_used        AS llm_tokens_used
        FROM public.error_analysis_queue q
        LEFT JOIN LATERAL (
            SELECT
                rr.is_false_positive,
                rr.confidence_score,
                rr.analysis_duration_ms,
                rr.llm_tokens_used
            FROM public.error_analysis_results rr
            WHERE rr.queue_id = q.id
            ORDER BY rr.id DESC
            LIMIT 1
        ) r ON TRUE
    )
    SELECT
        error_type,
        COUNT(DISTINCT queue_id) AS total_errors,
        COUNT(DISTINCT queue_id) FILTER (WHERE status = 'analyzed') AS analyzed_count,
        COUNT(DISTINCT queue_id) FILTER (WHERE status = 'pending') AS pending_count,
        COUNT(DISTINCT queue_id) FILTER (WHERE is_false_positive = TRUE) AS false_positive_count,
        AVG(confidence_score) AS avg_confidence,
        AVG(analysis_duration_ms) AS avg_analysis_time_ms,
        SUM(llm_tokens_used) AS total_tokens_used,
        MAX(created_at) AS last_error_at
    FROM per_queue
    GROUP BY error_type;
END;
$$;

DELETE FROM public.schema_migrations WHERE version = '980';

COMMIT;

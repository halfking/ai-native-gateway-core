-- Migration 628: propagate aggregation_id across promote.
-- audit-data-closure-A (2026-08-31): migration 624's promote did NOT carry
-- aggregation_id into the destination insert. Migration 627 added the column
-- on the destination and backfilled historical rows. This migration rewrites
-- the promote function to carry the new column going forward so newly
-- promoted hot rows are still visible to the aggregator watermark path
-- (which now reads candidate_failure_logs_unified).
--
-- The function is intentionally unchanged in semantics (atomic, FOR UPDATE
-- SKIP LOCKED, same retention/batch defaults); only the column list is
-- extended.

BEGIN;

CREATE OR REPLACE FUNCTION public.promote_candidate_failure_logs_hot_to_partition(
    p_retention interval DEFAULT '24 hours',
    p_batch_size integer DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    moved bigint := 0;
    month_rec record;
BEGIN
    IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be positive';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 50000';
    END IF;

    FOR month_rec IN
        SELECT DISTINCT date_trunc('month', ts) AS month_start
        FROM public.candidate_failure_logs_hot
        WHERE ts < statement_timestamp() - p_retention
        ORDER BY 1
        LIMIT 12
    LOOP
        PERFORM public.ensure_candidate_failure_logs_partition(month_rec.month_start);
    END LOOP;

    CREATE TEMP TABLE _candidate_failure_logs_promotion_batch ON COMMIT DROP AS
    SELECT ctid AS source_ctid, id, request_id, ts, tenant_id, credential_id,
           provider_id, raw_model_name, attempt_index, error_kind, error_message,
           upstream_status_code, upstream_response_body, upstream_response_preview,
           latency_ms, retryable, per_attempt_latency_ms,
           extracted_upstream_status_code, diagnosed_error_kind, context, session_id,
           aggregation_id
    FROM public.candidate_failure_logs_hot
    WHERE ts < statement_timestamp() - p_retention
    ORDER BY ts, ctid
    LIMIT p_batch_size
    FOR UPDATE SKIP LOCKED;

    IF NOT EXISTS (SELECT 1 FROM _candidate_failure_logs_promotion_batch) THEN
        RETURN 0;
    END IF;

    WITH moved_rows AS (
        DELETE FROM public.candidate_failure_logs_hot h
        USING _candidate_failure_logs_promotion_batch b
        WHERE h.ctid = b.source_ctid
        RETURNING h.id, h.request_id, h.ts, h.tenant_id, h.credential_id,
                  h.provider_id, h.raw_model_name, h.attempt_index,
                  h.error_kind, h.error_message, h.upstream_status_code,
                  h.upstream_response_body, h.upstream_response_preview,
                  h.latency_ms, h.retryable, h.per_attempt_latency_ms,
                  h.extracted_upstream_status_code, h.diagnosed_error_kind,
                  h.context, h.session_id, h.aggregation_id
    ), inserted_rows AS (
        INSERT INTO public.candidate_failure_logs (
            id, request_id, ts, tenant_id, credential_id, provider_id,
            raw_model_name, attempt_index, error_kind, error_message,
            upstream_status_code, upstream_response_body, upstream_response_preview,
            latency_ms, retryable, per_attempt_latency_ms,
            extracted_upstream_status_code, diagnosed_error_kind, context, session_id,
            aggregation_id
        )
        SELECT id, request_id, ts, tenant_id, credential_id, provider_id,
               raw_model_name, attempt_index, error_kind, error_message,
               upstream_status_code, upstream_response_body, upstream_response_preview,
               latency_ms, retryable, per_attempt_latency_ms,
               extracted_upstream_status_code, diagnosed_error_kind, context, session_id,
               aggregation_id
        FROM moved_rows
        RETURNING 1
    )
    SELECT count(*) INTO moved FROM inserted_rows;

    RETURN moved;
END;
$function$;

DO $do$
BEGIN
    IF to_regprocedure('public.promote_candidate_failure_logs_hot_to_partition(interval,integer)') IS NULL THEN
        RAISE EXCEPTION 'candidate failure promote atomic v3 post-condition failed';
    END IF;
END
$do$;

COMMIT;

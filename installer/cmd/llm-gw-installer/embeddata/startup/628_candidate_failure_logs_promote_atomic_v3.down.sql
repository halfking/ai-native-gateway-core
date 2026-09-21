-- Rollback Migration 628: restore promote-candidate-failure-logs v2 (without
-- aggregation_id propagation).
--
-- audit-data-closure-A hotfix (2026-08-31): migration 628 added aggregation_id
-- to the column list of promote_candidate_failure_logs_hot_to_partition. The
-- destination table already has the column (added in migration 627), so dropping
-- it from the projection here is safe — historical rows backfilled by 627 keep
-- their aggregation_id; future promotes simply stop re-writing it from the
-- source row.
--
-- WARNING (same shape as 627's down): running this down re-introduces the
-- promote-before-aggregation visibility bug. Until this down is reverted by
-- re-running 628, hot rows that get promoted will not carry aggregation_id
-- into the columnar partition and the provider-error aggregator (which reads
-- candidate_failure_logs_unified) will silently skip them. Operators running
-- the down MUST also follow the audit handoff remediation: re-seed the
-- aggregator watermark below the maximum historical aggregation_id BEFORE the
-- next aggregator tick.
--
-- This down is intentionally not a no-op CREATE OR REPLACE — it ships a full
-- v2 function body so operators do not need to keep a copy of the v2 source
-- pinned externally.

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
           extracted_upstream_status_code, diagnosed_error_kind, context, session_id
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
                  h.context, h.session_id
    ), inserted_rows AS (
        INSERT INTO public.candidate_failure_logs (
            id, request_id, ts, tenant_id, credential_id, provider_id,
            raw_model_name, attempt_index, error_kind, error_message,
            upstream_status_code, upstream_response_body, upstream_response_preview,
            latency_ms, retryable, per_attempt_latency_ms,
            extracted_upstream_status_code, diagnosed_error_kind, context, session_id
        )
        SELECT id, request_id, ts, tenant_id, credential_id, provider_id,
               raw_model_name, attempt_index, error_kind, error_message,
               upstream_status_code, upstream_response_body, upstream_response_preview,
               latency_ms, retryable, per_attempt_latency_ms,
               extracted_upstream_status_code, diagnosed_error_kind, context, session_id
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
        RAISE EXCEPTION 'candidate failure promote atomic v2 post-condition failed (628 down)';
    END IF;
END
$do$;

COMMIT;

-- Migration 635 down: recreate session_turns_unified (619 mirror) so a
-- rollback to before 635 brings the view back exactly as 619 left it.
-- Operators who already dropped the view through this 635 migration can
-- re-apply the up direction without rewriting the original 619 SQL.

\set ON_ERROR_STOP on

CREATE OR REPLACE VIEW session_turns_unified
    WITH (security_invoker=true) AS
SELECT
    id, session_id, turn_no, tenant_id, request_id,
    project_id, namespace, parent_request_id, task_type,
    ts, submit_mode, compression_applied, compression_strategy,
    compression_meta, compression_tokens_saved, injection_verdict,
    output_verdict, model, provider, credential_id, prompt_tokens,
    completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
    latency_ms, status_code, success, error_kind, source_kind, quality,
    partition_date, attachment_count, attachment_total_bytes,
    multimodal_types, attempt_no, tools, title, summary,
    aggregate_applied_at, t0_arrived_at, t1_total_enqueued_at,
    t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
    t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at,
    t8_response_start_at, t9_response_end_at
FROM session_turns_hot

UNION ALL

SELECT
    id, session_id, turn_no, tenant_id, request_id,
    project_id, namespace, parent_request_id, task_type,
    ts, submit_mode, compression_applied, compression_strategy,
    compression_meta, compression_tokens_saved, injection_verdict,
    output_verdict, model, provider, credential_id, prompt_tokens,
    completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
    latency_ms, status_code, success, error_kind, source_kind, quality,
    partition_date, attachment_count, attachment_total_bytes,
    multimodal_types, attempt_no, tools, title, summary,
    aggregate_applied_at, t0_arrived_at, t1_total_enqueued_at,
    t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
    t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at,
    t8_response_start_at, t9_response_end_at
FROM session_turns;

COMMENT ON VIEW session_turns_unified IS
    'Recreated by 635 down: UNION ALL mirror of 619 (deprecated, kept only for symmetric rollback).';

DO $$
BEGIN
    RAISE NOTICE 'Migration 635 rolled back: session_turns_unified view recreated';
END $$;
-- Migration 713 down: restore session_turns.cost_usd to numeric(12,6).
-- 注意：回滚会重新引入 E5 舍入漂移；已回填的 8 位小数值将被截断到 6 位。
-- 视图依赖处理同正向：turns 视图原地重建；request_logs 视图由 db.ensure 重建。

DROP VIEW IF EXISTS public.request_logs_with_current_month;
DROP VIEW IF EXISTS public.session_turns_with_current_month;

ALTER TABLE public.session_turns
    ALTER COLUMN cost_usd TYPE numeric(12, 6);

ALTER TABLE public.session_turns_hot
    ALTER COLUMN cost_usd TYPE numeric(12, 6);

-- 重建 session_turns_with_current_month（640 同款体，见正向迁移说明）。
CREATE OR REPLACE VIEW public.session_turns_with_current_month
WITH (security_invoker = true) AS
SELECT
    id, session_id, turn_no, tenant_id, request_id,
    project_id, namespace, parent_request_id, task_type,
    ts, submit_mode, compression_applied, compression_strategy,
    compression_meta, compression_tokens_saved, injection_verdict,
    output_verdict, model, provider, credential_id, prompt_tokens,
    completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
    latency_ms, status_code, success, error_kind, source_kind, quality,
    partition_date, attachment_count, attachment_total_bytes,
    multimodal_types, attempt_no, tools, title, summary, digest,
    aggregate_applied_at, t0_arrived_at, t1_total_enqueued_at,
    t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
    t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at,
    t8_response_start_at, t9_response_end_at,
    client_protocol, upstream_protocol, ir_metadata
FROM public.session_turns_hot hot
WHERE NOT EXISTS (
    SELECT 1
    FROM public.session_turns archived
    WHERE archived.tenant_id = hot.tenant_id
      AND archived.request_id = hot.request_id
)
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
    multimodal_types, attempt_no, tools, title, summary, digest,
    aggregate_applied_at, t0_arrived_at, t1_total_enqueued_at,
    t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
    t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at,
    t8_response_start_at, t9_response_end_at,
    client_protocol, upstream_protocol, ir_metadata
FROM public.session_turns;

DELETE FROM public.schema_migrations WHERE version = '713';

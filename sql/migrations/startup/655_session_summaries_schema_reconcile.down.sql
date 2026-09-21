-- 655_session_summaries_schema_reconcile.down.sql
-- 回滚 655：仅删除本迁移新增的列 / 索引 / 约束，不触碰 memora 原有列。
--
-- 警告：回滚后 trg_update_session_summary / trg_sync_session_task_id 触发器
-- 会重新开始失败（带 gw_session_id 的 request_logs_hot INSERT 整体回滚），
-- 仅在明确知道后果时使用。
-- 额外警告：在 canonical 库（本就拥有完整列的 session_summaries）上执行本
-- down 会删掉 canonical 列本身 —— 655 的 ADD COLUMN IF NOT EXISTS 语义下
-- 无法区分"655 补的列"与"原有列"。canonical 库严禁执行本 down。

DROP INDEX IF EXISTS public.idx_session_summaries_search;
DROP INDEX IF EXISTS public.idx_session_summaries_status_time;
DROP INDEX IF EXISTS public.idx_session_summaries_user_tags;
DROP INDEX IF EXISTS public.idx_session_summaries_task;
DROP INDEX IF EXISTS public.idx_session_summaries_project;

ALTER TABLE public.session_summaries
    DROP CONSTRAINT IF EXISTS session_summaries_session_key_per_tenant;

DROP INDEX IF EXISTS public.session_summaries_session_key_uidx;

ALTER TABLE public.session_summaries
    DROP CONSTRAINT IF EXISTS session_summaries_quality_score_check,
    DROP CONSTRAINT IF EXISTS chk_session_status;

ALTER TABLE public.session_summaries
    DROP COLUMN IF EXISTS search_vector,
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS expert_type,
    DROP COLUMN IF EXISTS agent_type,
    DROP COLUMN IF EXISTS outcome,
    DROP COLUMN IF EXISTS session_status,
    DROP COLUMN IF EXISTS user_tags,
    DROP COLUMN IF EXISTS gw_task_id,
    DROP COLUMN IF EXISTS gw_project_id,
    DROP COLUMN IF EXISTS last_trigger_at,
    DROP COLUMN IF EXISTS last_trigger_reason,
    DROP COLUMN IF EXISTS messages_at_trigger,
    DROP COLUMN IF EXISTS tokens_at_trigger,
    DROP COLUMN IF EXISTS last_health_at,
    DROP COLUMN IF EXISTS range,
    DROP COLUMN IF EXISTS health_grade,
    DROP COLUMN IF EXISTS health_score,
    DROP COLUMN IF EXISTS last_handoff_at,
    DROP COLUMN IF EXISTS handoff_count,
    DROP COLUMN IF EXISTS summary_version,
    DROP COLUMN IF EXISTS last_summarized_at,
    DROP COLUMN IF EXISTS client_models,
    DROP COLUMN IF EXISTS providers,
    DROP COLUMN IF EXISTS work_types,
    DROP COLUMN IF EXISTS toxic_output_detected,
    DROP COLUMN IF EXISTS pii_detected,
    DROP COLUMN IF EXISTS prompt_injection_detected,
    DROP COLUMN IF EXISTS compliance_issues_count,
    DROP COLUMN IF EXISTS compliance_status,
    DROP COLUMN IF EXISTS quality_score,
    DROP COLUMN IF EXISTS user_intent,
    DROP COLUMN IF EXISTS key_topics,
    DROP COLUMN IF EXISTS summary,
    DROP COLUMN IF EXISTS title,
    DROP COLUMN IF EXISTS model_switch_count,
    DROP COLUMN IF EXISTS primary_model,
    DROP COLUMN IF EXISTS models_used,
    DROP COLUMN IF EXISTS max_latency_ms,
    DROP COLUMN IF EXISTS min_latency_ms,
    DROP COLUMN IF EXISTS avg_latency_ms,
    DROP COLUMN IF EXISTS total_tokens,
    DROP COLUMN IF EXISTS total_completion_tokens,
    DROP COLUMN IF EXISTS total_prompt_tokens,
    DROP COLUMN IF EXISTS output_cost_usd,
    DROP COLUMN IF EXISTS input_cost_usd,
    DROP COLUMN IF EXISTS total_cost_usd,
    DROP COLUMN IF EXISTS error_count,
    DROP COLUMN IF EXISTS success_count,
    DROP COLUMN IF EXISTS request_count,
    DROP COLUMN IF EXISTS duration_seconds,
    DROP COLUMN IF EXISTS last_request_at,
    DROP COLUMN IF EXISTS first_request_at,
    DROP COLUMN IF EXISTS session_key;

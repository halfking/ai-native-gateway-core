-- Rollback migration 692: shrink session_summaries.user_intent back to varchar(50).
--
-- WARNING: this rollback will FAIL if any existing row has user_intent longer
-- than 50 chars (which is now the common case after migration 692). To roll
-- back safely, first run:
--   UPDATE public.session_summaries SET user_intent = LEFT(user_intent, 50)
--   WHERE LENGTH(user_intent) > 50;
-- then apply this down migration.

BEGIN;

-- v_session_flow depends on session_summaries.user_intent. Drop and recreate
-- it around the type change so PostgreSQL does not reject the rollback with
-- SQLSTATE 42P16 (cannot alter type of a column used by a view or rule).
DROP VIEW IF EXISTS public.v_session_flow;

ALTER TABLE public.session_summaries
    ALTER COLUMN user_intent TYPE varchar(50);

CREATE VIEW public.v_session_flow AS
SELECT
  s.session_key,
  s.tenant_id,
  s.gw_project_id,
  s.gw_task_id,
  s.title,
  s.summary,
  s.user_intent,
  s.first_request_at,
  s.last_request_at,
  s.duration_seconds,
  s.request_count,
  s.success_count,
  s.error_count,
  s.total_cost_usd,
  s.total_tokens,
  s.total_prompt_tokens,
  s.total_completion_tokens,
  s.user_tags,
  s.session_status,
  s.models_used,
  s.primary_model,
  LAG(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as prev_session_key,
  LAG(s.title) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as prev_session_title,
  LEAD(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as next_session_key,
  LEAD(s.title) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as next_session_title,
  ROW_NUMBER() OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as session_order_in_task,
  LAG(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_project_id ORDER BY s.first_request_at) as prev_session_in_project,
  LEAD(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_project_id ORDER BY s.first_request_at) as next_session_in_project
FROM public.session_summaries s
WHERE s.gw_task_id IS NOT NULL OR s.gw_project_id IS NOT NULL;

COMMENT ON VIEW public.v_session_flow IS '会话流程视图：显示会话在任务/项目中的前后关系';

DELETE FROM public.schema_migrations WHERE version = '692';

COMMIT;

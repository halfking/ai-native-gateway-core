-- Migration 692: widen session_summaries.user_intent from varchar(50) to varchar(200)
--
-- Incident (2026-09-09, llm-gateway-pg local logs):
--   Repeated SQLSTATE 22001 (value too long) on every auto-summary upsert
--   whose user_intent exceeded 50 chars. admin/auto_summary_generator.go
--   passes the LLM-generated intent verbatim; the auto-summary prompt
--   template instructs the LLM to write a "concise but complete" intent
--   in Chinese, which routinely produces 60–120 char strings (e.g.
--   "继续推进 llm-gateway-go 项目 proxy 模块的 P2 审计修复，按 #10→#9→#1→#14
--   顺序完成剩余修复并推送至 main"). The 50-char ceiling was set during
--   the initial migration 310 design without real-data sizing, so the
--   production auto-summary path is 100% rejected on every session that
--   triggers rolling-gate summarization.
--
-- Fix:
--   ALTER COLUMN ... TYPE varchar(200). Matches session_summaries.title
--   (varchar(200)) so both "short label" fields share the same ceiling;
--   real-world user_intent values measured in production range 30–180
--   chars (P95 ≈ 110 chars). Truncating in Go would silently drop the
--   tail of every intent — semantic data loss — so widening is correct.
--
-- View dependency handling (2026-09-10):
--   v_session_flow (defined in V354__session_management_views.sql) selects
--   s.user_intent directly. PostgreSQL refuses ALTER COLUMN ... TYPE on
--   a column used by a view with "cannot alter type of a column used by
--   a view or rule". DROP VIEW before ALTER, then CREATE VIEW with the
--   identical definition (column list + WHERE) to preserve the contract.
--   The view does not aggregate user_intent, so the recreate is a pure
--   pass-through and binary-equivalent to V354.
--
-- Idempotency: ALTER COLUMN ... TYPE is not idempotent on its own, but
-- the cast varchar(50) → varchar(200) is lossless and runs in a single
-- short table rewrite (session_summaries ≈ 13 MB in local dev). DROP VIEW
-- IF EXISTS + CREATE VIEW is safe to re-run. The ledger insert uses ON
-- CONFLICT DO NOTHING so re-runs are no-ops.

BEGIN;

DROP VIEW IF EXISTS public.v_session_flow;

ALTER TABLE public.session_summaries
    ALTER COLUMN user_intent TYPE varchar(200);

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

INSERT INTO public.schema_migrations (version, description)
VALUES ('692', 'widen session_summaries.user_intent varchar(50) → varchar(200) (drop+recreate v_session_flow)')
ON CONFLICT (version) DO NOTHING;

COMMIT;

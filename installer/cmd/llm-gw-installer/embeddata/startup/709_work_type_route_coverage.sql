-- Migration 709: work_type route coverage for the 4 production no-route task
-- classes (2026-09-14 auto-matching audit O1′-c, human-confirmed).
--
-- Background (docs/audit/2026-09-14-auto-matching-list.md §一, three-round
-- final): the V2 decision funnel consumes work_type_model_route + l1_task_type
-- (autoroute/decision_v2.go:203-255). Task classes without an enabled route
-- can only draw candidates from the 48h hot-popularity Top3 pool, and if the
-- pool winner's MatchScore<30 the whole recommendation is replaced by the
-- fallback pool winner (recommend_v2.go:272, composite constant 50) — normal
-- scoring signals (Reliability / price / channel quality) never apply.
--
-- Production (2026-09-14 read-only review) had enabled routes for
-- agent/chat/code/creative/long_context/reasoning/vision only;
-- code_audit / function_call / intent_classification / planning always fell
-- back. function_call has the enabled fn_call work_type_config row already;
-- code_audit / intent_classification / planning lacked config rows entirely.
--
-- What this seeds (idempotent, admin-managed route sets stay untouched):
--   - 3 work_type_config rows for the missing l1_task_type values;
--   - primary/secondary routes for the 4 keys. Primary is deepseek-v4-flash
--     and secondary glm-5.2 / minimax-m2.7 — the measured best picks from the
--     2026-09-14 E2E matrix (§一). claude models are deliberately NOT seeded:
--     item #2 of the same audit keeps them as the manual-explicit tier.
--
-- Compatibility: INSERT ... ON CONFLICT DO NOTHING plus a per-key
-- "no routes yet" guard so operator-edited route sets are never reverted on
-- re-run (491 convention). Down migration removes only the seeded rows.

BEGIN;

INSERT INTO work_type_config (key, label, category, l1_task_type, default_profile, tags, prompt_keywords, sort_order)
VALUES
  ('code_audit',            '代码审计', '研发', 'code_audit',            'smart',       ARRAY['code','audit'],        ARRAY['审计','审查','安全','漏洞'],     25),
  ('intent_classification', '意图分类', '通用', 'intent_classification', 'speed_first', ARRAY['classification','intent'], ARRAY['意图','分类','路由'],     26),
  ('planning',              '任务规划', '研发', 'planning',              'smart',       ARRAY['planning','plan'],     ARRAY['规划','计划','拆解','步骤'],     27)
ON CONFLICT (key) DO NOTHING;

-- function_call: the fn_call config row exists (seed #7) but had no routes.
INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier)
SELECT v.work_type_key, v.canonical_name, v.weight, 0, TRUE, v.tier
FROM (VALUES
  ('fn_call', 'deepseek-v4-flash', 1.00::numeric, 'primary'),
  ('fn_call', 'minimax-m2.7',      0.85::numeric, 'secondary'),
  ('fn_call', 'glm-5.2',           0.80::numeric, 'secondary')
) AS v(work_type_key, canonical_name, weight, tier)
WHERE NOT EXISTS (
  SELECT 1 FROM work_type_model_route r WHERE r.work_type_key = v.work_type_key
)
ON CONFLICT (work_type_key, canonical_name) DO NOTHING;

INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier)
SELECT v.work_type_key, v.canonical_name, v.weight, 0, TRUE, v.tier
FROM (VALUES
  ('code_audit', 'deepseek-v4-flash', 1.00::numeric, 'primary'),
  ('code_audit', 'glm-5.2',           0.80::numeric, 'secondary')
) AS v(work_type_key, canonical_name, weight, tier)
WHERE NOT EXISTS (
  SELECT 1 FROM work_type_model_route r WHERE r.work_type_key = v.work_type_key
)
ON CONFLICT (work_type_key, canonical_name) DO NOTHING;

INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier)
SELECT v.work_type_key, v.canonical_name, v.weight, 0, TRUE, v.tier
FROM (VALUES
  ('intent_classification', 'deepseek-v4-flash', 1.00::numeric, 'primary'),
  ('intent_classification', 'glm-5.2',           0.80::numeric, 'secondary')
) AS v(work_type_key, canonical_name, weight, tier)
WHERE NOT EXISTS (
  SELECT 1 FROM work_type_model_route r WHERE r.work_type_key = v.work_type_key
)
ON CONFLICT (work_type_key, canonical_name) DO NOTHING;

INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier)
SELECT v.work_type_key, v.canonical_name, v.weight, 0, TRUE, v.tier
FROM (VALUES
  ('planning', 'deepseek-v4-flash', 1.00::numeric, 'primary'),
  ('planning', 'glm-5.2',           0.80::numeric, 'secondary')
) AS v(work_type_key, canonical_name, weight, tier)
WHERE NOT EXISTS (
  SELECT 1 FROM work_type_model_route r WHERE r.work_type_key = v.work_type_key
)
ON CONFLICT (work_type_key, canonical_name) DO NOTHING;

-- 双账本自登记(695/701/703/704 定式)。幂等:重跑安全。
INSERT INTO public.schema_migrations (version, description)
VALUES ('709', 'work_type route coverage for code_audit/function_call/intent_classification/planning (auto-matching audit O1''-c)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

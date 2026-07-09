-- Migration 334: align cmb.billing_mode to credential.plan_type for token/code/agent plans
-- Date: 2026-07-10
-- Purpose: 修复 credential (token/code/agent)_plan 与 credential_model_bindings.billing_mode
--          默认值（'per_token'/'free'）的不匹配，让 v_routable_credential_models 的
--          plan_incompatible_* 规则不再误标 is_routable=false。
--
-- Background:
--   v_routable_credential_models (provider/client.go:685) 的 plan compatibility
--   判断逻辑：
--      A) c.plan_type ∈ (token/code/agent)_plan
--         AND cmb.billing_mode ∉ (token/code/agent)_plan  → is_routable=false
--      B) cmb.billing_mode ∈ (token/code/agent)_plan
--         AND c.plan_type ∉ (token/code/agent)_plan    → is_routable=false
--
--   model_offers_legacy.billing_mode 默认 'per_token'，credential_model_bindings 默认继承。
--   早期 on-bind 写入时若 credential.plan_type 设成 token_plan 但 cmb.billing_mode
--   还停留在默认 'per_token'/'free'，就会触发分支 A，被标 un-routable。
--
--   直接后果（已在 252 PG 上确认）：
--     - apiclaude (cred 17 / '130dao', plan_type='token_plan') 的
--       claude-sonnet-5 / claude-fable-5 以及其它 anthropic-* 模型
--       v.is_routable=false 且 unavailable_reason='plan_incompatible_cmb_requires_free'
--     - 用户在 llm.kxpms.cn 请求这两个模型，router 走 loadCandidatesDB，
--       v.is_routable=FALSE 过滤掉 → candidates=[] → no_candidate (503)
--
-- Fix:
--   对 plan_type 属于 (token/code/agent)_plan 的 credentials，把对应 cmb 行的
--   billing_mode 同步到 plan_type。这一修改在业务语义上是合理的：plan type
--   是凭据整体的计费模式，绑定（cmb）上的 billing_mode 若不一致则应以凭据为准。
--
-- 兼容性: 幂等可重复跑（已 WHERE 守卫）。只动 cmb.credential_id
--          对应到 plan_type ∈ (token/code/agent)_plan 的行，且 billing_mode
--          不在同集合的行。其它情形完全不动。

BEGIN;

WITH upd AS (
    UPDATE credential_model_bindings cmb
    SET    billing_mode         = c.plan_type,
           plan_type_origin     = COALESCE(cmb.plan_type_origin, 'sync_with_cred:334'),
           plan_type_updated_at = NOW()
    FROM   credentials c
    WHERE  c.id = cmb.credential_id
      AND  c.plan_type IN ('token_plan','code_plan','agent_plan')
      AND  cmb.billing_mode NOT IN ('token_plan','code_plan','agent_plan')
    RETURNING cmb.id, cmb.credential_id, cmb.billing_mode AS new_billing
) SELECT 'cmb_rows_aligned' AS metric, count(*),
       count(*) FILTER (WHERE new_billing='token_plan')  AS aligned_to_token,
       count(*) FILTER (WHERE new_billing='code_plan')   AS aligned_to_code,
       count(*) FILTER (WHERE new_billing='agent_plan')  AS aligned_to_agent
FROM upd;

-- 触发 credential_model_index_hot 重建
SELECT pg_notify('auto_route_refresh', 'manual:334');

COMMIT;

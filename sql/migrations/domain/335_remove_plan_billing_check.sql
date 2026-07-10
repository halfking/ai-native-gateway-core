-- Migration 335: 移除视图中错误的 plan_type vs billing_mode 校验
-- Date: 2026-07-10
-- Author: System Fix
--
-- Problem:
--   Migration 327/328/332 在 v_routable_credential_models 视图中添加了
--   plan_type vs billing_mode 的强制一致性校验，导致：
--     1. claude-fable-5 等模型被标记为 plan_incompatible（credential.plan_type=token_plan
--        但 cmb.billing_mode=per_token）
--     2. 业务逻辑错误：billing_mode 是我们从供应商**采购**的计费方式，
--        plan_type 是我们向客户**销售**的计费方式，两者本来就应该可以独立！
--
-- Root Cause:
--   Migration 327 试图让 plan_type 成为 SSOT 并派生 billing_mode，这违背了
--   采购-销售分离的商业逻辑。我们可以从供应商那里按量采购（per_token），
--   但以套餐方式（token_plan）卖给客户，这是完全合理的商业模式。
--
-- Fix:
--   重建 v_routable_credential_models 视图，移除所有 plan_type vs billing_mode
--   的校验逻辑。保留其他所有健康检查（provider enabled, credential status,
--   quota_state, availability_state 等）。
--
-- Impact:
--   - claude-fable-5, mimo-v2.5-pro 等模型恢复可路由
--   - 不再限制 billing_mode 和 plan_type 的组合
--   - 保留所有其他路由安全检查

BEGIN;

DROP VIEW IF EXISTS v_routable_credential_models CASCADE;

CREATE OR REPLACE VIEW v_routable_credential_models AS
SELECT
    cmb.id AS binding_id,
    cmb.credential_id,
    cmb.provider_model_id,
    c.tenant_id,
    p.id AS provider_id,
    c.label AS credential_label,
    pm.raw_model_name,
    pm.canonical_id,
    cmb.billing_mode,
    c.plan_type,
    cmb.plan_type_origin,

    -- 核心逻辑: 判断是否可路由（移除了 plan_type vs billing_mode 校验）
    CASE
        -- Provider 级检查
        WHEN NOT p.enabled THEN false
        WHEN COALESCE(p.manual_disabled, false) THEN false

        -- Credential 级检查
        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN false
        WHEN c.lifecycle_status != 'active' THEN false
        WHEN COALESCE(c.manual_disabled, false) THEN false

        -- Availability state 检查
        WHEN c.availability_state = 'cooling' THEN false
        WHEN c.availability_state = 'rate_limited' THEN false
        WHEN c.availability_state = 'auth_failed' THEN false
        WHEN c.availability_state = 'unreachable' THEN false
        WHEN c.availability_state = 'suspended' THEN false

        -- Quota state 检查
        WHEN c.quota_state IN ('permanently_exhausted', 'balance_exhausted') THEN false
        WHEN c.quota_state = 'periodic_exhausted' THEN false
        WHEN c.quota_state = 'exhausted'
             AND (c.quota_recover_at IS NULL OR c.quota_recover_at > NOW()) THEN false

        -- Health 检查（最近1小时探测为 unreachable）
        WHEN c.health_status = 'unreachable'
             AND c.health_checked_at > (NOW() - INTERVAL '1 hour') THEN false

        -- Provider model 检查
        WHEN NOT pm.available THEN false

        -- Binding 级检查
        WHEN cmb.available IS NOT true THEN false
        WHEN cmb.unavailable_reason = 'manual' THEN false

        ELSE true
    END AS is_routable,

    -- 不可路由原因（移除了 plan_incompatible_* 原因）
    CASE
        WHEN NOT p.enabled THEN 'provider_disabled'
        WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'
        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN 'credential_status_' || c.status
        WHEN c.lifecycle_status != 'active' THEN 'lifecycle_' || c.lifecycle_status
        WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'
        WHEN c.availability_state = 'cooling' THEN 'availability_cooling'
        WHEN c.availability_state = 'rate_limited' THEN 'availability_rate_limited'
        WHEN c.availability_state = 'auth_failed' THEN 'availability_auth_failed'
        WHEN c.availability_state = 'unreachable' THEN 'availability_unreachable'
        WHEN c.availability_state = 'suspended' THEN 'availability_suspended'
        WHEN c.quota_state IN ('permanently_exhausted', 'balance_exhausted') THEN 'quota_' || c.quota_state
        WHEN c.quota_state = 'periodic_exhausted' THEN 'quota_periodic_exhausted'
        WHEN c.quota_state = 'exhausted' THEN 'quota_exhausted'
        WHEN c.health_status = 'unreachable'
             AND c.health_checked_at > (NOW() - INTERVAL '1 hour') THEN 'recent_probe_unreachable'
        WHEN NOT pm.available THEN 'model_unavailable'
        WHEN cmb.unavailable_reason = 'manual' THEN 'model_manual_disabled'
        WHEN cmb.available IS NOT true THEN 'binding_unavailable'
        ELSE NULL
    END AS unavailable_reason,

    -- 路由评分（保持不变）
    (
        (cmb.manual_priority * 100)::numeric
        + COALESCE(cmb.success_rate, 0.5) * 50
        - COALESCE(cmb.unit_price_in_per_1m, 0) * 0.001
        - COALESCE(cmb.p95_latency_ms, 1000)::numeric * 0.01
    ) AS routing_score

FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id;

COMMENT ON VIEW v_routable_credential_models IS
  '可路由的凭证-模型绑定视图（billing_mode 和 plan_type 独立，不强制一致）';

GRANT SELECT ON v_routable_credential_models TO PUBLIC;

-- 触发自动路由刷新
SELECT pg_notify('auto_route_refresh', 'manual:335_remove_plan_billing_check');

COMMIT;

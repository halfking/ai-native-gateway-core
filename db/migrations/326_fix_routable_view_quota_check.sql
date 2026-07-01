-- 修复 v_routable_credential_models 视图的 is_routable 计算逻辑
--
-- 问题：
-- 1. 当前视图允许 quota_state = 'periodic_exhausted' 通过
-- 2. 不考虑恢复时间（availability_recover_at, quota_recover_at）
-- 3. 导致 Admin API 和实际路由看到不同的候选列表
--
-- 修复：
-- 1. 严格要求 quota_state = 'ok' 或 NULL
-- 2. 考虑恢复时间：如果已过恢复时间，视为可用
-- 3. 与实际路由逻辑 (provider.Candidate.UnavailableReason) 保持一致

CREATE OR REPLACE VIEW public.v_routable_credential_models AS
SELECT 
    cmb.id AS binding_id,
    cmb.credential_id,
    cmb.provider_model_id,
    c.tenant_id,
    p.id AS provider_id,
    c.label AS credential_label,
    pm.raw_model_name,
    pm.canonical_id,
    
    -- unavailable_reason: 诊断信息，说明为什么不可路由
    CASE
        WHEN NOT p.enabled THEN 'provider_disabled'::text
        WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'::text
        WHEN c.status <> 'active'::text THEN ('credential_status_' || c.status)::text
        WHEN c.lifecycle_status <> 'active'::text THEN ('lifecycle_' || c.lifecycle_status)::text
        WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'::text
        WHEN c.availability_state = 'cooling'::text THEN 'availability_cooling'::text
        WHEN c.availability_state = 'rate_limited'::text THEN 'availability_rate_limited'::text
        WHEN c.availability_state = 'auth_failed'::text THEN 'availability_auth_failed'::text
        WHEN c.availability_state = 'unreachable'::text THEN 'availability_unreachable'::text
        WHEN c.availability_state = 'suspended'::text THEN 'availability_suspended'::text
        WHEN c.quota_state IN ('permanently_exhausted', 'balance_exhausted', 'periodic_exhausted') THEN ('quota_' || c.quota_state)::text
        WHEN c.health_status = 'unreachable'::text AND c.health_checked_at > (now() - interval '1 hour') THEN 'recent_probe_unreachable'::text
        WHEN NOT pm.available THEN 'model_unavailable'::text
        WHEN cmb.unavailable_reason = 'manual'::text THEN 'model_manual_disabled'::text
        WHEN NOT cmb.available THEN 'binding_unavailable'::text
        ELSE NULL::text
    END AS unavailable_reason,
    
    -- is_routable: 严格检查是否真正可路由
    (
        p.enabled 
        AND COALESCE(p.manual_disabled, false) = false
        AND c.status = 'active'::text
        AND c.lifecycle_status = 'active'::text
        AND COALESCE(c.manual_disabled, false) = false
        
        -- 修复1: availability_state 检查（考虑恢复时间）
        AND (
            c.availability_state = 'ready'::text
            OR c.availability_state IS NULL
            OR (
                -- 如果有恢复时间且已过期，视为可用
                c.availability_state IN ('rate_limited', 'cooling', 'unreachable')
                AND c.availability_recover_at IS NOT NULL
                AND c.availability_recover_at <= NOW()
            )
        )
        
        -- 修复2: quota_state 严格检查（考虑恢复时间）
        AND (
            c.quota_state = 'ok'::text
            OR c.quota_state IS NULL
            OR (
                -- 如果有恢复时间且已过期，视为可用
                c.quota_state = 'periodic_exhausted'::text
                AND c.quota_recover_at IS NOT NULL
                AND c.quota_recover_at <= NOW()
            )
        )
        
        AND pm.available = true
        AND cmb.available = true
        AND cmb.unavailable_reason IS DISTINCT FROM 'manual'::text
        AND COALESCE(c.health_status, 'unknown'::text) IN ('healthy', 'unknown')
    ) AS is_routable,
    
    -- routing_score: 用于排序的综合分数
    (
        (cmb.manual_priority * 100)::numeric 
        + COALESCE(cmb.success_rate, 0.5) * 50::numeric
        - COALESCE(cmb.unit_price_in_per_1m, 0::numeric) * 0.001
        - COALESCE(cmb.p95_latency_ms, 1000)::numeric * 0.01
    ) AS routing_score
FROM 
    public.credential_model_bindings cmb
    JOIN public.credentials c ON c.id = cmb.credential_id
    JOIN public.providers p ON p.id = c.provider_id
    JOIN public.provider_models pm ON pm.id = cmb.provider_model_id;

-- 确保视图权限正确
GRANT SELECT ON public.v_routable_credential_models TO PUBLIC;

-- 添加注释
COMMENT ON VIEW public.v_routable_credential_models IS 
'可路由的凭证-模型绑定视图。
is_routable 字段严格检查所有可用性条件，包括：
- quota_state 必须为 ok/NULL 或已过恢复时间
- availability_state 必须为 ready/NULL 或已过恢复时间
- 其他标准检查（provider enabled, credential active等）

修订历史：
- 2026-07-01: 修复 periodic_exhausted 过滤逻辑，添加恢复时间检查';

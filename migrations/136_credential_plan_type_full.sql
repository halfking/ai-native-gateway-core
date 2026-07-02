-- Migration 136: Credential Plan Type Full Standardization
-- Date: 2026-07-03
-- Purpose: Eliminate billing_mode drift across credentials/cmb/model_offers.
--          Make plan_type the SSOT at credential level, derive cmb.billing_mode.
--
-- Supersedes part of migrations/131_credential_plan_type.sql — the view
-- definition is rewritten to drop LEFT JOIN model_offers and read cmb only.
--
-- Author: llm-gateway-go 凭据+路由团队
-- Bug fix: cred 6 / minimax-prod-1 had plan_type='token_plan' but cmb.billing_mode='per_token'
--          → v_routable_credential_models flagged all 11 models as plan_incompatible.

-- ============================================================================
-- 1. credentials.plan_type CHECK 约束收紧 + 扩展枚举
-- ============================================================================

ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_plan_type_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_plan_type_check
  CHECK (plan_type IN ('token','token_plan','code_plan','agent_plan','monthly','free'));

COMMENT ON COLUMN credentials.plan_type IS
  '套餐 SSOT (凭据级): token=按量付费 | token_plan=令牌包 | code_plan=代码包 | agent_plan=Agent包 | monthly=月付 | free=免费池';

-- ============================================================================
-- 2. credential_model_bindings 添加 plan_type_origin 标签
--    用于 backfill 跳过手动覆盖的行
-- ============================================================================

ALTER TABLE credential_model_bindings
  ADD COLUMN IF NOT EXISTS plan_type_origin TEXT DEFAULT 'auto'
  CHECK (plan_type_origin IN ('auto','manual','backfill'));

COMMENT ON COLUMN credential_model_bindings.plan_type_origin IS
  'billing_mode 来源标签: auto=discovery默认填充 | manual=运维手动覆盖 | backfill=一次性迁移脚本修复';

-- ============================================================================
-- 3. Backfill: 把 cmb.billing_mode 与 credentials.plan_type 不一致的脏数据修正
--    仅触动 plan_type_origin='auto' 的行（保护运维手动改过的）
-- ============================================================================

UPDATE credential_model_bindings cmb
SET billing_mode = CASE c.plan_type
        WHEN 'token' THEN 'per_token'
        ELSE c.plan_type
    END,
    plan_type_origin = 'backfill',
    updated_at = NOW()
FROM credentials c
WHERE c.id = cmb.credential_id
  AND cmb.plan_type_origin = 'auto'
  AND cmb.billing_mode IS DISTINCT FROM (
    CASE c.plan_type
      WHEN 'token' THEN 'per_token'
      ELSE c.plan_type
    END
  );

-- 记录被修复的行数到 runtask_errors（用于 audit）
DO $$
DECLARE
    affected INT;
BEGIN
    GET DIAGNOSTICS affected = ROW_COUNT;
    RAISE NOTICE '[mig_136] backfill updated % cmb rows', affected;
    -- 写入审计表（如存在）
    BEGIN
        INSERT INTO runtask_errors (task_name, payload, created_at)
        VALUES ('mig_136_backfill',
                jsonb_build_object('affected_rows', affected, 'ran_at', NOW()),
                NOW());
    EXCEPTION WHEN OTHERS THEN
        NULL;  -- 审计表不存在则跳过
    END;
END $$;

-- ============================================================================
-- 4. v_routable_credential_models 视图重构
--    - drop LEFT JOIN model_offers
--    - plan_type 兼容检查改用 cmb.billing_mode vs credentials.plan_type
--    - 移除 mo.billing_mode 字段（已退役）
-- ============================================================================

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
    -- billing_mode 派生: cmb 单一来源
    cmb.billing_mode,
    c.plan_type,
    cmb.plan_type_origin,

    -- 核心逻辑: 判断是否可路由
    CASE
        -- 基础条件检查
        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN false
        WHEN c.lifecycle_status != 'active' THEN false
        WHEN cmb.available IS NOT true THEN false

        -- quota_state 检查 (排除 periodic_exhausted, 考虑恢复时间)
        WHEN c.quota_state = 'periodic_exhausted' THEN false
        WHEN c.quota_state = 'exhausted'
             AND (c.quota_recover_at IS NULL OR c.quota_recover_at > NOW()) THEN false

        -- availability_state 检查 (考虑恢复时间)
        WHEN c.availability_state = 'unavailable'
             AND (c.availability_recover_at IS NULL OR c.availability_recover_at > NOW()) THEN false

        -- 套餐兼容性: credential.plan_type vs cmb.billing_mode
        WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan')
             AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan') THEN false
        WHEN cmb.billing_mode IN ('token_plan', 'code_plan', 'agent_plan')
             AND c.plan_type NOT IN ('token_plan', 'code_plan', 'agent_plan') THEN false

        ELSE true
    END AS is_routable,

    -- 不可路由原因
    CASE
        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN 'credential_status_' || c.status
        WHEN c.lifecycle_status != 'active' THEN 'lifecycle_' || c.lifecycle_status
        WHEN cmb.available IS NOT true THEN 'binding_unavailable'
        WHEN c.quota_state = 'periodic_exhausted' THEN 'quota_periodic_exhausted'
        WHEN c.quota_state = 'exhausted' THEN 'quota_exhausted'
        WHEN c.availability_state = 'unavailable' THEN 'availability_unavailable'

        -- 套餐不兼容 (cmb 视角)
        WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan')
             AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan')
             THEN 'plan_incompatible_cmb_requires_' || COALESCE(cmb.billing_mode, 'per_token')
        WHEN cmb.billing_mode IN ('token_plan', 'code_plan', 'agent_plan')
             AND c.plan_type NOT IN ('token_plan', 'code_plan', 'agent_plan')
             THEN 'plan_incompatible_credential_not_' || cmb.billing_mode

        ELSE NULL
    END AS unavailable_reason
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id;

COMMENT ON VIEW v_routable_credential_models IS
  '可路由的凭证-模型绑定视图（plan_type SSOT, billing_mode 派生 cmb, 路由即时一致）';

GRANT SELECT ON v_routable_credential_models TO PUBLIC;

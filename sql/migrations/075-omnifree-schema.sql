-- OmniFree Phase 1: 数据模型迁移
-- 创建时间: 2026-08-07
-- 版本: v1.0
-- 用途: 创建免费资源管理所需的 4 张新表 + 扩展 3 张现有表

-- ============================================================================
-- 1. 免费资源目录表
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.free_resource_catalog (
    id BIGSERIAL PRIMARY KEY,
    
    -- 关联信息
    provider_code TEXT NOT NULL,
    model_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    display_name_en TEXT,
    
    -- 免费类型与配额
    free_type TEXT NOT NULL CHECK (free_type IN (
        'recurring-daily', 'recurring-monthly', 'one-time-initial',
        'recurring-credit', 'recurring-uncapped', 'keyless', 'discontinued'
    )),
    monthly_tokens BIGINT DEFAULT 0,
    daily_tokens BIGINT DEFAULT 0,
    credit_tokens BIGINT DEFAULT 0,
    
    -- 共享配额池
    pool_key TEXT,
    
    -- ToS 合规
    tos_verdict TEXT NOT NULL DEFAULT 'unknown' CHECK (tos_verdict IN (
        'ok', 'caution', 'ambiguous', 'avoid', 'unknown'
    )),
    tos_notes TEXT,
    tos_reviewed_at TIMESTAMPTZ,
    tos_reviewed_by TEXT,
    
    -- 限制约束
    constraints_json JSONB DEFAULT '{}'::jsonb,
    
    -- 发现与验证
    discovery_method TEXT DEFAULT 'manual' CHECK (discovery_method IN (
        'manual', 'auto-scan', 'community', 'official-docs'
    )),
    verified_at TIMESTAMPTZ,
    last_probe_status TEXT,
    last_probe_error TEXT,
    
    -- 状态
    enabled BOOLEAN DEFAULT TRUE,
    disabled_at TIMESTAMPTZ,
    disabled_reason TEXT,
    
    -- 审计
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id BIGINT NOT NULL DEFAULT 1,
    
    UNIQUE(provider_code, model_id, tenant_id)
);

-- 索引
CREATE INDEX idx_free_resource_catalog_provider ON free_resource_catalog(provider_code);
CREATE INDEX idx_free_resource_catalog_free_type ON free_resource_catalog(free_type) WHERE enabled = TRUE;
CREATE INDEX idx_free_resource_catalog_tos ON free_resource_catalog(tos_verdict) WHERE enabled = TRUE;
CREATE INDEX idx_free_resource_catalog_pool_key ON free_resource_catalog(pool_key) WHERE pool_key IS NOT NULL;
CREATE INDEX idx_free_resource_catalog_tenant ON free_resource_catalog(tenant_id);

COMMENT ON TABLE free_resource_catalog IS '免费 LLM 资源目录 - 维护全球免费提供商配额与合规元数据';
COMMENT ON COLUMN free_resource_catalog.pool_key IS '跨模型共享配额池标识，用于去重计算总配额';
COMMENT ON COLUMN free_resource_catalog.tos_verdict IS 'ToS 合规判定: ok=明确允许 | caution=灰色地带 | avoid=明确禁止代理';

-- ============================================================================
-- 2. 配额追踪表
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.free_quota_tracker (
    id BIGSERIAL PRIMARY KEY,
    
    -- 关联
    credential_id BIGINT NOT NULL,
    provider_code TEXT NOT NULL,
    model_id TEXT NOT NULL,
    
    -- 窗口类型
    window_type TEXT NOT NULL CHECK (window_type IN (
        'hour-5', 'day-1', 'day-7', 'month-1'
    )),
    window_start TIMESTAMPTZ NOT NULL,
    window_end TIMESTAMPTZ NOT NULL,
    
    -- 计数器
    request_count INT DEFAULT 0,
    token_count BIGINT DEFAULT 0,
    success_count INT DEFAULT 0,
    error_count INT DEFAULT 0,
    
    -- 429 校准
    last_429_at TIMESTAMPTZ,
    last_429_reset_after INT,
    last_429_limit_header TEXT,
    corrected_limit INT,
    
    -- 状态
    is_exhausted BOOLEAN DEFAULT FALSE,
    exhausted_at TIMESTAMPTZ,
    auto_reset_at TIMESTAMPTZ,
    
    -- 审计
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id BIGINT NOT NULL DEFAULT 1,
    
    UNIQUE(credential_id, provider_code, model_id, window_type, window_start, tenant_id)
);

-- 索引
CREATE INDEX idx_free_quota_tracker_credential ON free_quota_tracker(credential_id, window_type);
CREATE INDEX idx_free_quota_tracker_provider_model ON free_quota_tracker(provider_code, model_id);
CREATE INDEX idx_free_quota_tracker_exhausted ON free_quota_tracker(is_exhausted, auto_reset_at) 
    WHERE is_exhausted = TRUE;
CREATE INDEX idx_free_quota_tracker_window ON free_quota_tracker(window_start, window_end);
CREATE INDEX idx_free_quota_tracker_tenant ON free_quota_tracker(tenant_id);
CREATE INDEX idx_free_quota_tracker_cleanup ON free_quota_tracker(auto_reset_at) 
    WHERE auto_reset_at IS NOT NULL AND auto_reset_at < now();

COMMENT ON TABLE free_quota_tracker IS '本地配额追踪 - 因大多数免费提供商无 usage API，需本地计量并从 429 校准';
COMMENT ON COLUMN free_quota_tracker.window_type IS '窗口类型: hour-5=短期burst | day-1=UTC日 | day-7=滚动周 | month-1=UTC月';

-- ============================================================================
-- 3. Auto Combo 模板表
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.auto_combo_templates (
    id BIGSERIAL PRIMARY KEY,
    
    -- 标识
    combo_name TEXT NOT NULL,
    display_name TEXT NOT NULL,
    description TEXT,
    
    -- 路由变体
    variant TEXT NOT NULL CHECK (variant IN (
        'cheap', 'fast', 'smart', 'coding', 'reasoning', 'creative', 'chaos'
    )),
    
    -- 过滤条件
    tier_filter TEXT[] DEFAULT ARRAY['free'],
    free_type_filter TEXT[],
    tos_filter TEXT[] DEFAULT ARRAY['ok', 'caution'],
    
    provider_allowlist TEXT[],
    provider_denylist TEXT[],
    model_pattern TEXT,
    
    -- 评分权重
    scoring_weights_json JSONB DEFAULT '{
        "health_score": 0.3,
        "latency_p95": 0.2,
        "quota_remaining": 0.25,
        "cost": 0.0,
        "task_fit": 0.15,
        "tier_affinity": 0.1
    }'::jsonb,
    
    -- 候选池配置
    max_candidates INT DEFAULT 50,
    exploration_rate FLOAT DEFAULT 0.05,
    
    -- 状态
    enabled BOOLEAN DEFAULT TRUE,
    priority INT DEFAULT 100,
    
    -- 审计
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id BIGINT NOT NULL DEFAULT 1,
    
    UNIQUE(combo_name, tenant_id)
);

-- 索引
CREATE INDEX idx_auto_combo_templates_variant ON auto_combo_templates(variant) WHERE enabled = TRUE;
CREATE INDEX idx_auto_combo_templates_name ON auto_combo_templates(combo_name);
CREATE INDEX idx_auto_combo_templates_tenant ON auto_combo_templates(tenant_id);

COMMENT ON TABLE auto_combo_templates IS '虚拟 auto/* 路由模板 - 零配置免费资源聚合';

-- ============================================================================
-- 4. Keyless 提供商表
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.keyless_providers (
    id BIGSERIAL PRIMARY KEY,
    
    -- 提供商信息
    provider_code TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    
    -- 认证机制
    auth_hint TEXT,
    bootstrap_method TEXT,
    bootstrap_script TEXT,
    
    -- 限制
    rpm_limit INT DEFAULT 20,
    rpd_limit INT DEFAULT 1000,
    concurrent_limit INT DEFAULT 5,
    
    -- 可靠性
    reliability_score FLOAT DEFAULT 1.0,
    last_probe_at TIMESTAMPTZ,
    last_probe_status TEXT,
    consecutive_failures INT DEFAULT 0,
    
    -- 状态
    enabled BOOLEAN DEFAULT TRUE,
    allowlist_in_auto_combo BOOLEAN DEFAULT FALSE,
    notes TEXT,
    
    -- 审计
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id BIGINT NOT NULL DEFAULT 1
);

-- 索引
CREATE INDEX idx_keyless_providers_enabled ON keyless_providers(provider_code) WHERE enabled = TRUE;
CREATE INDEX idx_keyless_providers_auto_combo ON keyless_providers(provider_code) 
    WHERE enabled = TRUE AND allowlist_in_auto_combo = TRUE;
CREATE INDEX idx_keyless_providers_tenant ON keyless_providers(tenant_id);

COMMENT ON TABLE keyless_providers IS '无认证提供商注册表 - 零成本直接调用的免费 LLM';

-- ============================================================================
-- 5. 扩展现有表 (如果列不存在则添加)
-- ============================================================================

-- 扩展 provider_catalog
DO $$ 
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                   WHERE table_name='provider_catalog' AND column_name='has_free_tier') THEN
        ALTER TABLE public.provider_catalog ADD COLUMN has_free_tier BOOLEAN DEFAULT FALSE;
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                   WHERE table_name='provider_catalog' AND column_name='free_tier_notes') THEN
        ALTER TABLE public.provider_catalog ADD COLUMN free_tier_notes TEXT;
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                   WHERE table_name='provider_catalog' AND column_name='official_free_docs_url') THEN
        ALTER TABLE public.provider_catalog ADD COLUMN official_free_docs_url TEXT;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_provider_catalog_free_tier 
    ON provider_catalog(code) WHERE has_free_tier = TRUE;

-- 扩展 credentials (如果表存在)
DO $$ 
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='credentials') THEN
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                       WHERE table_name='credentials' AND column_name='is_free_tier') THEN
            ALTER TABLE public.credentials ADD COLUMN is_free_tier BOOLEAN DEFAULT FALSE;
        END IF;
        
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                       WHERE table_name='credentials' AND column_name='free_quota_window_type') THEN
            ALTER TABLE public.credentials ADD COLUMN free_quota_window_type TEXT;
        END IF;
        
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                       WHERE table_name='credentials' AND column_name='free_quota_limit') THEN
            ALTER TABLE public.credentials ADD COLUMN free_quota_limit INT;
        END IF;
        
        CREATE INDEX IF NOT EXISTS idx_credentials_free_tier 
            ON credentials(provider_code, is_free_tier) 
            WHERE is_free_tier = TRUE AND enabled = TRUE;
    END IF;
END $$;

-- 扩展 model_offers (如果表存在)
DO $$ 
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='model_offers') THEN
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                       WHERE table_name='model_offers' AND column_name='is_free_model') THEN
            ALTER TABLE public.model_offers ADD COLUMN is_free_model BOOLEAN DEFAULT FALSE;
        END IF;
        
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                       WHERE table_name='model_offers' AND column_name='free_resource_id') THEN
            ALTER TABLE public.model_offers ADD COLUMN free_resource_id BIGINT 
                REFERENCES free_resource_catalog(id);
        END IF;
        
        CREATE INDEX IF NOT EXISTS idx_model_offers_free 
            ON model_offers(credential_id, model_id) 
            WHERE is_free_model = TRUE;
    END IF;
END $$;

-- ============================================================================
-- 6. 辅助视图
-- ============================================================================

CREATE OR REPLACE VIEW v_free_resource_summary AS
SELECT
    tenant_id,
    COUNT(*) AS total_resources,
    COUNT(*) FILTER (WHERE enabled = TRUE) AS enabled_resources,
    COUNT(DISTINCT provider_code) AS provider_count,
    SUM(monthly_tokens) FILTER (WHERE free_type IN ('recurring-monthly', 'keyless')) AS total_monthly_tokens,
    SUM(daily_tokens) FILTER (WHERE free_type = 'recurring-daily') AS total_daily_tokens,
    COUNT(*) FILTER (WHERE tos_verdict = 'ok') AS tos_ok_count,
    COUNT(*) FILTER (WHERE tos_verdict = 'avoid') AS tos_avoid_count,
    COUNT(*) FILTER (WHERE last_probe_status = 'ok') AS healthy_count,
    MAX(verified_at) AS last_verification
FROM free_resource_catalog
GROUP BY tenant_id;

COMMENT ON VIEW v_free_resource_summary IS '免费资源租户级汇总 - 仪表盘用';

-- ============================================================================
-- 7. 辅助函数
-- ============================================================================

-- 池去重配额计算
CREATE OR REPLACE FUNCTION fn_compute_deduped_quota(
    p_tenant_id BIGINT DEFAULT 1,
    p_free_types TEXT[] DEFAULT ARRAY['recurring-monthly', 'recurring-daily', 'keyless']
) RETURNS TABLE (
    pool_key TEXT,
    max_monthly_tokens BIGINT,
    max_daily_tokens BIGINT,
    model_count INT
) AS $$
BEGIN
    RETURN QUERY
    SELECT
        COALESCE(frc.pool_key, frc.provider_code || ':' || frc.model_id) AS pool_key,
        MAX(frc.monthly_tokens) AS max_monthly_tokens,
        MAX(frc.daily_tokens) AS max_daily_tokens,
        COUNT(*)::INT AS model_count
    FROM free_resource_catalog frc
    WHERE frc.tenant_id = p_tenant_id
      AND frc.enabled = TRUE
      AND frc.free_type = ANY(p_free_types)
      AND frc.tos_verdict IN ('ok', 'caution')
    GROUP BY COALESCE(frc.pool_key, frc.provider_code || ':' || frc.model_id);
END;
$$ LANGUAGE plpgsql STABLE;

COMMENT ON FUNCTION fn_compute_deduped_quota IS '池去重配额计算 - 避免共享池重复计数';

-- 配额预检
CREATE OR REPLACE FUNCTION fn_quota_preflight_check(
    p_credential_id BIGINT,
    p_provider_code TEXT,
    p_model_id TEXT,
    p_min_remaining_pct FLOAT DEFAULT 0.1
) RETURNS BOOLEAN AS $$
DECLARE
    v_limit INT;
    v_used INT;
    v_remaining_pct FLOAT;
BEGIN
    SELECT 
        COALESCE(corrected_limit, 1000),
        request_count
    INTO v_limit, v_used
    FROM free_quota_tracker
    WHERE credential_id = p_credential_id
      AND provider_code = p_provider_code
      AND model_id = p_model_id
      AND window_type = 'day-1'
      AND window_start >= date_trunc('day', now())
      AND is_exhausted = FALSE;
    
    IF NOT FOUND THEN
        RETURN TRUE;
    END IF;
    
    v_remaining_pct := (v_limit - v_used)::FLOAT / NULLIF(v_limit, 0);
    
    RETURN v_remaining_pct >= p_min_remaining_pct;
END;
$$ LANGUAGE plpgsql STABLE;

COMMENT ON FUNCTION fn_quota_preflight_check IS '配额预检 - 过滤近耗尽凭据';

-- ============================================================================
-- 8. 完成消息
-- ============================================================================

DO $$
BEGIN
    RAISE NOTICE '✅ OmniFree 数据模型迁移完成！';
    RAISE NOTICE '📊 已创建 4 张新表: free_resource_catalog, free_quota_tracker, auto_combo_templates, keyless_providers';
    RAISE NOTICE '🔧 已扩展 3 张现有表: provider_catalog, credentials, model_offers';
    RAISE NOTICE '📈 已创建 1 个视图: v_free_resource_summary';
    RAISE NOTICE '⚙️  已创建 2 个函数: fn_compute_deduped_quota, fn_quota_preflight_check';
    RAISE NOTICE '';
    RAISE NOTICE '下一步: 导入种子数据';
    RAISE NOTICE '  go run cmd/seed-free-resources/main.go --db-url=$DB_URL';
END $$;

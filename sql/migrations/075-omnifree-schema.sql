BEGIN;

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
    tenant_id TEXT NOT NULL DEFAULT 'default',

    UNIQUE(provider_code, model_id, tenant_id)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_provider ON free_resource_catalog(provider_code);
CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_free_type ON free_resource_catalog(free_type) WHERE enabled = TRUE;
CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_tos ON free_resource_catalog(tos_verdict) WHERE enabled = TRUE;
CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_pool_key ON free_resource_catalog(pool_key) WHERE pool_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_tenant ON free_resource_catalog(tenant_id);

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
    tenant_id TEXT NOT NULL DEFAULT 'default',

    UNIQUE(credential_id, provider_code, model_id, window_type, window_start, tenant_id)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_credential ON free_quota_tracker(credential_id, window_type);
CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_provider_model ON free_quota_tracker(provider_code, model_id);
CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_exhausted ON free_quota_tracker(is_exhausted, auto_reset_at)
    WHERE is_exhausted = TRUE;
CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_window ON free_quota_tracker(window_start, window_end);
CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_tenant ON free_quota_tracker(tenant_id);
-- PostgreSQL requires partial-index predicates to be immutable; now() is not.
CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_cleanup ON free_quota_tracker(auto_reset_at);

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
    tenant_id TEXT NOT NULL DEFAULT 'default',

    UNIQUE(combo_name, tenant_id)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_auto_combo_templates_variant ON auto_combo_templates(variant) WHERE enabled = TRUE;
CREATE INDEX IF NOT EXISTS idx_auto_combo_templates_name ON auto_combo_templates(combo_name);
CREATE INDEX IF NOT EXISTS idx_auto_combo_templates_tenant ON auto_combo_templates(tenant_id);

COMMENT ON TABLE auto_combo_templates IS '虚拟 auto/* 路由模板 - 零配置免费资源聚合';

-- ============================================================================
-- 4. Keyless 提供商表
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.keyless_providers (
    id BIGSERIAL PRIMARY KEY,

    -- 提供商信息
    provider_code TEXT NOT NULL,
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
    tenant_id TEXT NOT NULL DEFAULT 'default',
    CONSTRAINT keyless_providers_provider_tenant_key UNIQUE (provider_code, tenant_id)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_keyless_providers_enabled ON keyless_providers(provider_code) WHERE enabled = TRUE;
CREATE INDEX IF NOT EXISTS idx_keyless_providers_auto_combo ON keyless_providers(provider_code)
    WHERE enabled = TRUE AND allowlist_in_auto_combo = TRUE;
CREATE INDEX IF NOT EXISTS idx_keyless_providers_tenant ON keyless_providers(tenant_id);

COMMENT ON TABLE keyless_providers IS '无认证提供商注册表 - 零成本直接调用的免费 LLM';

-- ============================================================================
-- 5. 扩展现有表 (如果列不存在则添加)
-- ============================================================================

-- Reconcile tables created by early Go bootstrap versions. CREATE IF NOT EXISTS
-- does not alter an existing table, so explicitly add the contract columns and
-- retire legacy NOT NULL/check constraints before seed/import runs.
DO $$
DECLARE
    constraint_name text;
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='auto_combo_templates') THEN
        IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='template_key')
           AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='combo_name') THEN
            ALTER TABLE public.auto_combo_templates ADD COLUMN combo_name TEXT;
            UPDATE public.auto_combo_templates SET combo_name = template_key WHERE combo_name IS NULL;
        END IF;
        IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='template_key') THEN
            ALTER TABLE public.auto_combo_templates ALTER COLUMN template_key DROP NOT NULL;
        END IF;
        IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='combo_name') THEN
            ALTER TABLE public.auto_combo_templates ALTER COLUMN combo_name SET NOT NULL;
        END IF;
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='variant') THEN ALTER TABLE public.auto_combo_templates ADD COLUMN variant TEXT NOT NULL DEFAULT 'cheap'; END IF;
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='tier_filter') THEN ALTER TABLE public.auto_combo_templates ADD COLUMN tier_filter TEXT[] DEFAULT ARRAY['free']; END IF;
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='provider_allowlist') THEN ALTER TABLE public.auto_combo_templates ADD COLUMN provider_allowlist TEXT[]; END IF;
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='provider_denylist') THEN ALTER TABLE public.auto_combo_templates ADD COLUMN provider_denylist TEXT[]; END IF;
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='scoring_weights_json') THEN ALTER TABLE public.auto_combo_templates ADD COLUMN scoring_weights_json JSONB DEFAULT '{}'::jsonb; END IF;
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='max_candidates') THEN ALTER TABLE public.auto_combo_templates ADD COLUMN max_candidates INT DEFAULT 50; END IF;
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='exploration_rate') THEN ALTER TABLE public.auto_combo_templates ADD COLUMN exploration_rate FLOAT DEFAULT 0.05; END IF;
        IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='priority') THEN ALTER TABLE public.auto_combo_templates ADD COLUMN priority INT DEFAULT 100; END IF;
        IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='denylist_codes') THEN UPDATE public.auto_combo_templates SET provider_denylist = denylist_codes WHERE provider_denylist IS NULL; END IF;
    END IF;

    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='free_resource_catalog') THEN
        FOR constraint_name IN SELECT conname FROM pg_constraint WHERE conrelid='public.free_resource_catalog'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%tos_verdict%' LOOP
            EXECUTE format('ALTER TABLE public.free_resource_catalog DROP CONSTRAINT %I', constraint_name);
        END LOOP;
        ALTER TABLE public.free_resource_catalog ADD CONSTRAINT free_resource_catalog_tos_verdict_check CHECK (tos_verdict IN ('ok','caution','ambiguous','avoid','unknown'));
    END IF;
END $$;


DO $$
BEGIN
    -- 仅在 provider_catalog 表存在时执行扩展
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='provider_catalog') THEN
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
    ELSE
        RAISE NOTICE 'provider_catalog 表不存在，跳过扩展步骤';
    END IF;
END $$;

-- 索引创建也需要保护
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='provider_catalog') THEN
        CREATE INDEX IF NOT EXISTS idx_provider_catalog_free_tier
            ON provider_catalog(code) WHERE has_free_tier = TRUE;
    END IF;
END $$;

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
            ON credentials(provider_id, is_free_tier)
            WHERE is_free_tier = TRUE
              AND status IN ('active', 'cooling', 'degraded')
              AND lifecycle_status = 'active';
    END IF;
END $$;

-- model_offers is a view in the gateway baseline, not a table. Free-model
-- metadata is kept in free_resource_catalog; do not ALTER the view here.

-- Normalize tenant columns from early development builds and apply the
-- tenant-scoped key used by keyless providers.
DO $$
DECLARE
    table_name text;
    tenant_type text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'free_resource_catalog', 'free_quota_tracker',
        'auto_combo_templates', 'keyless_providers'
    ] LOOP
        SELECT format_type(a.atttypid, a.atttypmod)
          INTO tenant_type
          FROM pg_attribute a
          JOIN pg_class c ON c.oid = a.attrelid
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'public'
           AND c.relname = table_name
           AND a.attname = 'tenant_id'
           AND NOT a.attisdropped;

        IF tenant_type IS NOT NULL AND tenant_type <> 'text' THEN
            EXECUTE format('ALTER TABLE public.%I ALTER COLUMN tenant_id DROP DEFAULT', table_name);
            EXECUTE format('ALTER TABLE public.%I ALTER COLUMN tenant_id TYPE text USING tenant_id::text', table_name);
        END IF;
        EXECUTE format('ALTER TABLE public.%I ALTER COLUMN tenant_id SET DEFAULT ''default''', table_name);
    END LOOP;

    IF EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'public.keyless_providers'::regclass
           AND conname = 'keyless_providers_provider_code_key'
    ) THEN
        ALTER TABLE public.keyless_providers DROP CONSTRAINT keyless_providers_provider_code_key;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'public.keyless_providers'::regclass
           AND conname = 'keyless_providers_provider_tenant_key'
    ) THEN
        ALTER TABLE public.keyless_providers
            ADD CONSTRAINT keyless_providers_provider_tenant_key UNIQUE (provider_code, tenant_id);
    END IF;
END $$;

CREATE OR REPLACE FUNCTION public.omnifree_touch_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

-- 确保 get_current_tenant() 函数存在（用于 RLS 策略）
-- 如果数据库中尚未定义此函数，则创建一个简化版本
DO $outer$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc p
        JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = 'public' AND p.proname = 'get_current_tenant'
    ) THEN
        EXECUTE $func$
            CREATE FUNCTION public.get_current_tenant() RETURNS text
                LANGUAGE sql STABLE
                AS $body$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $body$;
        $func$;
        RAISE NOTICE '已创建 get_current_tenant() 函数';
    ELSE
        RAISE NOTICE 'get_current_tenant() 函数已存在，跳过创建';
    END IF;
END
$outer$;

DO $$
DECLARE
    table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'free_resource_catalog', 'free_quota_tracker',
        'auto_combo_templates', 'keyless_providers'
    ] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS %I ON public.%I', table_name || '_updated_at', table_name);
        EXECUTE format(
            'CREATE TRIGGER %I BEFORE UPDATE ON public.%I FOR EACH ROW EXECUTE FUNCTION public.omnifree_touch_updated_at()',
            table_name || '_updated_at', table_name
        );
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', 'tenant_isolation_' || table_name, table_name);
        EXECUTE format(
            'CREATE POLICY %I ON public.%I USING ((tenant_id)::text = public.get_current_tenant() OR current_setting(''app.current_role'', true) = ''super_admin'' OR current_setting(''app.bypass_rls'', true) = ''true'') WITH CHECK ((tenant_id)::text = public.get_current_tenant() OR current_setting(''app.current_role'', true) = ''super_admin'' OR current_setting(''app.bypass_rls'', true) = ''true'')',
            'tenant_isolation_' || table_name, table_name
        );
    END LOOP;
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
-- Drop legacy overloads before creating the contract signatures.
DROP FUNCTION IF EXISTS public.fn_compute_deduped_quota(TEXT);
DROP FUNCTION IF EXISTS public.fn_compute_deduped_quota(BIGINT, TEXT[]);
DROP FUNCTION IF EXISTS public.fn_quota_preflight_check(BIGINT, TEXT, TEXT, FLOAT);
DROP FUNCTION IF EXISTS public.fn_quota_preflight_check(BIGINT, TEXT, TEXT, INT, FLOAT, TEXT);

CREATE OR REPLACE FUNCTION fn_compute_deduped_quota(
    p_tenant_id TEXT DEFAULT 'default',
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

COMMENT ON FUNCTION public.fn_compute_deduped_quota(TEXT, TEXT[]) IS '池去重配额计算 - 避免共享池重复计数';

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

COMMENT ON FUNCTION public.fn_quota_preflight_check(BIGINT, TEXT, TEXT, FLOAT) IS '配额预检 - 过滤近耗尽凭据';

-- ============================================================================
-- 7.5. round 3 audit M7: 添加 trains_on_prompts 字段 (OmniRoute 对标)
-- ============================================================================
ALTER TABLE free_resource_catalog
    ADD COLUMN IF NOT EXISTS trains_on_prompts BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN free_resource_catalog.trains_on_prompts IS
    'round 3 M7: 是否将用户 prompt 用于模型训练 (OmniRoute freeModelCatalog.trainsOnPrompts 对标字段). TRUE 时可被运维层根据用户偏好自动剔除.';

-- ============================================================================
-- 8. 完成消息
-- ============================================================================

DO $$
BEGIN
    RAISE NOTICE '✅ OmniFree 数据模型迁移完成！';
    RAISE NOTICE '📊 已创建 4 张新表: free_resource_catalog, free_quota_tracker, auto_combo_templates, keyless_providers';
    RAISE NOTICE '🔧 已扩展 provider_catalog 和 credentials；免费模型元数据在 free_resource_catalog';
    RAISE NOTICE '📈 已创建 1 个视图: v_free_resource_summary';
    RAISE NOTICE '⚙️  已创建 2 个函数: fn_compute_deduped_quota, fn_quota_preflight_check';
    RAISE NOTICE '';
    RAISE NOTICE '下一步: 导入种子数据';
    RAISE NOTICE '  go run cmd/seed-free-resources/main.go --db-url=$DB_URL';
END $$;

COMMIT;

-- FreeDiscovery Phase 1: 免费资源自动发现数据模型
-- 创建时间: 2026-09-09
-- 版本: v1.0
-- 用途: 供应商模板 / 发现任务 / 发现结果 三张表 + free_resource_catalog 来源追踪扩展
-- 参考: Orbi pi-providers 模板能力 (templates/pi-providers/*.json) 的多租户化落地

BEGIN;

-- ============================================================================
-- 1. 供应商模板表 (provider_templates)
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.provider_templates (
    id BIGSERIAL PRIMARY KEY,

    -- 提供商标识
    provider_code TEXT NOT NULL,           -- 'groq', 'openrouter', 'google-ai-studio'
    display_name TEXT NOT NULL,
    base_url TEXT NOT NULL,                -- 'https://api.groq.com/openai/v1'
    api_type TEXT NOT NULL DEFAULT 'openai-completions' CHECK (api_type IN (
        'openai-completions', 'google-generative-ai', 'anthropic'
    )),

    -- 认证: env 引用优先 ('$GROQ_API_KEY'), 密文为空表示 keyless / 无认证
    api_key_env TEXT,
    api_key_encrypted BYTEA,

    -- 发现端点
    models_endpoint TEXT NOT NULL DEFAULT '/models',
    quota_endpoint TEXT,

    -- ToS 合规元数据
    tos_url TEXT,
    tos_verdict TEXT NOT NULL DEFAULT 'unknown' CHECK (tos_verdict IN (
        'ok', 'caution', 'ambiguous', 'avoid', 'unknown'
    )),
    tos_notes TEXT,

    -- 状态与审计
    enabled BOOLEAN DEFAULT TRUE,
    created_by TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id TEXT NOT NULL DEFAULT 'default',

    UNIQUE(provider_code, tenant_id),
    CONSTRAINT provider_templates_base_url_format CHECK (base_url LIKE 'http://%' OR base_url LIKE 'https://%')
);

CREATE INDEX IF NOT EXISTS idx_provider_templates_tenant ON provider_templates(tenant_id);
CREATE INDEX IF NOT EXISTS idx_provider_templates_enabled ON provider_templates(provider_code) WHERE enabled = TRUE;

COMMENT ON TABLE provider_templates IS '免费资源供应商模板 - Orbi pi-providers 模板的多租户化';
COMMENT ON COLUMN provider_templates.api_key_env IS '环境变量引用 ($GROQ_API_KEY); 密钥值本身不入库, 优先走 env 注入';

-- ============================================================================
-- 2. 发现任务表 (discovery_tasks)
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.discovery_tasks (
    id BIGSERIAL PRIMARY KEY,

    template_id BIGINT REFERENCES public.provider_templates(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending', 'running', 'success', 'failed'
    )),
    trigger_type TEXT NOT NULL DEFAULT 'manual' CHECK (trigger_type IN (
        'manual', 'scheduled', 'webhook'
    )),
    triggered_by TEXT,

    -- 冗余 provider_code: template 被 DELETE 后 (ON DELETE SET NULL) 仍可追溯
    provider_code TEXT NOT NULL,

    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    error_message TEXT,
    models_found INT DEFAULT 0,
    models_imported INT DEFAULT 0,

    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id TEXT NOT NULL DEFAULT 'default'
);

CREATE INDEX IF NOT EXISTS idx_discovery_tasks_tenant ON discovery_tasks(tenant_id);
CREATE INDEX IF NOT EXISTS idx_discovery_tasks_status ON discovery_tasks(status, created_at);
CREATE INDEX IF NOT EXISTS idx_discovery_tasks_template ON discovery_tasks(template_id);

COMMENT ON TABLE discovery_tasks IS '免费资源发现任务 - 每次扫描上游模型列表产生一条记录';

-- ============================================================================
-- 3. 发现结果表 (discovery_results)
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.discovery_results (
    id BIGSERIAL PRIMARY KEY,

    task_id BIGINT NOT NULL REFERENCES public.discovery_tasks(id) ON DELETE CASCADE,
    provider_code TEXT NOT NULL,
    model_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    context_window INT,
    max_tokens INT,

    -- 推断的免费类型与配额估算
    free_type TEXT CHECK (free_type IN (
        'recurring-daily', 'recurring-monthly', 'one-time-initial',
        'recurring-credit', 'recurring-uncapped', 'keyless', 'discontinued'
    )),
    monthly_tokens BIGINT DEFAULT 0,
    daily_tokens BIGINT DEFAULT 0,
    pool_key TEXT,

    -- ToS 判定快照 (导入时随行写入 free_resource_catalog)
    tos_verdict TEXT NOT NULL DEFAULT 'unknown' CHECK (tos_verdict IN (
        'ok', 'caution', 'ambiguous', 'avoid', 'unknown'
    )),
    tos_notes TEXT,

    -- 导入状态机
    import_status TEXT NOT NULL DEFAULT 'pending' CHECK (import_status IN (
        'pending', 'imported', 'skipped', 'conflict'
    )),
    imported_at TIMESTAMPTZ,

    -- 上游原始响应 (排障 + 审计)
    raw_metadata JSONB DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ DEFAULT now(),
    -- omnifree_touch_updated_at() 触发器要求 updated_at 列 (与三表循环一致)
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id TEXT NOT NULL DEFAULT 'default',

    UNIQUE(task_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_discovery_results_task ON discovery_results(task_id);
CREATE INDEX IF NOT EXISTS idx_discovery_results_import_status ON discovery_results(import_status) WHERE import_status = 'pending';
CREATE INDEX IF NOT EXISTS idx_discovery_results_tenant ON discovery_results(tenant_id);

COMMENT ON TABLE discovery_results IS '免费资源发现结果 - 待人工审查后批量导入 free_resource_catalog';

-- ============================================================================
-- 4. 扩展 free_resource_catalog: 来源追踪
-- ============================================================================

ALTER TABLE public.free_resource_catalog
    ADD COLUMN IF NOT EXISTS source_type TEXT NOT NULL DEFAULT 'manual',
    ADD COLUMN IF NOT EXISTS discovery_task_id BIGINT,
    ADD COLUMN IF NOT EXISTS last_synced_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS upstream_metadata JSONB;

COMMENT ON COLUMN free_resource_catalog.source_type IS '条目来源: manual=手工 | discovered=自动发现导入';
COMMENT ON COLUMN free_resource_catalog.discovery_task_id IS '产生此条目的发现任务 (不设外键, 允许任务被清理后保留条目)';

CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_source ON free_resource_catalog(source_type)
    WHERE source_type <> 'manual';

-- ============================================================================
-- 5. RLS: 复用 075 的 get_current_tenant() 契约
-- ============================================================================

-- get_current_tenant() 可能已被 075 创建; 幂等保护
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
    END IF;

    -- omnifree_touch_updated_at() 同样可能被 075 回滚掉
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc p
        JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = 'public' AND p.proname = 'omnifree_touch_updated_at'
    ) THEN
        EXECUTE $func$
            CREATE FUNCTION public.omnifree_touch_updated_at()
            RETURNS trigger
            LANGUAGE plpgsql
            AS $body$
            BEGIN
                NEW.updated_at = now();
                RETURN NEW;
            END;
            $body$;
        $func$;
    END IF;
END
$outer$;

DO $$
DECLARE
    table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'provider_templates', 'discovery_tasks', 'discovery_results'
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
-- 6. 完成消息
-- ============================================================================

DO $$
BEGIN
    RAISE NOTICE '✅ FreeDiscovery 数据模型迁移完成！';
    RAISE NOTICE '📊 已创建 3 张新表: provider_templates, discovery_tasks, discovery_results';
    RAISE NOTICE '🔧 已扩展 free_resource_catalog (source_type / discovery_task_id / last_synced_at / upstream_metadata)';
    RAISE NOTICE '🔐 已对 3 张新表启用 RLS (tenant_isolation_* 策略, 复用 get_current_tenant() 契约)';
END $$;

COMMIT;

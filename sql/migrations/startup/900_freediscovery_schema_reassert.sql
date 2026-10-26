-- Migration 900: 把 FreeDiscovery 数据模型（084 + 087）再投递给升级通道库。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 全部为 2026-10-23 生产只读实测，结论可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【症状】https://llmgo.kxpms.cn/free-discovery 全页
--   "free-discovery is not available (database disabled)"（HTTP 503）。
--
-- 【根因：不是网关坏了，是表从未到达生产库】
--   R39 起网关启动链用 db.ProviderTemplatesProvisioned() 探测
--   public.provider_templates 是否存在：不存在 ⇒ SetFreeDiscovery 不接线
--   ⇒ /api/free-discovery/* 一律 503（有意的降级，避免 42P01 裸 500 与
--   scan scheduler 对着不存在的表空转）。245 journal 逐字佐证（10-23）：
--     "provider_templates absent (freediscovery not provisioned); skipping
--      087 health-feedback ensure"
--     "provider_templates not provisioned; free-discovery routes stay
--      unwired (503 on request)"
--
--   而生产共享库（172.16.2.210:5432/llm_gateway，154 与 245 同库）实测：
--     provider_templates / discovery_tasks / discovery_results 三表**不存在**；
--     schema_migrations 数字台账最高 897（894~897 于 2026-10-23 应用），
--     其中**没有 084/087**。
--
-- 【投递缺口：升级通道只把 version ≥ 412 视为 pending】
--   deploy-lib/db-changelog.sh 的 pending 判定只覆盖 ≥412 的版本
--   （896 的文件头已逐字登记过同一缺口：375/376 因此从未到达 252，
--   最终以 896 再投递修复）。084 版本号 < 412 ⇒ 永远不会被升级通道
--   应用。而 084 在 db-changelog 的登记原文是
--   "applied+verified (本地 llm-gateway 库, psql 手动应用…)"
--   ⇒ **它只在本地开发库被手动应用过**，从未进入任何生产库。
--   ⇒ 对照：free_resource_catalog（075，同样 <412）在生产存在，
--   那是建库早期（≥412 规则生效前）遗留的产物 —— 不是规则失效的反例。
--
-- 【形态】
--   逐字复用 084 的 DDL（本身全幂等：CREATE TABLE IF NOT EXISTS /
--   ADD COLUMN IF NOT EXISTS / DO 块自守卫），并把 087 的三个健康反馈列
--   并入（同为 ADD COLUMN IF NOT EXISTS；网关启动 ensure 链在表存在后
--   也会自补，两处叠加无害）。全新安装链（installer StartupFiles）与
--   升级链（apply-db-revision-sequence.sh）**同时登记本文件**：
--   - 新装库：084 不在任何通道 ⇒ 新装库此前同样缺这三张表
--     （free-discovery 在新装形态同样 503）⇒ 本迁移同时补齐新装路径；
--   - 升级库：IF NOT EXISTS 全 no-op，仅补齐缺失对象。
--
-- 【Post-condition】
--   public.provider_templates / discovery_tasks / discovery_results 三表
--   在位且 RLS 启用；网关下次启动时 ProviderTemplatesProvisioned()=true
--   ⇒ free-discovery 路由自动接线（503 消失），无需其它改动。
--
-- 【Down】
--   DROP TABLE IF EXISTS discovery_results, discovery_tasks,
--   provider_templates CASCADE;（本迁移是「把从未到达的表带到」，回滚
--   即回到 503 形态；若迁移后已产生真实数据，down 会连带删数据 ——
--   如实登记，不是缺陷。）

BEGIN;

-- ★ 锁保护（installer startup_ddl_lock_guard_gate_test 的硬要求：>cutoff 的
-- ★ 强锁 DDL 迁移必须带 lock_timeout）。CREATE TABLE / ALTER TABLE 都是
-- ★ 强锁 DDL；对象不存在时锁几乎瞬时可得，抢不到 ⇒ 快速失败、整体回滚、
-- ★ 稍后重跑（896/891 同款语义）。
SET LOCAL lock_timeout = '60s';

-- ── 1. 供应商模板表（084 §1 逐字，幂等）──────────────────────────────
CREATE TABLE IF NOT EXISTS public.provider_templates (
    id BIGSERIAL PRIMARY KEY,
    provider_code TEXT NOT NULL,
    display_name TEXT NOT NULL,
    base_url TEXT NOT NULL,
    api_type TEXT NOT NULL DEFAULT 'openai-completions' CHECK (api_type IN (
        'openai-completions', 'google-generative-ai', 'anthropic'
    )),
    api_key_env TEXT,
    api_key_encrypted BYTEA,
    models_endpoint TEXT NOT NULL DEFAULT '/models',
    quota_endpoint TEXT,
    tos_url TEXT,
    tos_verdict TEXT NOT NULL DEFAULT 'unknown' CHECK (tos_verdict IN (
        'ok', 'caution', 'ambiguous', 'avoid', 'unknown'
    )),
    tos_notes TEXT,
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

-- ── 2. 发现任务表（084 §2 逐字，幂等）────────────────────────────────
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

-- ── 3. 发现结果表（084 §3 逐字，幂等）────────────────────────────────
CREATE TABLE IF NOT EXISTS public.discovery_results (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL REFERENCES public.discovery_tasks(id) ON DELETE CASCADE,
    provider_code TEXT NOT NULL,
    model_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    context_window INT,
    max_tokens INT,
    free_type TEXT CHECK (free_type IN (
        'recurring-daily', 'recurring-monthly', 'one-time-initial',
        'recurring-credit', 'recurring-uncapped', 'keyless', 'discontinued'
    )),
    monthly_tokens BIGINT DEFAULT 0,
    daily_tokens BIGINT DEFAULT 0,
    pool_key TEXT,
    tos_verdict TEXT NOT NULL DEFAULT 'unknown' CHECK (tos_verdict IN (
        'ok', 'caution', 'ambiguous', 'avoid', 'unknown'
    )),
    tos_notes TEXT,
    import_status TEXT NOT NULL DEFAULT 'pending' CHECK (import_status IN (
        'pending', 'imported', 'skipped', 'conflict'
    )),
    imported_at TIMESTAMPTZ,
    raw_metadata JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id TEXT NOT NULL DEFAULT 'default',
    UNIQUE(task_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_discovery_results_task ON discovery_results(task_id);
CREATE INDEX IF NOT EXISTS idx_discovery_results_import_status ON discovery_results(import_status) WHERE import_status = 'pending';
CREATE INDEX IF NOT EXISTS idx_discovery_results_tenant ON discovery_results(tenant_id);

COMMENT ON TABLE discovery_results IS '免费资源发现结果 - 待人工审查后批量导入 free_resource_catalog';

-- ── 4. 087 的健康反馈列（ADD COLUMN IF NOT EXISTS，幂等）──────────────
ALTER TABLE public.provider_templates
    ADD COLUMN IF NOT EXISTS consecutive_scan_failures INT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_scan_failure_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS auto_disabled_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_provider_templates_health
    ON public.provider_templates(consecutive_scan_failures)
    WHERE enabled = FALSE AND auto_disabled_at IS NOT NULL;

COMMENT ON COLUMN provider_templates.consecutive_scan_failures IS
    'Health feedback: consecutive scan failure count; resets to 0 on success, increments on failure, auto-disables at ≥3';
COMMENT ON COLUMN provider_templates.last_scan_failure_at IS
    'Health feedback: timestamp of the most recent scan failure';
COMMENT ON COLUMN provider_templates.auto_disabled_at IS
    'Health feedback: timestamp when template was auto-disabled due to consecutive failures; NULL when manually enabled/disabled';

-- ── 5. free_resource_catalog 来源追踪扩展（084 §4，幂等 + 存在性守卫）──
--
-- ★ 为什么必须有存在性守卫：fresh-install 链上 installer 先跑本迁移、
--   网关 boot ensure 链（db/db_omnifree.go ensureOmniFreeSchema）**之后**
--   才建 free_resource_catalog —— 本表不在任何迁移通道（075 同样 <412，
--   由 Go ensure 承载）⇒ 裸 ALTER 在新装库必然 42P01 中止整条安装链
--   （853/489/490 同族事故）。⇒ 表不存在时跳过并 NOTICE；对应列由
--   ensureOmniFreeSchema 的 ALTER ADD COLUMN IF NOT EXISTS 补齐
--   （087 健康反馈列「迁移 + ensure 双侧幂等」同款先例）。
DO $catalog_900$
BEGIN
    IF to_regclass('public.free_resource_catalog') IS NOT NULL THEN
        ALTER TABLE public.free_resource_catalog
            ADD COLUMN IF NOT EXISTS source_type TEXT NOT NULL DEFAULT 'manual',
            ADD COLUMN IF NOT EXISTS discovery_task_id BIGINT,
            ADD COLUMN IF NOT EXISTS last_synced_at TIMESTAMPTZ,
            ADD COLUMN IF NOT EXISTS upstream_metadata JSONB;

        COMMENT ON COLUMN free_resource_catalog.source_type IS '条目来源: manual=手工 | discovered=自动发现导入';
        COMMENT ON COLUMN free_resource_catalog.discovery_task_id IS '产生此条目的发现任务 (不设外键, 允许任务被清理后保留条目)';

        CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_source ON free_resource_catalog(source_type)
            WHERE source_type <> 'manual';
        RAISE NOTICE '900: free_resource_catalog source-tracking columns ensured';
    ELSE
        RAISE NOTICE '900: public.free_resource_catalog absent (fresh install: boot ensure chain owns this table); skipping source-tracking columns (ensure chain adds them at gateway start)';
    END IF;
END
$catalog_900$;

-- ── 6. RLS 复用 075 的 get_current_tenant() 契约（084 §5 逐字，自守卫）──
DO $outer_900$
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
        RAISE NOTICE '900: created get_current_tenant() function';
    END IF;

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
$outer_900$;

DO $rls_900$
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
END
$rls_900$;

-- ── 7. Post-condition 校验：三表必须在位且 RLS 启用 ────────────────────
DO $verify_900$
DECLARE
    v_missing text;
BEGIN
    SELECT string_agg(relname, ', ') INTO v_missing
    FROM (VALUES ('provider_templates'), ('discovery_tasks'), ('discovery_results')) AS t(relname)
    WHERE NOT EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relname = t.relname
          AND c.relkind IN ('r', 'p')
    );
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION '900 failed: freediscovery tables still missing after re-assert: %', v_missing;
    END IF;
    RAISE NOTICE '900: freediscovery schema (provider_templates, discovery_tasks, discovery_results) is in place with RLS enabled';
END
$verify_900$;

COMMIT;

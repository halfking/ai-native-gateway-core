package db

import (
	"context"
	"fmt"
	"log/slog"
)

// ensureOmniFreeSchema creates the OmniFree data model (4 tables + extensions + RLS + triggers).
//
// round 4 审计补充修复 (2026-08-09): 这个函数曾经维护一套与
// sql/migrations/075-omnifree-schema.sql 完全不同的表结构 (不同列名,
// 不同约束, 不同函数签名) —— 例如旧版本用 template_key/denylist_codes,
// 而迁移文件与 cmd/seed-free-resources/main.go、
// domains/autocombo/resolver.go、domains/autocombo/virtual_factory.go
// 实际读写的是 combo_name/provider_denylist/tier_filter 等字段。任何
// 首次通过 db.Open() 自举 (而不是先跑 075 迁移) 的环境都会创建"另一套"
// 表结构, 导致 seed 导入失败 (列不存在) 且 Resolver.queryDB /
// VirtualFactory.queryCatalog 的 SELECT 直接报错。
//
// 现在这个函数是 075-omnifree-schema.sql 的逐字段镜像 (含相同的
// CHECK 约束、UNIQUE 约束、RLS policy、辅助函数签名), 确保无论走
// "先迁移再启动" 还是 "直接启动自举" 两条路径, 最终落地的 schema
// 完全一致。对已经用旧 bootstrap 版本创建过表的环境, 下面的
// ADD COLUMN IF NOT EXISTS / DROP FUNCTION IF EXISTS 语句负责把旧列
// 补齐为迁移契约的列 (不删除旧列, 避免破坏尚未迁移代码的读取路径;
// 旧列若确认无用可在后续版本单独清理)。
//
// Tables ensured (契约来自 075-omnifree-schema.sql):
//   - free_resource_catalog: 免费 LLM 资源目录
//   - free_quota_tracker: 配额追踪表
//   - auto_combo_templates: auto/* 虚拟路由模板
//   - keyless_providers: keyless 提供商配置
//
// Extensions:
//   - provider_catalog.has_free_tier / free_tier_notes / official_free_docs_url
//   - credentials.is_free_tier / free_quota_window_type / free_quota_limit
//
// Also creates RLS policies, updated_at triggers, summary view, and helper functions.
func (d *DB) ensureOmniFreeSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}

	// ── 1. Create 4 new tables (与 075-omnifree-schema.sql §1-4 逐字段一致) ──
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.free_resource_catalog (
			id BIGSERIAL PRIMARY KEY,
			provider_code TEXT NOT NULL,
			model_id TEXT NOT NULL,
			display_name TEXT NOT NULL,
			display_name_en TEXT,
			free_type TEXT NOT NULL CHECK (free_type IN (
				'recurring-daily', 'recurring-monthly', 'one-time-initial',
				'recurring-credit', 'recurring-uncapped', 'keyless', 'discontinued'
			)),
			monthly_tokens BIGINT DEFAULT 0,
			daily_tokens BIGINT DEFAULT 0,
			credit_tokens BIGINT DEFAULT 0,
			pool_key TEXT,
			tos_verdict TEXT NOT NULL DEFAULT 'unknown' CHECK (tos_verdict IN (
				'ok', 'caution', 'ambiguous', 'avoid', 'unknown'
			)),
			tos_notes TEXT,
			tos_reviewed_at TIMESTAMPTZ,
			tos_reviewed_by TEXT,
			constraints_json JSONB DEFAULT '{}'::jsonb,
			discovery_method TEXT DEFAULT 'manual' CHECK (discovery_method IN (
				'manual', 'auto-scan', 'community', 'official-docs'
			)),
			verified_at TIMESTAMPTZ,
			last_probe_status TEXT,
			last_probe_error TEXT,
			enabled BOOLEAN DEFAULT TRUE,
			disabled_at TIMESTAMPTZ,
			disabled_reason TEXT,
			created_at TIMESTAMPTZ DEFAULT now(),
			updated_at TIMESTAMPTZ DEFAULT now(),
			tenant_id TEXT NOT NULL DEFAULT 'default',
			trains_on_prompts BOOLEAN NOT NULL DEFAULT FALSE,
			UNIQUE (provider_code, model_id, tenant_id)
		);

		-- 对已用旧 bootstrap 版本创建的表补齐迁移契约列 (幂等).
		ALTER TABLE public.free_resource_catalog
			ADD COLUMN IF NOT EXISTS trains_on_prompts BOOLEAN NOT NULL DEFAULT FALSE,
			ADD COLUMN IF NOT EXISTS tos_reviewed_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS tos_reviewed_by TEXT,
			ADD COLUMN IF NOT EXISTS constraints_json JSONB DEFAULT '{}'::jsonb,
			ADD COLUMN IF NOT EXISTS discovery_method TEXT DEFAULT 'manual',
			ADD COLUMN IF NOT EXISTS last_probe_error TEXT,
			ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS disabled_reason TEXT;

		CREATE TABLE IF NOT EXISTS public.free_quota_tracker (
			id BIGSERIAL PRIMARY KEY,
			credential_id BIGINT NOT NULL,
			provider_code TEXT NOT NULL,
			model_id TEXT NOT NULL,
			window_type TEXT NOT NULL CHECK (window_type IN ('hour-5', 'day-1', 'day-7', 'month-1')),
			window_start TIMESTAMPTZ NOT NULL,
			window_end TIMESTAMPTZ NOT NULL,
			request_count INT DEFAULT 0,
			token_count BIGINT DEFAULT 0,
			success_count INT DEFAULT 0,
			error_count INT DEFAULT 0,
			last_429_at TIMESTAMPTZ,
			last_429_reset_after INT,
			last_429_limit_header TEXT,
			corrected_limit INT,
			is_exhausted BOOLEAN DEFAULT FALSE,
			exhausted_at TIMESTAMPTZ,
			auto_reset_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ DEFAULT now(),
			updated_at TIMESTAMPTZ DEFAULT now(),
			tenant_id TEXT NOT NULL DEFAULT 'default',
			UNIQUE (credential_id, provider_code, model_id, window_type, window_start, tenant_id)
		);

		-- combo_name/variant/tier_filter/provider_allowlist 等是 Resolver.queryDB
		-- 与 cmd/seed-free-resources 实际读写的列名; 旧 bootstrap 版本用
		-- template_key/denylist_codes 等不同名字, 二者不兼容, 现在统一为
		-- 迁移契约的列名.
		CREATE TABLE IF NOT EXISTS public.auto_combo_templates (
			id BIGSERIAL PRIMARY KEY,
			combo_name TEXT NOT NULL,
			display_name TEXT NOT NULL,
			description TEXT,
			variant TEXT NOT NULL CHECK (variant IN (
				'cheap', 'fast', 'smart', 'coding', 'reasoning', 'creative', 'chaos'
			)),
			tier_filter TEXT[] DEFAULT ARRAY['free'],
			free_type_filter TEXT[],
			tos_filter TEXT[] DEFAULT ARRAY['ok', 'caution'],
			provider_allowlist TEXT[],
			provider_denylist TEXT[],
			model_pattern TEXT,
			scoring_weights_json JSONB DEFAULT '{
				"health_score": 0.3,
				"latency_p95": 0.2,
				"quota_remaining": 0.25,
				"cost": 0.0,
				"task_fit": 0.15,
				"tier_affinity": 0.1
			}'::jsonb,
			max_candidates INT DEFAULT 50,
			exploration_rate FLOAT DEFAULT 0.05,
			enabled BOOLEAN DEFAULT TRUE,
			priority INT DEFAULT 100,
			created_at TIMESTAMPTZ DEFAULT now(),
			updated_at TIMESTAMPTZ DEFAULT now(),
			tenant_id TEXT NOT NULL DEFAULT 'default',
			UNIQUE (combo_name, tenant_id)
		);

		CREATE TABLE IF NOT EXISTS public.keyless_providers (
			id BIGSERIAL PRIMARY KEY,
			provider_code TEXT NOT NULL,
			display_name TEXT NOT NULL,
			auth_hint TEXT,
			bootstrap_method TEXT,
			bootstrap_script TEXT,
			rpm_limit INT DEFAULT 20,
			rpd_limit INT DEFAULT 1000,
			concurrent_limit INT DEFAULT 5,
			reliability_score FLOAT DEFAULT 1.0,
			last_probe_at TIMESTAMPTZ,
			last_probe_status TEXT,
			consecutive_failures INT DEFAULT 0,
			enabled BOOLEAN DEFAULT TRUE,
			allowlist_in_auto_combo BOOLEAN DEFAULT FALSE,
			notes TEXT,
			created_at TIMESTAMPTZ DEFAULT now(),
			updated_at TIMESTAMPTZ DEFAULT now(),
			tenant_id TEXT NOT NULL DEFAULT 'default',
			CONSTRAINT keyless_providers_provider_tenant_key UNIQUE (provider_code, tenant_id)
		);
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: create tables: %w", err)
	}

	// ── 1.5 修复旧 bootstrap 版本遗留的不兼容列 (template_key 等) ──────────
	// 旧版本的 auto_combo_templates 用 template_key 做 UNIQUE key; 迁移
	// 契约用 combo_name。如果历史环境已经跑过旧 bootstrap 版本 (无
	// combo_name 列但有 template_key), 这里把 template_key 的数据搬到
	// combo_name, 避免新代码路径 (Resolver.queryDB) 报 "column combo_name
	// does not exist"。仅在 combo_name 缺失且 template_key 存在时执行。
	_, err = d.pool.Exec(ctx, `
		DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='template_key'
			) AND NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='combo_name'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN combo_name TEXT;
				UPDATE public.auto_combo_templates SET combo_name = template_key WHERE combo_name IS NULL;
				ALTER TABLE public.auto_combo_templates ALTER COLUMN combo_name SET NOT NULL;
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='variant'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN variant TEXT NOT NULL DEFAULT 'cheap';
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='tier_filter'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN tier_filter TEXT[] DEFAULT ARRAY['free'];
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='provider_allowlist'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN provider_allowlist TEXT[];
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='provider_denylist'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN provider_denylist TEXT[];
				IF EXISTS (
					SELECT 1 FROM information_schema.columns
					 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='denylist_codes'
				) THEN
					UPDATE public.auto_combo_templates SET provider_denylist = denylist_codes WHERE provider_denylist IS NULL;
				END IF;
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='model_pattern'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN model_pattern TEXT;
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='scoring_weights_json'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN scoring_weights_json JSONB DEFAULT '{
					"health_score": 0.3, "latency_p95": 0.2, "quota_remaining": 0.25,
					"cost": 0.0, "task_fit": 0.15, "tier_affinity": 0.1
				}'::jsonb;
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='max_candidates'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN max_candidates INT DEFAULT 50;
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='exploration_rate'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN exploration_rate FLOAT DEFAULT 0.05;
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema='public' AND table_name='auto_combo_templates' AND column_name='priority'
			) THEN
				ALTER TABLE public.auto_combo_templates ADD COLUMN priority INT DEFAULT 100;
			END IF;
			-- combo_name/tenant_id 上的 UNIQUE 约束 (迁移契约用的 conflict key).
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				 WHERE conrelid = 'public.auto_combo_templates'::regclass
				   AND contype = 'u'
				   AND conkey = (
						SELECT array_agg(attnum ORDER BY attnum) FROM pg_attribute
						 WHERE attrelid = 'public.auto_combo_templates'::regclass
						   AND attname IN ('combo_name', 'tenant_id')
					)
			) THEN
				BEGIN
					ALTER TABLE public.auto_combo_templates
						ADD CONSTRAINT auto_combo_templates_combo_name_tenant_id_key UNIQUE (combo_name, tenant_id);
				EXCEPTION WHEN duplicate_table OR duplicate_object THEN
					NULL; -- 约束已存在 (换个名字), 忽略.
				END;
			END IF;
		END $$;
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: reconcile auto_combo_templates legacy columns: %w", err)
	}

	// ── 2. Extend provider_catalog + credentials (与 075-omnifree-schema.sql §5 一致) ──
	_, err = d.pool.Exec(ctx, `
		DO $$
		BEGIN
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
				CREATE INDEX IF NOT EXISTS idx_provider_catalog_free_tier
					ON provider_catalog(code) WHERE has_free_tier = TRUE;
			END IF;

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
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: extend tables: %w", err)
	}

	// ── 3. Create indexes (与 075-omnifree-schema.sql §1-4 索引一致) ────────
	_, err = d.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_provider ON free_resource_catalog(provider_code);
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_free_type ON free_resource_catalog(free_type) WHERE enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_tos ON free_resource_catalog(tos_verdict) WHERE enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_pool_key ON free_resource_catalog(pool_key) WHERE pool_key IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_tenant ON free_resource_catalog(tenant_id);

		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_credential ON free_quota_tracker(credential_id, window_type);
		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_provider_model ON free_quota_tracker(provider_code, model_id);
		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_exhausted ON free_quota_tracker(is_exhausted, auto_reset_at)
			WHERE is_exhausted = TRUE;
		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_window ON free_quota_tracker(window_start, window_end);
		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_tenant ON free_quota_tracker(tenant_id);
		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_cleanup ON free_quota_tracker(auto_reset_at);

		CREATE INDEX IF NOT EXISTS idx_keyless_providers_enabled ON keyless_providers(provider_code) WHERE enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_keyless_providers_auto_combo ON keyless_providers(provider_code)
			WHERE enabled = TRUE AND allowlist_in_auto_combo = TRUE;
		CREATE INDEX IF NOT EXISTS idx_keyless_providers_tenant ON keyless_providers(tenant_id);
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: create indexes: %w", err)
	}

	// combo_name/variant 依赖的索引单独建 (前面已保证列存在).
	_, err = d.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_auto_combo_templates_variant ON auto_combo_templates(variant) WHERE enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_auto_combo_templates_name ON auto_combo_templates(combo_name);
		CREATE INDEX IF NOT EXISTS idx_auto_combo_templates_tenant ON auto_combo_templates(tenant_id);
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: create auto_combo_templates indexes: %w", err)
	}

	// ── 4. Normalize tenant_id columns to TEXT (from legacy BIGINT) ───────
	_, err = d.pool.Exec(ctx, `
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
				 WHERE n.nspname = 'public' AND c.relname = table_name AND a.attname = 'tenant_id'
				   AND NOT a.attisdropped;

				IF tenant_type IS NOT NULL AND tenant_type <> 'text' THEN
					EXECUTE format('ALTER TABLE %I ALTER COLUMN tenant_id DROP DEFAULT', table_name);
					EXECUTE format('ALTER TABLE %I ALTER COLUMN tenant_id TYPE TEXT USING tenant_id::TEXT', table_name);
				END IF;
				EXECUTE format('ALTER TABLE %I ALTER COLUMN tenant_id SET DEFAULT ''default''', table_name);
			END LOOP;
		END $$;
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: normalize tenant_id: %w", err)
	}

	// ── 5. Create get_current_tenant() + RLS policies ─────────────────────
	// get_current_tenant() 也在 db.go::applyMigrationsOnce 里 CREATE OR
	// REPLACE 过一次 (MaaS schema 的前置依赖); 这里保持一致定义, 用
	// CREATE OR REPLACE 幂等覆盖, 不依赖执行顺序.
	_, err = d.pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION public.get_current_tenant()
		RETURNS text
		LANGUAGE sql
		STABLE
		AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $$;

		DO $$
		DECLARE
			table_name text;
		BEGIN
			FOREACH table_name IN ARRAY ARRAY[
				'free_resource_catalog', 'free_quota_tracker',
				'auto_combo_templates', 'keyless_providers'
			] LOOP
				EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
				EXECUTE format('DROP POLICY IF EXISTS tenant_isolation_policy ON %I', table_name);
				EXECUTE format('DROP POLICY IF EXISTS tenant_isolation_%I ON %I', table_name, table_name);
				EXECUTE format(
					'CREATE POLICY tenant_isolation_%I ON %I USING (tenant_id = public.get_current_tenant() OR current_setting(''app.current_role'', true) = ''super_admin'' OR current_setting(''app.bypass_rls'', true) = ''true'') WITH CHECK (tenant_id = public.get_current_tenant() OR current_setting(''app.current_role'', true) = ''super_admin'' OR current_setting(''app.bypass_rls'', true) = ''true'')',
					table_name, table_name
				);
			END LOOP;
		END $$;
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: create RLS policies: %w", err)
	}

	// ── 6. Create updated_at triggers ─────────────────────────────────────
	_, err = d.pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION update_updated_at_column()
		RETURNS TRIGGER AS $$
		BEGIN
			NEW.updated_at = now();
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS update_free_resource_catalog_updated_at ON free_resource_catalog;
		CREATE TRIGGER update_free_resource_catalog_updated_at
			BEFORE UPDATE ON free_resource_catalog
			FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

		DROP TRIGGER IF EXISTS update_free_quota_tracker_updated_at ON free_quota_tracker;
		CREATE TRIGGER update_free_quota_tracker_updated_at
			BEFORE UPDATE ON free_quota_tracker
			FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

		DROP TRIGGER IF EXISTS update_auto_combo_templates_updated_at ON auto_combo_templates;
		CREATE TRIGGER update_auto_combo_templates_updated_at
			BEFORE UPDATE ON auto_combo_templates
			FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

		DROP TRIGGER IF EXISTS update_keyless_providers_updated_at ON keyless_providers;
		CREATE TRIGGER update_keyless_providers_updated_at
			BEFORE UPDATE ON keyless_providers
			FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: create triggers: %w", err)
	}

	// ── 7. Create summary view + helper functions (与 075-omnifree-schema.sql §6-7 签名一致) ──
	// 先 drop 旧 bootstrap 版本可能创建的不同签名重载, 避免
	// "function is not unique" / 残留旧签名的问题.
	_, err = d.pool.Exec(ctx, `
		DROP FUNCTION IF EXISTS fn_compute_deduped_quota(TEXT);
		DROP FUNCTION IF EXISTS fn_compute_deduped_quota(BIGINT, TEXT[]);
		DROP FUNCTION IF EXISTS fn_quota_preflight_check(BIGINT, TEXT, TEXT, INT, FLOAT, TEXT);
		DROP FUNCTION IF EXISTS fn_quota_preflight_check(BIGINT, TEXT, TEXT, FLOAT);

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
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: create view+functions: %w", err)
	}

	slog.Info("omnifree schema ensured (4 tables + extensions + RLS + triggers + view + 2 functions, migration-contract aligned)")
	return nil
}

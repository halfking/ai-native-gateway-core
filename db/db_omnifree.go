package db

import (
	"context"
	"fmt"
	"log/slog"
)

// ensureOmniFreeSchema creates the OmniFree data model (4 tables + extensions + RLS + triggers).
// Equivalent to sql/migrations/075-omnifree-schema.sql but idempotent and startup-safe.
//
// Tables created:
//   - free_resource_catalog: 免费 LLM 资源目录
//   - free_quota_tracker: 配额追踪表
//   - auto_combo_templates: auto/* 虚拟路由模板
//   - keyless_providers: keyless 提供商配置
//
// Extensions:
//   - provider_catalog.has_free_tier / free_tier_notes
//   - credentials.is_free_tier / free_quota_limit
//
// Also creates RLS policies, updated_at triggers, summary view, and helper functions.
func (d *DB) ensureOmniFreeSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}

	// ── 1. Create 4 new tables ────────────────────────────────────────────
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
			tos_verdict TEXT DEFAULT 'caution' CHECK (tos_verdict IN ('ok', 'caution', 'avoid')),
			tos_notes TEXT,
			signup_url TEXT,
			api_endpoint TEXT,
			capability_modalities TEXT[] DEFAULT ARRAY['text-to-text'],
			capability_vision BOOLEAN DEFAULT FALSE,
			capability_tool_use BOOLEAN DEFAULT FALSE,
			notes TEXT,
			verified_at TIMESTAMPTZ,
			last_probe_at TIMESTAMPTZ,
			last_probe_status TEXT,
			enabled BOOLEAN DEFAULT TRUE,
			allowlist_in_auto_combo BOOLEAN DEFAULT FALSE,
			created_at TIMESTAMPTZ DEFAULT now(),
			updated_at TIMESTAMPTZ DEFAULT now(),
			tenant_id TEXT NOT NULL DEFAULT 'default',
			UNIQUE (tenant_id, provider_code, model_id)
		);

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

		CREATE TABLE IF NOT EXISTS public.auto_combo_templates (
			id BIGSERIAL PRIMARY KEY,
			template_key TEXT NOT NULL,
			display_name TEXT NOT NULL,
			display_name_en TEXT,
			description TEXT,
			free_type_filter TEXT[] DEFAULT '{}',
			tos_filter TEXT[] DEFAULT ARRAY['ok', 'caution'],
			denylist_codes TEXT[] DEFAULT '{}',
			pool_keys TEXT[] DEFAULT '{}',
			capability_modalities TEXT[] DEFAULT ARRAY['text-to-text'],
			capability_vision BOOLEAN,
			capability_tool_use BOOLEAN,
			capability_image_generation BOOLEAN,
			tier_preference TEXT[] DEFAULT ARRAY['standard', 'premium', 'keyless'],
			sort_by TEXT DEFAULT 'health' CHECK (sort_by IN ('health', 'latency', 'cost', 'random')),
			enabled BOOLEAN DEFAULT TRUE,
			notes TEXT,
			created_at TIMESTAMPTZ DEFAULT now(),
			updated_at TIMESTAMPTZ DEFAULT now(),
			tenant_id TEXT NOT NULL DEFAULT 'default',
			UNIQUE (tenant_id, template_key)
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
			UNIQUE (tenant_id, provider_code)
		);
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: create tables: %w", err)
	}

	// ── 2. Extend provider_catalog + credentials ──────────────────────────
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
			END IF;

			IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='credentials') THEN
				IF NOT EXISTS (SELECT 1 FROM information_schema.columns
							   WHERE table_name='credentials' AND column_name='is_free_tier') THEN
					ALTER TABLE public.credentials ADD COLUMN is_free_tier BOOLEAN DEFAULT FALSE;
				END IF;
				IF NOT EXISTS (SELECT 1 FROM information_schema.columns
							   WHERE table_name='credentials' AND column_name='free_quota_limit') THEN
					ALTER TABLE public.credentials ADD COLUMN free_quota_limit INT;
				END IF;
			END IF;
		END $$;
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: extend tables: %w", err)
	}

	// ── 3. Create indexes ─────────────────────────────────────────────────
	_, err = d.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_lookup
			ON free_resource_catalog(provider_code, model_id, tenant_id) WHERE enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_type
			ON free_resource_catalog(free_type) WHERE enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_tos
			ON free_resource_catalog(tos_verdict) WHERE enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_pool_key
			ON free_resource_catalog(pool_key) WHERE pool_key IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_tenant
			ON free_resource_catalog(tenant_id);

		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_lookup
			ON free_quota_tracker(credential_id, provider_code, model_id, window_type, tenant_id);
		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_window
			ON free_quota_tracker(window_start, window_end);
		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_exhausted
			ON free_quota_tracker(is_exhausted, auto_reset_at) WHERE is_exhausted = TRUE;
		CREATE INDEX IF NOT EXISTS idx_free_quota_tracker_tenant
			ON free_quota_tracker(tenant_id);

		CREATE INDEX IF NOT EXISTS idx_auto_combo_templates_key
			ON auto_combo_templates(template_key, tenant_id) WHERE enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_auto_combo_templates_tenant
			ON auto_combo_templates(tenant_id);

		CREATE INDEX IF NOT EXISTS idx_keyless_providers_code
			ON keyless_providers(provider_code, tenant_id) WHERE enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_keyless_providers_allowlist
			ON keyless_providers(allowlist_in_auto_combo) WHERE allowlist_in_auto_combo = TRUE AND enabled = TRUE;
		CREATE INDEX IF NOT EXISTS idx_keyless_providers_tenant
			ON keyless_providers(tenant_id);

		CREATE INDEX IF NOT EXISTS idx_credentials_free_tier
			ON credentials(provider_id, is_free_tier)
			WHERE is_free_tier = TRUE
			  AND status IN ('active', 'cooling', 'degraded')
			  AND lifecycle_status = 'active';
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: create indexes: %w", err)
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
				 WHERE n.nspname = 'public' AND c.relname = table_name AND a.attname = 'tenant_id';

				IF tenant_type = 'bigint' THEN
					EXECUTE format('ALTER TABLE %I ALTER COLUMN tenant_id TYPE TEXT USING tenant_id::TEXT', table_name);
					EXECUTE format('ALTER TABLE %I ALTER COLUMN tenant_id SET DEFAULT ''default''', table_name);
				END IF;
			END LOOP;
		END $$;
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: normalize tenant_id: %w", err)
	}

	// ── 5. Create RLS policies ────────────────────────────────────────────
	_, err = d.pool.Exec(ctx, `
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

	// ── 7. Create summary view + helper functions ─────────────────────────
	_, err = d.pool.Exec(ctx, `
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

		CREATE OR REPLACE FUNCTION fn_compute_deduped_quota(p_tenant_id TEXT DEFAULT 'default')
		RETURNS TABLE(
			provider_code TEXT,
			model_id TEXT,
			pool_key TEXT,
			effective_monthly_tokens BIGINT,
			effective_daily_tokens BIGINT
		) LANGUAGE plpgsql STABLE AS $$
		BEGIN
			RETURN QUERY
			WITH ranked AS (
				SELECT
					frc.provider_code,
					frc.model_id,
					frc.pool_key,
					frc.monthly_tokens,
					frc.daily_tokens,
					ROW_NUMBER() OVER (PARTITION BY frc.pool_key ORDER BY frc.monthly_tokens DESC NULLS LAST) AS rn
				FROM free_resource_catalog frc
				WHERE frc.enabled = TRUE
				  AND frc.tenant_id = p_tenant_id
				  AND frc.pool_key IS NOT NULL
			)
			SELECT
				r.provider_code,
				r.model_id,
				r.pool_key,
				CASE WHEN r.rn = 1 THEN r.monthly_tokens ELSE 0 END AS effective_monthly_tokens,
				CASE WHEN r.rn = 1 THEN r.daily_tokens ELSE 0 END AS effective_daily_tokens
			FROM ranked r
			UNION ALL
			SELECT
				frc.provider_code,
				frc.model_id,
				NULL AS pool_key,
				frc.monthly_tokens,
				frc.daily_tokens
			FROM free_resource_catalog frc
			WHERE frc.enabled = TRUE
			  AND frc.tenant_id = p_tenant_id
			  AND frc.pool_key IS NULL;
		END;
		$$;

		CREATE OR REPLACE FUNCTION fn_quota_preflight_check(
			p_credential_id BIGINT,
			p_provider_code TEXT,
			p_model_id TEXT,
			p_default_limit INT DEFAULT 1000,
			p_min_remaining_pct FLOAT DEFAULT 0.1,
			p_tenant_id TEXT DEFAULT 'default'
		)
		RETURNS BOOLEAN LANGUAGE plpgsql STABLE AS $$
		DECLARE
			v_limit INT;
			v_used INT;
			v_exhausted BOOLEAN;
			v_reset_at TIMESTAMPTZ;
		BEGIN
			SELECT
				COALESCE(corrected_limit, p_default_limit),
				request_count,
				is_exhausted,
				auto_reset_at
			INTO v_limit, v_used, v_exhausted, v_reset_at
			FROM free_quota_tracker
			WHERE credential_id = p_credential_id
			  AND provider_code = p_provider_code
			  AND model_id = p_model_id
			  AND window_type = 'day-1'
			  AND window_start <= now()
			  AND window_end >= now()
			  AND tenant_id = p_tenant_id;

			IF NOT FOUND THEN
				RETURN TRUE;
			END IF;

			IF v_exhausted THEN
				IF v_reset_at IS NOT NULL AND now() >= v_reset_at THEN
					RETURN TRUE;
				END IF;
				RETURN FALSE;
			END IF;

			IF v_limit > 0 THEN
				RETURN (v_limit - v_used)::float / v_limit >= p_min_remaining_pct;
			END IF;

			RETURN TRUE;
		END;
		$$;
	`)
	if err != nil {
		return fmt.Errorf("ensureOmniFreeSchema: create view+functions: %w", err)
	}

	slog.Info("omnifree schema ensured (4 tables + extensions + RLS + triggers + view + 2 functions)")
	return nil
}

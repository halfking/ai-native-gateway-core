-- ============================================================================
-- llm-gateway-go 提供商和模型相关表结构
-- ============================================================================

-- ----------------------------------------------------------------------------
-- Providers 表 - LLM提供商
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.providers (
    id SERIAL PRIMARY KEY,
    code TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    api_type TEXT NOT NULL,
    base_url TEXT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    requires_gateway BOOLEAN NOT NULL DEFAULT FALSE,
    http_signature_header JSONB,
    auth_type VARCHAR(32) NOT NULL DEFAULT 'bearer',
    extra JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_providers_enabled ON providers(enabled);

-- ----------------------------------------------------------------------------
-- Models Canonical 表 - 标准模型定义
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.models_canonical (
    id SERIAL PRIMARY KEY,
    canonical_name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    provider_id INT,
    mode TEXT NOT NULL DEFAULT 'chat'
        CHECK (mode IN ('chat', 'embedding', 'rerank', 'image', 'audio', 'video')),
    modal TEXT NOT NULL DEFAULT 'text'
        CHECK (modal IN ('text', 'image', 'audio', 'video', 'multi')),
    context_window INT,
    max_output_tokens INT,
    input_price_per_1m NUMERIC(10,2),
    output_price_per_1m NUMERIC(10,2),
    tuned_versions TEXT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    tags TEXT[] DEFAULT '{}',
    released_at TIMESTAMPTZ,
    deprecation_notice JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_canonical_name ON models_canonical(canonical_name);
CREATE INDEX IF NOT EXISTS idx_canonical_mode ON models_canonical(mode);
CREATE INDEX IF NOT EXISTS idx_canonical_enabled ON models_canonical(enabled);

-- ----------------------------------------------------------------------------
-- Provider Header Profiles 表 - 提供商HTTP头配置
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_header_profiles (
    id SERIAL PRIMARY KEY,
    provider_id INT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    profile TEXT NOT NULL,
    headers JSONB NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(provider_id, profile)
);

-- ----------------------------------------------------------------------------
-- Model Families 表 - 模型家族
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_families (
    id SERIAL PRIMARY KEY,
    family_name TEXT NOT NULL,
    provider_id INT REFERENCES providers(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_model_families_name ON model_families(family_name);

-- ----------------------------------------------------------------------------
-- Provider Models 表 - 提供商的具体模型
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_models (
    id SERIAL PRIMARY KEY,
    provider_id INT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    raw_model_name TEXT NOT NULL,
    canonical_name TEXT NOT NULL,
    family_id INT REFERENCES model_families(id) ON DELETE SET NULL,
    is_deprecated BOOLEAN NOT NULL DEFAULT FALSE,
    is_new BOOLEAN NOT NULL DEFAULT FALSE,
    deprecation_info JSONB,
    cost_context_tokens INT,
    supports_streaming BOOLEAN DEFAULT TRUE,
    supports_function_calling BOOLEAN DEFAULT FALSE,
    supports_vision BOOLEAN DEFAULT FALSE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    route_hint TEXT,
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT unique_provider_model UNIQUE(provider_id, raw_model_name)
);
CREATE INDEX IF NOT EXISTS idx_pm_canonical ON provider_models(canonical_name);
CREATE INDEX IF NOT EXISTS idx_pm_provider ON provider_models(provider_id);

-- ----------------------------------------------------------------------------
-- Model Aliases 表 - 模型别名
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_aliases (
    id SERIAL PRIMARY KEY,
    alias TEXT NOT NULL UNIQUE,
    canonical_name TEXT NOT NULL REFERENCES models_canonical(canonical_name),
    tenant_id TEXT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ----------------------------------------------------------------------------
-- Model Fingerprints 表 - 模型指纹（用于路由决策）
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_fingerprints (
    canonical_name TEXT PRIMARY KEY,
    fingerprint JSONB NOT NULL,
    route_hint TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ----------------------------------------------------------------------------
-- Model Task Index 表 - 模型任务索引
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_task_index (
    id SERIAL PRIMARY KEY,
    canonical_name TEXT NOT NULL,
    task_type TEXT NOT NULL,
    score NUMERIC(5,4) NOT NULL DEFAULT 0,
    samples INT NOT NULL DEFAULT 0,
    last_updated TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_mti_unique ON model_task_index(canonical_name, task_type);

-- ----------------------------------------------------------------------------
-- Provider Quality Rollup 表 - 提供商质量汇总
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_quality_rollup (
    provider_id INT NOT NULL,
    bucket_start TIMESTAMPTZ NOT NULL,
    total_requests INT NOT NULL DEFAULT 0,
    bad_requests INT NOT NULL DEFAULT 0,
    fixed_requests INT NOT NULL DEFAULT 0,
    avg_quality_score NUMERIC(3,2),
    top_flag TEXT,
    PRIMARY KEY (provider_id, bucket_start)
);
CREATE INDEX IF NOT EXISTS idx_provider_quality_rollup_bucket
    ON provider_quality_rollup (bucket_start DESC);

-- ----------------------------------------------------------------------------
-- Provider Settings 表 - 提供商设置
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_settings (
    provider_id INT PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
    settings JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ----------------------------------------------------------------------------
-- Provider Scores 表 - 提供商评分
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_scores (
    provider_id INT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    window_start TIMESTAMPTZ NOT NULL,
    score NUMERIC(5,4) NOT NULL DEFAULT 0,
    total_requests INT NOT NULL DEFAULT 0,
    avg_latency_ms NUMERIC(10,2),
    PRIMARY KEY (provider_id, window_start)
);

-- ----------------------------------------------------------------------------
-- Provider Catalog 表 - 提供商目录
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_catalog (
    id SERIAL PRIMARY KEY,
    provider_id INT REFERENCES providers(id) ON DELETE CASCADE,
    catalog_data JSONB NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
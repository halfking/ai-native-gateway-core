-- ============================================================================
-- llm-gateway-go 提供商和模型相关表结构
-- Source of truth: 184 / llm_gateway / pg_dump --schema-only (2026-06-27)
-- ============================================================================

-- ----------------------------------------------------------------------------
-- model_aliases
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_aliases (
    id bigint NOT NULL,
    canonical_id bigint NOT NULL,
    raw_name text NOT NULL,
    quantization text,
    surface text,
    status text DEFAULT 'active'::text NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    client_profiles text[],
    CONSTRAINT model_aliases_status_check CHECK (
        status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])
    )
);
CREATE SEQUENCE IF NOT EXISTS public.model_aliases_id_seq START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE public.model_aliases_id_seq OWNED BY public.model_aliases.id;
ALTER TABLE ONLY public.model_aliases ALTER COLUMN id SET DEFAULT nextval('public.model_aliases_id_seq'::regclass);

-- ----------------------------------------------------------------------------
-- model_families
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_families (
    id text NOT NULL,
    display_name text NOT NULL,
    vendor text,
    status text DEFAULT 'active'::text NOT NULL,
    source text DEFAULT 'db'::text NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_families_status_check CHECK (
        status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])
    )
);

-- ----------------------------------------------------------------------------
-- model_fingerprints
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_fingerprints (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint NOT NULL,
    fingerprint_hash text NOT NULL,
    sampled_features_json jsonb,
    last_verified_at timestamp with time zone,
    drift_detected boolean DEFAULT false NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.model_fingerprints_id_seq START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE public.model_fingerprints_id_seq OWNED BY public.model_fingerprints.id;
ALTER TABLE ONLY public.model_fingerprints ALTER COLUMN id SET DEFAULT nextval('public.model_fingerprints_id_seq'::regclass);

-- ----------------------------------------------------------------------------
-- provider_models
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_models (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    raw_model_name text NOT NULL,
    canonical_id bigint,
    standardized_name text,
    outbound_model_name text,
    available boolean DEFAULT true NOT NULL,
    unavailable_reason text,
    unavailable_at timestamp with time zone,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.provider_models_id_seq START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE public.provider_models_id_seq OWNED BY public.provider_models.id;
ALTER TABLE ONLY public.provider_models ALTER COLUMN id SET DEFAULT nextval('public.provider_models_id_seq'::regclass);

-- ----------------------------------------------------------------------------
-- model_task_index
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_task_index (
    bucket timestamp with time zone NOT NULL,
    canonical_id integer NOT NULL,
    task_type text NOT NULL,
    sample_count integer DEFAULT 0 NOT NULL,
    success_rate numeric(5,4),
    avg_latency_ms integer,
    p95_latency_ms integer,
    avg_cost_per_1k_usd numeric(10,6),
    primary_credential_id bigint,
    updated_at timestamp with time zone DEFAULT now()
);

-- ----------------------------------------------------------------------------
-- models_canonical
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.models_canonical (
    id bigint NOT NULL,
    canonical_name text NOT NULL,
    family text,
    parameters_b numeric(8,2),
    modality text DEFAULT 'text'::text NOT NULL,
    context_window integer,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    tags_locked boolean DEFAULT false NOT NULL,
    tags_updated_at timestamp with time zone,
    display_name text,
    status text DEFAULT 'active'::text NOT NULL,
    source text DEFAULT 'db'::text NOT NULL,
    disabled_reason text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    input_price_cny numeric(10,4) DEFAULT 0,
    output_price_cny numeric(10,4) DEFAULT 0,
    released_at date,
    strengths text[] DEFAULT '{}'::text[] NOT NULL,
    cost_tier text DEFAULT 'unknown'::text NOT NULL,
    multimodal_caps text[] DEFAULT '{}'::text[] NOT NULL,
    version_rank integer,
    CONSTRAINT models_canonical_cost_tier_check CHECK (
        cost_tier = ANY (ARRAY['free'::text, 'low'::text, 'medium'::text, 'high'::text, 'premium'::text, 'unknown'::text])
    ),
    CONSTRAINT models_canonical_modality_check CHECK (
        modality = ANY (ARRAY['text'::text, 'vision'::text, 'audio'::text, 'multimodal'::text, 'embedding'::text])
    ),
    CONSTRAINT models_canonical_status_check CHECK (
        status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])
    )
);
CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE public.models_canonical_id_seq OWNED BY public.models_canonical.id;
ALTER TABLE ONLY public.models_canonical ALTER COLUMN id SET DEFAULT nextval('public.models_canonical_id_seq'::regclass);
CREATE INDEX IF NOT EXISTS idx_models_canonical_released ON public.models_canonical USING btree (released_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_models_canonical_strengths ON public.models_canonical USING gin (strengths);
CREATE INDEX IF NOT EXISTS idx_models_canonical_version_rank ON public.models_canonical USING btree (version_rank);

-- ----------------------------------------------------------------------------
-- provider_catalog
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_catalog (
    code text NOT NULL,
    tier text NOT NULL,
    display_name text NOT NULL,
    display_name_en text,
    category text DEFAULT 'official'::text NOT NULL,
    kind text DEFAULT 'cloud'::text NOT NULL,
    protocol text NOT NULL,
    base_url_template text NOT NULL,
    docs_url text,
    default_egress_profile text DEFAULT 'direct'::text NOT NULL,
    domestic boolean DEFAULT true NOT NULL,
    discount_rate_default numeric(5,4) DEFAULT 1.0,
    models_manifest_json jsonb DEFAULT '[]'::jsonb,
    discovery_strategy text DEFAULT 'auto'::text NOT NULL,
    models_endpoint_template text,
    seed_pricing_plans_json jsonb DEFAULT '[]'::jsonb,
    price_sources_json jsonb DEFAULT '{}'::jsonb,
    hidden boolean DEFAULT false NOT NULL,
    notes text,
    catalog_version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    header_profile_code text,
    capabilities jsonb DEFAULT '{}'::jsonb,
    vendor_name text,
    CONSTRAINT provider_catalog_category_check CHECK (
        category = ANY (ARRAY['official'::text, 'official_proxy'::text, 'third_party_relay'::text, 'aggregator'::text, 'self_host'::text])
    ),
    CONSTRAINT provider_catalog_discovery_strategy_check CHECK (
        discovery_strategy = ANY (ARRAY['auto'::text, 'manifest'::text, 'hybrid'::text])
    ),
    CONSTRAINT provider_catalog_kind_check CHECK (
        kind = ANY (ARRAY['cloud'::text, 'local'::text])
    ),
    CONSTRAINT provider_catalog_protocol_check CHECK (
        protocol = ANY (ARRAY['openai-completions'::text, 'openai-responses'::text, 'anthropic-messages'::text, 'gemini-generate'::text, 'ollama-native'::text])
    ),
    CONSTRAINT provider_catalog_tier_check CHECK (
        tier = ANY (ARRAY['tier1'::text, 'tier2'::text, 'local'::text, 'restricted'::text])
    )
);

-- ----------------------------------------------------------------------------
-- provider_header_profiles
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_header_profiles (
    id bigint NOT NULL,
    profile_code text NOT NULL,
    display_name text NOT NULL,
    protocol text,
    headers_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    strip_headers_json jsonb DEFAULT '[]'::jsonb NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.provider_header_profiles_id_seq START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE public.provider_header_profiles_id_seq OWNED BY public.provider_header_profiles.id;
ALTER TABLE ONLY public.provider_header_profiles ALTER COLUMN id SET DEFAULT nextval('public.provider_header_profiles_id_seq'::regclass);

-- ----------------------------------------------------------------------------
-- provider_quality_rollup
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_quality_rollup (
    provider_id integer NOT NULL,
    bucket_start timestamp with time zone NOT NULL,
    total_requests integer DEFAULT 0 NOT NULL,
    bad_requests integer DEFAULT 0 NOT NULL,
    fixed_requests integer DEFAULT 0 NOT NULL,
    avg_quality_score numeric(3,2),
    top_flag text
);
CREATE INDEX IF NOT EXISTS idx_provider_quality_rollup_bucket ON public.provider_quality_rollup USING btree (bucket_start DESC);

-- ----------------------------------------------------------------------------
-- provider_scores
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_scores (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    score numeric(6,4) NOT NULL,
    factors_json jsonb,
    computed_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.provider_scores_id_seq START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE public.provider_scores_id_seq OWNED BY public.provider_scores.id;
ALTER TABLE ONLY public.provider_scores ALTER COLUMN id SET DEFAULT nextval('public.provider_scores_id_seq'::regclass);

-- ----------------------------------------------------------------------------
-- provider_settings
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.provider_settings (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    setting_key text NOT NULL,
    setting_value jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_by text DEFAULT 'system'::text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.provider_settings_id_seq START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE public.provider_settings_id_seq OWNED BY public.provider_settings.id;
ALTER TABLE ONLY public.provider_settings ALTER COLUMN id SET DEFAULT nextval('public.provider_settings_id_seq'::regclass);
CREATE INDEX IF NOT EXISTS idx_provider_settings_key ON public.provider_settings USING btree (setting_key) WHERE (enabled = true);
CREATE INDEX IF NOT EXISTS idx_provider_settings_provider ON public.provider_settings USING btree (provider_id) WHERE (enabled = true);

-- ----------------------------------------------------------------------------
-- providers
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.providers (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    code text NOT NULL,
    display_name text NOT NULL,
    catalog_code text,
    is_custom boolean DEFAULT false NOT NULL,
    catalog_version_at_create integer,
    user_overrides_json jsonb DEFAULT '[]'::jsonb NOT NULL,
    kind text DEFAULT 'cloud'::text NOT NULL,
    category text DEFAULT 'official'::text NOT NULL,
    protocol text NOT NULL,
    base_url text NOT NULL,
    egress_profile text DEFAULT 'direct'::text NOT NULL,
    domestic boolean DEFAULT true NOT NULL,
    discount_rate numeric(5,4) DEFAULT 1.0,
    enabled boolean DEFAULT true NOT NULL,
    network_quality_score numeric(4,3) DEFAULT 1.000,
    owner_user text,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    manual_disabled boolean DEFAULT false NOT NULL,
    quality_fix_mode text DEFAULT 'off'::text NOT NULL,
    CONSTRAINT providers_category_check CHECK (
        category = ANY (ARRAY['official'::text, 'official_proxy'::text, 'third_party_relay'::text, 'aggregator'::text, 'self_host'::text])
    ),
    CONSTRAINT providers_kind_check CHECK (
        kind = ANY (ARRAY['cloud'::text, 'local'::text])
    ),
    CONSTRAINT providers_quality_fix_mode_check CHECK (
        quality_fix_mode = ANY (ARRAY['off'::text, 'detect_only'::text, 'fix'::text])
    )
);
CREATE SEQUENCE IF NOT EXISTS public.providers_id_seq START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE public.providers_id_seq OWNED BY public.providers.id;
ALTER TABLE ONLY public.providers ALTER COLUMN id SET DEFAULT nextval('public.providers_id_seq'::regclass);

-- ----------------------------------------------------------------------------
-- Key constraints from 184 baseline
-- ----------------------------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'model_fingerprints_credential_id_canonical_id_key'
          AND conrelid = 'public.model_fingerprints'::regclass
    ) THEN
        ALTER TABLE ONLY public.model_fingerprints
            ADD CONSTRAINT model_fingerprints_credential_id_canonical_id_key
            UNIQUE (credential_id, canonical_id);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'provider_models_unique_provider_model'
          AND conrelid = 'public.provider_models'::regclass
    ) THEN
        ALTER TABLE ONLY public.provider_models
            ADD CONSTRAINT provider_models_unique_provider_model
            UNIQUE (provider_id, raw_model_name);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'model_task_index_bucket_canonical_task_key'
          AND conrelid = 'public.model_task_index'::regclass
    ) THEN
        ALTER TABLE ONLY public.model_task_index
            ADD CONSTRAINT model_task_index_bucket_canonical_task_key
            UNIQUE (bucket, canonical_id, task_type);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'models_canonical_pkey'
          AND conrelid = 'public.models_canonical'::regclass
    ) THEN
        ALTER TABLE ONLY public.models_canonical
            ADD CONSTRAINT models_canonical_pkey PRIMARY KEY (id);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'models_canonical_canonical_name_key'
          AND conrelid = 'public.models_canonical'::regclass
    ) THEN
        ALTER TABLE ONLY public.models_canonical
            ADD CONSTRAINT models_canonical_canonical_name_key
            UNIQUE (canonical_name);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'provider_header_profiles_profile_code_key'
          AND conrelid = 'public.provider_header_profiles'::regclass
    ) THEN
        ALTER TABLE ONLY public.provider_header_profiles
            ADD CONSTRAINT provider_header_profiles_profile_code_key
            UNIQUE (profile_code);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'provider_quality_rollup_pkey'
          AND conrelid = 'public.provider_quality_rollup'::regclass
    ) THEN
        ALTER TABLE ONLY public.provider_quality_rollup
            ADD CONSTRAINT provider_quality_rollup_pkey PRIMARY KEY (provider_id, bucket_start);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'provider_settings_unique_key'
          AND conrelid = 'public.provider_settings'::regclass
    ) THEN
        ALTER TABLE ONLY public.provider_settings
            ADD CONSTRAINT provider_settings_unique_key UNIQUE (provider_id, setting_key);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'providers_pkey'
          AND conrelid = 'public.providers'::regclass
    ) THEN
        ALTER TABLE ONLY public.providers
            ADD CONSTRAINT providers_pkey PRIMARY KEY (id);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'providers_tenant_id_code_key'
          AND conrelid = 'public.providers'::regclass
    ) THEN
        ALTER TABLE ONLY public.providers
            ADD CONSTRAINT providers_tenant_id_code_key UNIQUE (tenant_id, code);
    END IF;
END $$;

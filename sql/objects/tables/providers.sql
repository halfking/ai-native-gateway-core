--
-- Name: providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.providers (
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
    CONSTRAINT providers_category_check CHECK ((category = ANY (ARRAY['official'::text, 'official_proxy'::text, 'third_party_relay'::text, 'aggregator'::text, 'self_host'::text]))),
    CONSTRAINT providers_kind_check CHECK ((kind = ANY (ARRAY['cloud'::text, 'local'::text]))),
    CONSTRAINT providers_quality_fix_mode_check CHECK ((quality_fix_mode = ANY (ARRAY['off'::text, 'detect_only'::text, 'fix'::text])))
);


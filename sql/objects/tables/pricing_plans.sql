--
-- Name: pricing_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pricing_plans (
    id bigint NOT NULL,
    scope text NOT NULL,
    provider_id bigint,
    credential_id bigint,
    tenant_id text,
    model_canonical_id bigint,
    plan_type text NOT NULL,
    currency text DEFAULT 'USD'::text NOT NULL,
    plan_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    effective_from timestamp with time zone DEFAULT now() NOT NULL,
    effective_to timestamp with time zone,
    source text DEFAULT 'manual'::text NOT NULL,
    confidence numeric(4,3) DEFAULT 1.000,
    scraped_url text,
    offer_scope_key text GENERATED ALWAYS AS (((((((((((scope || ':'::text) || COALESCE((provider_id)::text, '-'::text)) || ':'::text) || COALESCE((credential_id)::text, '-'::text)) || ':'::text) || COALESCE(tenant_id, '-'::text)) || ':'::text) || COALESCE((model_canonical_id)::text, '-'::text)) || ':'::text) || plan_type)) STORED,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pricing_plans_plan_type_check CHECK ((plan_type = ANY (ARRAY['token'::text, 'token_plan'::text, 'code_plan'::text, 'agent_plan'::text, 'request'::text, 'seat'::text, 'compute_time'::text, 'flat_quota'::text, 'free'::text]))),
    CONSTRAINT pricing_plans_scope_check CHECK ((scope = ANY (ARRAY['provider'::text, 'credential'::text, 'tenant'::text]))),
    CONSTRAINT pricing_plans_source_check CHECK ((source = ANY (ARRAY['manual'::text, 'seed'::text, 'litellm'::text, 'scraped'::text, 'catalog'::text])))
);


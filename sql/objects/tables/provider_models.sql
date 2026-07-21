--
-- Name: provider_models; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_models (
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


--
-- Name: TABLE provider_models; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_models IS 'Provider-exposed models: one row per (provider, raw_model_name)';


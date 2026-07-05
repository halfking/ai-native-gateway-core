--
-- Name: usage_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_ledger (
    id bigint NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    department text,
    employee text,
    "position" text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    raw_model_name text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean,
    error_kind text,
    route_reason text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    cost_currency text
);


--
-- Name: request_stats_dim_minute; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_stats_dim_minute (
    bucket timestamp with time zone NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    dim_type text NOT NULL,
    dim_key text NOT NULL,
    requests bigint DEFAULT 0 NOT NULL,
    success_count bigint DEFAULT 0 NOT NULL,
    failure_count bigint DEFAULT 0 NOT NULL,
    total_tokens bigint DEFAULT 0 NOT NULL,
    credits_charged bigint DEFAULT 0 NOT NULL,
    cost_usd numeric(18,8) DEFAULT 0 NOT NULL
);


--
-- Name: TABLE request_stats_dim_minute; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.request_stats_dim_minute IS 'Per-minute dimension breakdowns: client_profile, virtual_ip, identity_hash, model, error_kind, tenant, provider.';


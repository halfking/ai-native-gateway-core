--
-- Name: request_stats_minute; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_stats_minute (
    bucket timestamp with time zone NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    provider_id bigint DEFAULT 0 NOT NULL,
    canonical_id bigint DEFAULT 0 NOT NULL,
    requests bigint DEFAULT 0 NOT NULL,
    success_count bigint DEFAULT 0 NOT NULL,
    failure_count bigint DEFAULT 0 NOT NULL,
    prompt_tokens bigint DEFAULT 0 NOT NULL,
    completion_tokens bigint DEFAULT 0 NOT NULL,
    total_tokens bigint DEFAULT 0 NOT NULL,
    credits_charged bigint DEFAULT 0 NOT NULL,
    cost_usd numeric(18,8) DEFAULT 0 NOT NULL,
    latency_ms_sum bigint DEFAULT 0 NOT NULL
);


--
-- Name: TABLE request_stats_minute; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.request_stats_minute IS 'Per-minute usage aggregates for dashboard KPIs and trend charts. provider_id=0 and canonical_id=0 denote tenant-wide totals.';


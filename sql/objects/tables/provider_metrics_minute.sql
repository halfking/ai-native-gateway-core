--
-- Name: provider_metrics_minute; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_metrics_minute (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    model_name character varying(100),
    endpoint character varying(50),
    bucket timestamp with time zone NOT NULL,
    total_requests integer DEFAULT 0 NOT NULL,
    successful_requests integer DEFAULT 0 NOT NULL,
    error_5xx integer DEFAULT 0 NOT NULL,
    error_4xx integer DEFAULT 0 NOT NULL,
    error_timeout integer DEFAULT 0 NOT NULL,
    error_other integer DEFAULT 0 NOT NULL,
    latency_sum bigint DEFAULT 0 NOT NULL,
    latency_min integer,
    latency_max integer,
    latency_p50 integer,
    latency_p95 integer,
    latency_p99 integer,
    ttft_sum bigint DEFAULT 0,
    ttft_p95 integer,
    total_input_tokens bigint DEFAULT 0,
    total_output_tokens bigint DEFAULT 0,
    total_cost numeric(12,6) DEFAULT 0,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: TABLE provider_metrics_minute; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_metrics_minute IS '按分钟聚合的供应商指标 - 从request_logs实时聚合';


--
-- Name: provider_metrics_hour; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_metrics_hour (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    model_name character varying(100),
    endpoint character varying(50),
    bucket timestamp with time zone NOT NULL,
    total_requests bigint DEFAULT 0 NOT NULL,
    successful_requests bigint DEFAULT 0 NOT NULL,
    error_5xx integer DEFAULT 0 NOT NULL,
    error_4xx integer DEFAULT 0 NOT NULL,
    error_timeout integer DEFAULT 0 NOT NULL,
    success_rate numeric(5,2),
    error_rate_5xx numeric(5,2),
    latency_p50 integer,
    latency_p95 integer,
    latency_p99 integer,
    ttft_p95 integer,
    total_input_tokens bigint DEFAULT 0,
    total_output_tokens bigint DEFAULT 0,
    total_cost numeric(12,6) DEFAULT 0,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: TABLE provider_metrics_hour; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_metrics_hour IS '按小时聚合的供应商指标 - 从分钟级聚合，保留90天';


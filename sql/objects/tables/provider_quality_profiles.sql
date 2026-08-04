--
-- Name: provider_quality_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_quality_profiles (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    model_name character varying(100),
    success_rate_5m numeric(5,2),
    success_rate_1h numeric(5,2),
    success_rate_24h numeric(5,2),
    error_rate_5xx_5m numeric(5,2),
    error_rate_5xx_1h numeric(5,2),
    error_rate_5xx_24h numeric(5,2),
    error_rate_4xx_5m numeric(5,2),
    error_rate_4xx_1h numeric(5,2),
    error_rate_4xx_24h numeric(5,2),
    error_rate_timeout_5m numeric(5,2),
    error_rate_timeout_1h numeric(5,2),
    error_rate_timeout_24h numeric(5,2),
    availability_24h numeric(5,2),
    latency_p50_5m integer,
    latency_p50_1h integer,
    latency_p50_24h integer,
    latency_p95_5m integer,
    latency_p95_1h integer,
    latency_p95_24h integer,
    latency_p99_5m integer,
    latency_p99_1h integer,
    latency_p99_24h integer,
    ttft_p95_5m integer,
    ttft_p95_1h integer,
    ttft_p95_24h integer,
    throughput_tokens_per_sec_1h numeric(10,2),
    volatility_24h numeric(5,3),
    mttr_seconds_24h integer,
    error_diversity_score_24h numeric(5,2),
    consecutive_failures integer DEFAULT 0,
    last_failure_at timestamp with time zone,
    last_recovery_at timestamp with time zone,
    cost_per_1k_tokens numeric(10,6),
    quota_usage_percentage numeric(5,2),
    availability_score numeric(5,2),
    performance_score numeric(5,2),
    stability_score numeric(5,2),
    cost_efficiency_score numeric(5,2),
    quality_score numeric(5,2),
    quality_grade character varying(1),
    total_requests_5m bigint DEFAULT 0,
    total_requests_1h bigint DEFAULT 0,
    total_requests_24h bigint DEFAULT 0,
    successful_requests_5m bigint DEFAULT 0,
    successful_requests_1h bigint DEFAULT 0,
    successful_requests_24h bigint DEFAULT 0,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    reliability_score numeric(5,2) DEFAULT 0,
    calculated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);


--
-- Name: TABLE provider_quality_profiles; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_quality_profiles IS '供应商质量画像表';


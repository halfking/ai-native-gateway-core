--
-- Name: provider_profile_metrics; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_profile_metrics (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    provider_id bigint NOT NULL,
    metric_time timestamp with time zone NOT NULL,
    time_slot text NOT NULL,
    network_latency_p50 integer,
    network_latency_p95 integer,
    network_latency_p99 integer,
    availability_total_requests integer DEFAULT 0,
    availability_success_requests integer DEFAULT 0,
    availability_ttft_avg_ms integer,
    availability_duration_avg_ms integer,
    stability_error_count integer DEFAULT 0,
    stability_error_types jsonb,
    scale_total_models integer,
    scale_available_models integer,
    rate_limit_hits integer,
    rate_limit_total_requests integer,
    concurrency_limit integer,
    concurrency_limit_auto integer,
    concurrency_eff_limit integer,
    concurrency_is_capped boolean,
    downtime_buckets integer,
    downtime_total_buckets integer,
    longest_downtime_run integer,
    quality_stability_mean double precision,
    quality_stability_stddev double precision,
    quality_stability_cv double precision,
    quality_stability_is_volatile boolean,
    quality_stability_sample_n integer,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE provider_profile_metrics; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_profile_metrics IS '供应商画像小时级原始指标数据（保留7天）';


--
-- Name: provider_quality_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_quality_configs (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    alert_error_rate_5xx_p0 numeric(5,2) DEFAULT 5.0,
    alert_error_rate_5xx_p1 numeric(5,2) DEFAULT 1.0,
    alert_availability_p0 numeric(5,2) DEFAULT 95.0,
    alert_latency_p99_p0 integer DEFAULT 30000,
    alert_latency_p95_p1 integer DEFAULT 10000,
    target_success_rate numeric(5,2) DEFAULT 99.5,
    target_latency_p95 integer DEFAULT 5000,
    target_latency_p99 integer DEFAULT 10000,
    weight_availability numeric(3,2) DEFAULT 0.4,
    weight_performance numeric(3,2) DEFAULT 0.3,
    weight_stability numeric(3,2) DEFAULT 0.2,
    weight_cost_efficiency numeric(3,2) DEFAULT 0.1,
    circuit_breaker_enabled boolean DEFAULT true,
    circuit_breaker_threshold integer DEFAULT 5,
    circuit_breaker_timeout_seconds integer DEFAULT 300,
    downgrade_on_score_below numeric(5,2) DEFAULT 70.0,
    downgrade_weight_multiplier numeric(3,2) DEFAULT 0.5,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: TABLE provider_quality_configs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_quality_configs IS '供应商质量配置表 - 存储告警阈值和评分权重';


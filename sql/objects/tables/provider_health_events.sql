--
-- Name: provider_health_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_health_events (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    model_name character varying(100),
    event_type character varying(50) NOT NULL,
    severity character varying(10) NOT NULL,
    title character varying(200) NOT NULL,
    description text,
    trigger_metric character varying(50),
    trigger_value numeric(10,2),
    threshold_value numeric(10,2),
    affected_requests_count bigint,
    estimated_downtime_seconds integer,
    auto_action character varying(100),
    manual_action text,
    acknowledged_by character varying(100),
    acknowledged_at timestamp with time zone,
    resolved_at timestamp with time zone,
    notified boolean DEFAULT false,
    notification_channels text[],
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: TABLE provider_health_events; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_health_events IS '供应商健康事件日志 - 用于告警和审计追踪';


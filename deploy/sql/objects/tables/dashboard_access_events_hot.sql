--
-- Name: dashboard_access_events_hot; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dashboard_access_events_hot (
    event_id character varying(64) NOT NULL,
    event_type character varying(20) NOT NULL,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id character varying(255) NOT NULL,
    user_id character varying(255),
    user_role character varying(50),
    session_id character varying(128),
    api_path character varying(255) NOT NULL,
    api_method character varying(10) NOT NULL,
    api_version character varying(20),
    query_params jsonb,
    status_code integer NOT NULL,
    response_time_ms integer NOT NULL,
    cache_hit boolean DEFAULT false,
    data_size integer,
    error_code character varying(50),
    error_message text,
    client_ip inet,
    user_agent text,
    referer text,
    db_query_time_ms integer,
    cache_query_time_ms integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL
)
WITH (autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05', autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02', autovacuum_analyze_threshold='50');


--
-- Name: TABLE dashboard_access_events_hot; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.dashboard_access_events_hot IS 'Dashboard API 访问事件热表 - 记录所有 Dashboard 相关 API 的访问情况（保留 30 天）';


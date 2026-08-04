--
-- Name: dashboard_access_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dashboard_access_events (
    event_id character varying(64) NOT NULL,
    event_type character varying(20) NOT NULL,
    "timestamp" timestamp with time zone NOT NULL,
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
    created_at timestamp with time zone NOT NULL
)
PARTITION BY RANGE (created_at);


--
-- Name: TABLE dashboard_access_events; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.dashboard_access_events IS 'Dashboard API 访问事件归档表 - 按月分区，长期保留用于审计和分析';


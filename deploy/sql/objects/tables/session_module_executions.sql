--
-- Name: session_module_executions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_module_executions (
    execution_id bigint NOT NULL,
    gw_session_id character varying(128) NOT NULL,
    tenant_id character varying(255) NOT NULL,
    module_name character varying(100) NOT NULL,
    module_version character varying(20),
    request_id character varying(128),
    batch_key character varying(255) DEFAULT ''::character varying,
    status character varying(20) NOT NULL,
    started_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    duration_ms integer,
    result_summary jsonb,
    result_detail jsonb,
    error_message text,
    cache_key character varying(255) NOT NULL,
    ttl_seconds integer DEFAULT 3600 NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL
)
PARTITION BY RANGE (created_at);


--
-- Name: TABLE session_module_executions; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_module_executions IS '会话模块执行记录归档表 - 按月分区，保留历史数据供审计和长期分析';


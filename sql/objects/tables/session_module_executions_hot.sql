--
-- Name: session_module_executions_hot; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_module_executions_hot (
    execution_id bigint NOT NULL,
    gw_session_id character varying(128) NOT NULL,
    tenant_id character varying(255) NOT NULL,
    module_name character varying(100) NOT NULL,
    module_version character varying(20),
    request_id character varying(128),
    batch_key character varying(255) DEFAULT ''::character varying,
    status character varying(20) DEFAULT 'running'::character varying NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    duration_ms integer,
    result_summary jsonb,
    result_detail jsonb,
    error_message text,
    cache_key character varying(255) NOT NULL,
    ttl_seconds integer DEFAULT 3600 NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
)
WITH (autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05', autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02', autovacuum_analyze_threshold='50');


--
-- Name: TABLE session_module_executions_hot; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_module_executions_hot IS '会话模块执行记录热表 - 记录每个会话对每个模块的执行情况，避免重复执行（保留 7 天）';


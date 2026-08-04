--
-- Name: tool_usage_stats_hot; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_usage_stats_hot (
    id bigint DEFAULT nextval('public.tool_usage_stats_partitioned_id_seq'::regclass) NOT NULL,
    tool_id character varying NOT NULL,
    tenant_id character varying NOT NULL,
    usage_date date NOT NULL,
    call_count bigint DEFAULT 0,
    success_count bigint DEFAULT 0,
    error_count bigint DEFAULT 0,
    avg_latency_ms integer,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone
)
WITH (fillfactor='90', autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05', autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02', autovacuum_analyze_threshold='50');


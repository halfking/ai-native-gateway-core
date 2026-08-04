--
-- Name: model_probe_runs_hot; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_probe_runs_hot (
    id bigint,
    tenant_id text,
    credential_id bigint,
    raw_model_name text,
    status text,
    http_status integer,
    error_code text,
    error_message text,
    latency_ms integer,
    state_change text,
    state_applied boolean,
    triggered_by text,
    created_at timestamp with time zone DEFAULT now()
)
WITH (fillfactor='90', autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05', autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02', autovacuum_analyze_threshold='50');


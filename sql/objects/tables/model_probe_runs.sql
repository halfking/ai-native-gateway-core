--
-- Name: model_probe_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_probe_runs (
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
PARTITION BY RANGE (created_at);


--
-- Name: TABLE model_probe_runs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.model_probe_runs IS 'Per-(credential, model) probe attempts. Drives the providers-page "auto-test" panel and the model-discovery failed-count badge.';


SET default_table_access_method = columnar;

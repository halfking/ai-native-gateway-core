--
-- Name: credential_probe_model_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_probe_model_log (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    credential_id bigint NOT NULL,
    source text NOT NULL,
    old_model text,
    new_model text,
    actor text,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
)
WITH (autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05', autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02', autovacuum_analyze_threshold='50');


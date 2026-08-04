--
-- Name: handoff_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.handoff_logs (
    id integer NOT NULL,
    session_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    trigger_reason character varying(64) NOT NULL,
    tokens_at_handoff integer NOT NULL,
    context_window integer,
    handoff_prompt text,
    new_session_id character varying(64),
    created_at timestamp without time zone DEFAULT now(),
    summary_text text,
    summary_engine character varying(32),
    trigger_mode character varying(32),
    tokens_in_session integer,
    messages_in_session integer,
    skill_name character varying(64),
    duration_ms integer
)
WITH (autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05', autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02', autovacuum_analyze_threshold='50');


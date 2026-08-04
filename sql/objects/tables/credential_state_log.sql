--
-- Name: credential_state_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_state_log (
    credential_id integer NOT NULL,
    raw_model_name text NOT NULL,
    available boolean,
    health_status text,
    latency_ms integer,
    last_success_at timestamp with time zone,
    last_failure_at timestamp with time zone,
    last_error text,
    recover_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE credential_state_log; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_state_log IS 'Real-time per-(credential, model) state snapshots written by domains/credentialstate/batch_writer. Mirrors application-level StateUpdate struct. UPSERT by (credential_id, raw_model_name).';


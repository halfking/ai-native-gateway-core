--
-- Name: node_probe_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.node_probe_state (
    credential_id bigint NOT NULL,
    raw_model_name text NOT NULL,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    consecutive_successes integer DEFAULT 0 NOT NULL,
    last_attempt_at timestamp with time zone,
    next_retry_at timestamp with time zone DEFAULT now() NOT NULL,
    next_retry_seconds integer DEFAULT 5 NOT NULL,
    paused boolean DEFAULT false NOT NULL,
    last_run_id bigint,
    last_direct_ok boolean,
    last_gateway_ok boolean,
    last_err_code text,
    last_err_detail text,
    in_flight_until timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE node_probe_state; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.node_probe_state IS '341: per (credential, model) node-probe state machine. 7-step backoff ladder, paused after attempt=7 (24h cap).';


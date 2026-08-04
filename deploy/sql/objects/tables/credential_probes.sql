--
-- Name: credential_probes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_probes (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    provider_id bigint NOT NULL,
    probe_model text NOT NULL,
    success boolean NOT NULL,
    http_status integer,
    latency_ms integer,
    error_kind text,
    error_message text,
    response_preview text,
    triggered_by text DEFAULT 'scheduled'::text,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: model_integrity_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_integrity_events (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    request_id text,
    tenant_id text,
    application_id integer,
    api_key_id integer,
    provider_id integer,
    provider_code text,
    credential_id integer,
    client_model text,
    outbound_model text,
    raw_model_name text,
    anomaly_type text NOT NULL,
    severity text DEFAULT 'low'::text NOT NULL,
    expected_value text,
    actual_value text,
    sample text,
    context jsonb,
    resolved boolean DEFAULT false NOT NULL,
    resolved_at timestamp with time zone,
    resolution_notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


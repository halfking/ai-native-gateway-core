--
-- Name: response_format_anomalies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.response_format_anomalies (
    id bigint NOT NULL,
    detected_at timestamp with time zone DEFAULT now() NOT NULL,
    request_id text NOT NULL,
    provider_id integer,
    provider_code text,
    client_model text,
    outbound_model text,
    anomaly_type text NOT NULL,
    severity text DEFAULT 'medium'::text NOT NULL,
    usage_source text,
    expected_tokens integer,
    actual_tokens integer,
    content_size_bytes integer,
    response_structure jsonb,
    response_sample text,
    resolved boolean DEFAULT false NOT NULL,
    resolved_at timestamp with time zone,
    resolution_notes text,
    tenant_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


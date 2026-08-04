--
-- Name: fault_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fault_events (
    id bigint NOT NULL,
    rule_id bigint NOT NULL,
    rule_name text NOT NULL,
    severity text NOT NULL,
    title text NOT NULL,
    description text NOT NULL,
    source text NOT NULL,
    status text DEFAULT 'new'::text NOT NULL,
    metadata jsonb,
    detected_at timestamp with time zone NOT NULL,
    acked_at timestamp with time zone,
    acked_by text,
    resolved_at timestamp with time zone,
    resolved_by text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT fault_events_severity_check CHECK ((severity = ANY (ARRAY['info'::text, 'warning'::text, 'error'::text, 'critical'::text]))),
    CONSTRAINT fault_events_status_check CHECK ((status = ANY (ARRAY['new'::text, 'acknowledged'::text, 'resolving'::text, 'resolved'::text, 'ignored'::text])))
);


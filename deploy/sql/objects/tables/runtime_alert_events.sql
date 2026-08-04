--
-- Name: runtime_alert_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.runtime_alert_events (
    id bigint NOT NULL,
    rule_key text NOT NULL,
    instance_id text NOT NULL,
    severity text NOT NULL,
    title text NOT NULL,
    message text NOT NULL,
    status text DEFAULT 'triggered'::text NOT NULL,
    metric_value double precision,
    detected_at timestamp with time zone DEFAULT now() NOT NULL,
    acked_at timestamp with time zone,
    acked_by text,
    resolved_at timestamp with time zone,
    resolved_by text,
    suppressed_until timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT runtime_alert_events_severity_check CHECK ((severity = ANY (ARRAY['info'::text, 'warning'::text, 'error'::text, 'critical'::text]))),
    CONSTRAINT runtime_alert_events_status_check CHECK ((status = ANY (ARRAY['triggered'::text, 'acknowledged'::text, 'resolved'::text, 'suppressed'::text])))
);


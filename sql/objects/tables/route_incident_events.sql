--
-- Name: route_incident_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.route_incident_events (
    id bigint NOT NULL,
    incident_id uuid NOT NULL,
    event_type text NOT NULL,
    request_id text,
    terminal_status text,
    failure_kind text,
    failure_stage text,
    failure_streak integer,
    recovery_streak integer,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    actor text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT route_incident_events_event_type_check CHECK ((event_type = ANY (ARRAY['opened'::text, 'failure_observed'::text, 'recovery_progress'::text, 'recovered'::text, 'diagnostic_run'::text, 'operator_action'::text])))
);


--
-- Name: TABLE route_incident_events; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.route_incident_events IS 'Immutable, append-only evidence trail for route_incidents. Each (incident_id, request_id, terminal_status) triple is unique so the observer can retry safely.';


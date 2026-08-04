--
-- Name: analysis_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.analysis_events (
    id bigint NOT NULL,
    event_id text NOT NULL,
    type text NOT NULL,
    tenant_id text NOT NULL,
    session_id text,
    request_id text,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone,
    worker text,
    attempts integer DEFAULT 0 NOT NULL,
    last_error text,
    claimed_at timestamp with time zone,
    claimed_by text
);


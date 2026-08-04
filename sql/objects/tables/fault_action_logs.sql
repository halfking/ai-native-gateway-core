--
-- Name: fault_action_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fault_action_logs (
    id bigint NOT NULL,
    event_id bigint NOT NULL,
    action text NOT NULL,
    status text NOT NULL,
    result text,
    duration_ms bigint DEFAULT 0 NOT NULL,
    triggered_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone
);


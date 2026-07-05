--
-- Name: routing_audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_audit_log (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now(),
    actor text NOT NULL,
    action text NOT NULL,
    target_type text,
    target_id bigint,
    before_json jsonb,
    after_json jsonb
);


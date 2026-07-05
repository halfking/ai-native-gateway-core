--
-- Name: tool_call_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_call_events (
    id bigint NOT NULL,
    tool_id character varying(128) NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    request_id character varying(64),
    api_key character varying(64),
    status character varying(16) NOT NULL,
    latency_ms integer DEFAULT 0,
    error_code character varying(64),
    called_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_status CHECK (((status)::text = ANY (ARRAY[('success'::character varying)::text, ('error'::character varying)::text, ('timeout'::character varying)::text])))
);

ALTER TABLE ONLY public.tool_call_events FORCE ROW LEVEL SECURITY;


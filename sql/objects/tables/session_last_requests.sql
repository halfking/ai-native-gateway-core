--
-- Name: session_last_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_last_requests (
    session_id character varying(255) NOT NULL,
    last_request_id bigint NOT NULL,
    last_request_status character varying(50) NOT NULL,
    last_request_user_message text,
    last_response_cached text,
    last_response_chunks integer DEFAULT 0,
    last_model character varying(100),
    last_provider_id integer,
    last_latency_ms integer,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    expires_at timestamp with time zone DEFAULT (now() + '01:00:00'::interval)
);


--
-- Name: TABLE session_last_requests; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_last_requests IS '会话最后请求缓存表';


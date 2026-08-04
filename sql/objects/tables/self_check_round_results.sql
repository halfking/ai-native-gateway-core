--
-- Name: self_check_round_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.self_check_round_results (
    id bigint NOT NULL,
    run_id bigint NOT NULL,
    round_index integer NOT NULL,
    is_ping boolean DEFAULT false NOT NULL,
    is_tool_call boolean DEFAULT false NOT NULL,
    latency_ms integer DEFAULT 0 NOT NULL,
    prompt_tokens integer DEFAULT 0 NOT NULL,
    completion_tokens integer DEFAULT 0 NOT NULL,
    total_tokens integer DEFAULT 0 NOT NULL,
    success boolean DEFAULT false NOT NULL,
    http_code integer,
    error_message text,
    request_body text,
    response_preview text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT self_check_round_results_round_check CHECK (((round_index >= 0) AND (round_index <= 10)))
);


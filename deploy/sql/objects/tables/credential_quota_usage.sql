--
-- Name: credential_quota_usage; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_quota_usage (
    id bigint NOT NULL,
    quota_id bigint NOT NULL,
    window_started_at timestamp with time zone NOT NULL,
    window_ends_at timestamp with time zone NOT NULL,
    used_total_tokens bigint DEFAULT 0 NOT NULL,
    used_input_tokens bigint DEFAULT 0 NOT NULL,
    used_output_tokens bigint DEFAULT 0 NOT NULL,
    used_requests bigint DEFAULT 0 NOT NULL,
    used_cost_usd numeric(18,8) DEFAULT 0 NOT NULL,
    last_event_at timestamp with time zone,
    exhausted boolean DEFAULT false NOT NULL
);


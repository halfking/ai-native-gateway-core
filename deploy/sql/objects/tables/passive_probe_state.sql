--
-- Name: passive_probe_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.passive_probe_state (
    credential_id integer NOT NULL,
    raw_model_name text NOT NULL,
    error_kind text NOT NULL,
    consecutive_count integer DEFAULT 0 NOT NULL,
    total_recent_count integer DEFAULT 0 NOT NULL,
    window_total_count integer DEFAULT 0 NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    in_reviewing boolean DEFAULT false NOT NULL,
    reviewing_until timestamp with time zone,
    final_marked_at timestamp with time zone,
    unavailable_reason text,
    last_response_body_preview text
);


--
-- Name: TABLE passive_probe_state; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.passive_probe_state IS 'v5: Passive observation state for Layer 5. Accumulates consecutive errors from request_logs for the secondary-verification trigger (consecutive>=3 or error_rate>=0.6).';


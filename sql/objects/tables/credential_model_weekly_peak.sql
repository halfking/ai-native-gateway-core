--
-- Name: credential_model_weekly_peak; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_weekly_peak (
    week_start timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text DEFAULT ''::text NOT NULL,
    peak_concurrent integer DEFAULT 0 NOT NULL,
    peak_concurrent_5min integer DEFAULT 0 NOT NULL,
    p95_concurrent numeric(8,2) DEFAULT 0 NOT NULL,
    avg_concurrent numeric(8,2) DEFAULT 0 NOT NULL,
    total_requests bigint DEFAULT 0 NOT NULL,
    sample_days integer DEFAULT 0 NOT NULL,
    current_limit integer DEFAULT 0 NOT NULL,
    suggested_limit integer,
    suggestion_reason text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE credential_model_weekly_peak; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_weekly_peak IS 'Weekly aggregated peak concurrency for auto-tune suggestions';


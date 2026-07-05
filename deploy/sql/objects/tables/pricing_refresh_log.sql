--
-- Name: pricing_refresh_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pricing_refresh_log (
    id bigint NOT NULL,
    run_id text NOT NULL,
    run_ts timestamp with time zone DEFAULT now() NOT NULL,
    trigger text DEFAULT 'cron'::text NOT NULL,
    status text NOT NULL,
    before_summary jsonb NOT NULL,
    after_summary jsonb NOT NULL,
    diff_count integer DEFAULT 0 NOT NULL,
    new_offers integer DEFAULT 0 NOT NULL,
    removed_offers integer DEFAULT 0 NOT NULL,
    changed_offers integer DEFAULT 0 NOT NULL,
    artifacts_path text,
    feishu_sent boolean DEFAULT false NOT NULL,
    error_message text,
    duration_seconds integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE pricing_refresh_log; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.pricing_refresh_log IS 'Audit log for monthly pricing refresh cron job. Each run inserts one row.';


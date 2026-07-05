--
-- Name: credential_quotas; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_quotas (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    quota_name text NOT NULL,
    window_type text NOT NULL,
    starts_at timestamp with time zone,
    ends_at timestamp with time zone,
    period text,
    cron_expr text,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    reset_anchor_local time without time zone,
    rolling_seconds integer,
    cap_total_tokens bigint,
    cap_input_tokens bigint,
    cap_output_tokens bigint,
    cap_requests bigint,
    cap_cost_usd numeric(14,6),
    unlimited_in_window boolean DEFAULT false NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    priority integer DEFAULT 100 NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT credential_quotas_window_type_check CHECK ((window_type = ANY (ARRAY['fixed'::text, 'recurring'::text, 'rolling'::text])))
);


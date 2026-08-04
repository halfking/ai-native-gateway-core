--
-- Name: integrity_fingerprint_baseline; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.integrity_fingerprint_baseline (
    tenant_id text DEFAULT 'default'::text NOT NULL,
    provider_id integer,
    credential_id integer NOT NULL,
    raw_model_name text NOT NULL,
    baseline_fingerprint text,
    baseline_share_pct integer,
    baseline_sample_count bigint DEFAULT 0 NOT NULL,
    baseline_window_start timestamp with time zone,
    baseline_window_end timestamp with time zone,
    current_fingerprint text,
    current_share_pct integer,
    last_observed_at timestamp with time zone DEFAULT now() NOT NULL,
    last_alerted_fingerprint text,
    last_alerted_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


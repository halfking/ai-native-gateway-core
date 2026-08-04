--
-- Name: self_check_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.self_check_settings (
    id integer DEFAULT 1 NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    normal_interval_seconds integer DEFAULT 60 NOT NULL,
    fault_interval_seconds integer DEFAULT 30 NOT NULL,
    model_source text DEFAULT 'both'::text NOT NULL,
    max_models integer DEFAULT 10 NOT NULL,
    max_tokens_per_run integer DEFAULT 100000 NOT NULL,
    featured_model_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text,
    monitor_concurrency integer DEFAULT 5 NOT NULL,
    CONSTRAINT self_check_settings_id_check CHECK ((id = 1)),
    CONSTRAINT self_check_settings_interval_check CHECK (((normal_interval_seconds >= 10) AND (fault_interval_seconds >= 5))),
    CONSTRAINT self_check_settings_model_source_check CHECK ((model_source = ANY (ARRAY['top10'::text, 'featured'::text, 'both'::text]))),
    CONSTRAINT self_check_settings_monitor_concurrency_check CHECK (((monitor_concurrency >= 1) AND (monitor_concurrency <= 32)))
);


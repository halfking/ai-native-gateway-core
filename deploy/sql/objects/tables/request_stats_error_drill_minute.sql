--
-- Name: request_stats_error_drill_minute; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_stats_error_drill_minute (
    bucket timestamp with time zone NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    error_kind text NOT NULL,
    model_name text DEFAULT ''::text NOT NULL,
    provider_id bigint DEFAULT 0 NOT NULL,
    client_profile text DEFAULT ''::text NOT NULL,
    requests bigint DEFAULT 0 NOT NULL
);


--
-- Name: TABLE request_stats_error_drill_minute; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.request_stats_error_drill_minute IS 'Error drill-down aggregates for dashboard pie chart second level.';


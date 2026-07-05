--
-- Name: provider_quality_rollup; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_quality_rollup (
    provider_id integer NOT NULL,
    bucket_start timestamp with time zone NOT NULL,
    total_requests integer DEFAULT 0 NOT NULL,
    bad_requests integer DEFAULT 0 NOT NULL,
    fixed_requests integer DEFAULT 0 NOT NULL,
    avg_quality_score numeric(3,2),
    top_flag text
);


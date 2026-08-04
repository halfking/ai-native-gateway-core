--
-- Name: compression_bench_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.compression_bench_results (
    id bigint NOT NULL,
    row_id bigint,
    request_id text,
    tenant_id text,
    gw_session_id text,
    ts timestamp with time zone,
    bytes_before integer,
    tokens_before integer,
    msgs_before integer,
    bytes_after integer,
    tokens_after integer,
    msgs_after integer,
    bytes_ratio double precision,
    tokens_ratio double precision,
    msgs_ratio double precision,
    bytes_saved integer,
    tokens_saved integer,
    msgs_saved integer,
    strategy text,
    window_triggered text,
    summary_marker text,
    degraded boolean,
    lossiness text,
    protocol text,
    created_at timestamp with time zone DEFAULT now()
);


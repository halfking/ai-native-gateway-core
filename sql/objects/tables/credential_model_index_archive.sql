--
-- Name: credential_model_index_archive; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_index_archive (
    bucket timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text NOT NULL,
    canonical_id integer,
    billing_mode text,
    unit_price_in_per_1m numeric(10,4),
    unit_price_out_per_1m numeric(10,4),
    context_window integer,
    success_rate numeric(5,4),
    p95_latency_ms integer,
    active_sessions integer DEFAULT 0,
    concurrency_limit integer,
    pressure_ratio numeric(5,4),
    score_smart numeric(8,4),
    score_speed_first numeric(8,4),
    score_cost_first numeric(8,4),
    updated_at timestamp with time zone DEFAULT now()
)
PARTITION BY RANGE (bucket);


--
-- Name: TABLE credential_model_index_archive; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_index_archive IS 'Tiered storage: columnar partitions for historical credential_model_index (older than 7 days). Monthly partitions use Citus columnar (compressed, read-only). Main table keeps recent 7 days with ON CONFLICT support. Data flow: daily cleanup_old_credential_model_index() removes 7d+ data from main table after archival. Monthly archive_credential_model_index(month) migrates 7d+ data to columnar partitions. Query historical data via UNION ALL with main table.';


SET default_table_access_method = heap;

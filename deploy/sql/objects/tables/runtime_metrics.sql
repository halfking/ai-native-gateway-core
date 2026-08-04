--
-- Name: runtime_metrics; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.runtime_metrics (
    id bigint NOT NULL,
    instance_id text NOT NULL,
    license_id bigint,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    cpu_usage_pct real,
    mem_used_mb bigint,
    mem_total_mb bigint,
    disk_used_gb bigint,
    disk_total_gb bigint,
    db_size_mb bigint,
    uptime_secs bigint,
    current_concurrency integer,
    last_5min_tps real,
    last_5min_p50_ms real,
    last_5min_p99_ms real,
    last_5min_success_pct real,
    model_usage jsonb,
    tenant_count integer
);


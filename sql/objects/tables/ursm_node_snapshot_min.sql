--
-- Name: ursm_node_snapshot_min; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ursm_node_snapshot_min (
    snapshot_ts timestamp with time zone NOT NULL,
    recovery_epoch bigint NOT NULL,
    provider_id integer NOT NULL,
    credential_id integer NOT NULL,
    raw_model_name text NOT NULL,
    canonical_name text,
    tenant_id text,
    available boolean NOT NULL,
    health_status text,
    fail_streak integer,
    cool_until timestamp with time zone,
    sr_1m real,
    sr_5m real,
    sr_30m real,
    samples_1m integer,
    samples_5m integer,
    samples_30m integer,
    lat_p50_ms integer,
    lat_p95_ms integer,
    score real,
    price_in_per_1m numeric,
    price_out_per_1m numeric,
    billing_mode text,
    trust_level real,
    baseurl_latency_ms integer,
    conc_used integer,
    conc_limit integer,
    fp_used integer,
    fp_limit integer,
    source_priority integer,
    generation bigint,
    payload jsonb
);


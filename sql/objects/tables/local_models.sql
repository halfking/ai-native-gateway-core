--
-- Name: local_models; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.local_models (
    id bigint NOT NULL,
    runtime_id bigint NOT NULL,
    canonical_id bigint,
    raw_name text NOT NULL,
    quantization text,
    size_bytes bigint,
    family text,
    parameters_b numeric(8,2),
    loaded boolean DEFAULT false NOT NULL,
    keep_alive_seconds integer DEFAULT 0 NOT NULL,
    last_used_at timestamp with time zone
);


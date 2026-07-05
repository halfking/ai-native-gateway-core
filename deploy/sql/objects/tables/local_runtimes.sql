--
-- Name: local_runtimes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.local_runtimes (
    id bigint NOT NULL,
    host_code text NOT NULL,
    runtime_type text NOT NULL,
    base_url text NOT NULL,
    mode text DEFAULT 'direct'::text NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    gpu_info_json jsonb,
    vram_total_mb integer,
    vram_used_mb integer,
    ram_total_mb integer,
    last_heartbeat_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT local_runtimes_mode_check CHECK ((mode = ANY (ARRAY['direct'::text, 'agent'::text]))),
    CONSTRAINT local_runtimes_runtime_type_check CHECK ((runtime_type = ANY (ARRAY['ollama'::text, 'vllm'::text, 'llamacpp'::text, 'lmstudio'::text, 'mlx'::text]))),
    CONSTRAINT local_runtimes_status_check CHECK ((status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'degraded'::text, 'offline'::text])))
);


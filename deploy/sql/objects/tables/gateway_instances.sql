--
-- Name: gateway_instances; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.gateway_instances (
    instance_id text NOT NULL,
    hostname text NOT NULL,
    ip_address text NOT NULL,
    region text,
    version text NOT NULL,
    build_seq integer NOT NULL,
    status text DEFAULT 'online'::text NOT NULL,
    started_at timestamp with time zone NOT NULL,
    last_heartbeat timestamp with time zone DEFAULT now() NOT NULL,
    registered_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    instance_token text,
    refresh_token text,
    refresh_token_issued_at timestamp with time zone,
    refresh_token_expires_at timestamp with time zone,
    public_key text,
    current_version text,
    license_key_hash text,
    hardware_hash text,
    instance_type text DEFAULT 'standalone'::text,
    deployment_id text,
    replica_count integer DEFAULT 1,
    CONSTRAINT gateway_instances_status_check CHECK ((status = ANY (ARRAY['online'::text, 'offline'::text, 'degraded'::text])))
);


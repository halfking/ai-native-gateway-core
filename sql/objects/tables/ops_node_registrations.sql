--
-- Name: ops_node_registrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ops_node_registrations (
    id bigint NOT NULL,
    instance_id text NOT NULL,
    region text NOT NULL,
    license_key text NOT NULL,
    license_id bigint,
    admin_user text NOT NULL,
    admin_email text,
    hostname text,
    ip_address text,
    version text,
    build_seq integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    registered_at timestamp with time zone DEFAULT now() NOT NULL,
    last_heartbeat timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT ops_node_registrations_status_check CHECK ((status = ANY (ARRAY['active'::text, 'revoked'::text])))
);


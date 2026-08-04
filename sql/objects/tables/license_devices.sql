--
-- Name: license_devices; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.license_devices (
    id bigint NOT NULL,
    license_id bigint NOT NULL,
    instance_id text NOT NULL,
    hardware_hash text NOT NULL,
    device_name text NOT NULL,
    activated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_heartbeat timestamp with time zone,
    status text DEFAULT 'active'::text NOT NULL,
    deactivated_at timestamp with time zone,
    deactivate_reason text,
    CONSTRAINT license_devices_status_check CHECK ((status = ANY (ARRAY['active'::text, 'deactivated'::text])))
);


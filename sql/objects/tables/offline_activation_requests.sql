--
-- Name: offline_activation_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.offline_activation_requests (
    id bigint NOT NULL,
    license_key text NOT NULL,
    hardware_hash text NOT NULL,
    instance_id text NOT NULL,
    device_name text NOT NULL,
    request_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    approved_at timestamp with time zone,
    signed_license jsonb
);


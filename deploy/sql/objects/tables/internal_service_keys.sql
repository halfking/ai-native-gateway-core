--
-- Name: internal_service_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.internal_service_keys (
    service_id text NOT NULL,
    secret_hash text NOT NULL,
    description text,
    enabled boolean DEFAULT true NOT NULL,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    rotated_at timestamp with time zone,
    rotation_notes text
);


--
-- Name: TABLE internal_service_keys; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.internal_service_keys IS 'Registry of HMAC secrets for internal service-to-service authentication.
     The actual secret is stored in INTERNAL_SERVICE_KEYS_JSON env var (not here).
     This table tracks registration metadata and last-used timestamps for audit.';


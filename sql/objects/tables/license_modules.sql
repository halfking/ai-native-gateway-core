--
-- Name: license_modules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.license_modules (
    id bigint NOT NULL,
    license_id bigint NOT NULL,
    module_key text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    config jsonb,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


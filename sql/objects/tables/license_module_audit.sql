--
-- Name: license_module_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.license_module_audit (
    id bigint NOT NULL,
    license_key text NOT NULL,
    module_key text NOT NULL,
    action text NOT NULL,
    old_value jsonb,
    new_value jsonb,
    actor text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


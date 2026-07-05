--
-- Name: applications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.applications (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    code text NOT NULL,
    display_name text NOT NULL,
    owner_user text,
    data_sensitivity text DEFAULT 'internal'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    default_client_profile text,
    allowed_models_json jsonb,
    CONSTRAINT applications_data_sensitivity_check CHECK ((data_sensitivity = ANY (ARRAY['public'::text, 'internal'::text, 'confidential'::text])))
);


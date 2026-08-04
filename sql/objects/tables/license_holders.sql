--
-- Name: license_holders; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.license_holders (
    id bigint NOT NULL,
    email text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    holder_type text DEFAULT 'individual'::text NOT NULL,
    consent_version text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone,
    CONSTRAINT license_holders_holder_type_check CHECK ((holder_type = ANY (ARRAY['individual'::text, 'organization'::text])))
);


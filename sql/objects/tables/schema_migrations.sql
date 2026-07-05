--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version text NOT NULL,
    description text,
    applied_at timestamp with time zone DEFAULT now()
);


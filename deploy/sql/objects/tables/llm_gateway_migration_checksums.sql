--
-- Name: llm_gateway_migration_checksums; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.llm_gateway_migration_checksums (
    version text NOT NULL,
    migration_name text NOT NULL,
    checksum text NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL
);


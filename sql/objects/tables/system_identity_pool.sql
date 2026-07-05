--
-- Name: system_identity_pool; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.system_identity_pool (
    id integer DEFAULT 1 NOT NULL,
    max_identities integer DEFAULT 10000 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text,
    CONSTRAINT system_identity_pool_id_check CHECK ((id = 1))
);


--
-- Name: TABLE system_identity_pool; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.system_identity_pool IS 'Global cap on total distinct end-user identities the gateway will accept. Once this many unique fingerprints are active, new connections must reuse an existing fingerprint (round-robin among least-recently-used).';


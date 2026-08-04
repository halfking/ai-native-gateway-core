--
-- Name: request_wal_bodies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal_bodies (
    request_id character varying(64) NOT NULL,
    outbound_body text,
    compression_meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE request_wal_bodies; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.request_wal_bodies IS 'Large outbound bodies separated for performance';


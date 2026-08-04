--
-- Name: request_wal_archive; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal_archive (
    request_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    gw_session_id character varying(128),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model character varying(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    upstream_request_at timestamp with time zone,
    upstream_response_at timestamp with time zone,
    error text,
    compression_strategy character varying(50),
    compression_meta jsonb
)
PARTITION BY RANGE (created_at);


--
-- Name: TABLE request_wal_archive; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.request_wal_archive IS 'Columnar archive for old request_wal partitions (2+ months old).';


SET default_table_access_method = heap;

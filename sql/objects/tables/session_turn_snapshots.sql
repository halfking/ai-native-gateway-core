--
-- Name: session_turn_snapshots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_turn_snapshots (
    id bigint NOT NULL,
    tenant_id character varying(255) NOT NULL,
    gw_session_id character varying(128) NOT NULL,
    turn_no integer NOT NULL,
    request_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    original_send jsonb,
    original_receive jsonb,
    compressed_send jsonb,
    compressed_receive jsonb,
    secured_send jsonb,
    secured_receive jsonb,
    original_send_ref character varying(64),
    original_receive_ref character varying(64),
    compressed_send_ref character varying(64),
    compressed_receive_ref character varying(64),
    secured_send_ref character varying(64),
    secured_receive_ref character varying(64),
    compression_strategy text,
    compression_meta jsonb DEFAULT '{}'::jsonb NOT NULL,
    security_tags text[] DEFAULT '{}'::text[] NOT NULL,
    compressed_range_start integer,
    compressed_range_end integer,
    summary_marker text,
    token_original integer DEFAULT 0 NOT NULL,
    token_compressed integer DEFAULT 0 NOT NULL,
    token_secured integer DEFAULT 0 NOT NULL,
    stream_completed boolean DEFAULT true NOT NULL
);


--
-- Name: TABLE session_turn_snapshots; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_turn_snapshots IS 'TTL-bound, turn-aligned original/compressed/secured conversation snapshots for admin audit.';


--
-- Name: session_bodies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_bodies (
    id bigint NOT NULL,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id text NOT NULL,
    request_delta jsonb,
    response_delta jsonb,
    outbound_body jsonb,
    request_attachments jsonb DEFAULT '[]'::jsonb,
    response_attachments jsonb DEFAULT '[]'::jsonb,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    partition_date date DEFAULT CURRENT_DATE NOT NULL
)
PARTITION BY RANGE (partition_date);


--
-- Name: TABLE session_bodies; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_bodies IS 'V2正文存储表：存储增量正文，避免request_logs的全量JSONB膨胀问题。
     使用columnar存储格式，配合zstd压缩，预计可节省60-80%磁盘空间。
     Created: 2026-07-17, Migration 430';


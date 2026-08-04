--
-- Name: attachments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.attachments (
    id text NOT NULL,
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    attachment_type text NOT NULL,
    media_type text NOT NULL,
    file_size bigint NOT NULL,
    file_path text NOT NULL,
    original_data_type text NOT NULL,
    original_url text,
    content_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb
);


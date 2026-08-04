--
-- Name: download_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.download_events (
    id bigint NOT NULL,
    request_id text NOT NULL,
    release_version text NOT NULL,
    platform text NOT NULL,
    arch text DEFAULT ''::text NOT NULL,
    edition text DEFAULT 'customer'::text NOT NULL,
    channel text DEFAULT 'stable'::text NOT NULL,
    holder_id bigint,
    donation_id bigint,
    result text DEFAULT 'started'::text NOT NULL,
    duration_ms integer,
    source text DEFAULT 'web'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT download_events_result_check CHECK ((result = ANY (ARRAY['started'::text, 'completed'::text, 'failed'::text])))
);


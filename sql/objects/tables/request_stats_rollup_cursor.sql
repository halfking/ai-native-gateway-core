--
-- Name: request_stats_rollup_cursor; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_stats_rollup_cursor (
    id smallint DEFAULT 1 NOT NULL,
    last_ts timestamp with time zone,
    last_request_id text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT request_stats_rollup_cursor_id_check CHECK ((id = 1))
);


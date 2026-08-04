--
-- Name: download_publish_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.download_publish_runs (
    id bigint NOT NULL,
    release_version text NOT NULL,
    build_seq integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    artifact_count integer DEFAULT 0 NOT NULL,
    test_passed boolean DEFAULT false NOT NULL,
    log_summary text,
    created_by text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone
);


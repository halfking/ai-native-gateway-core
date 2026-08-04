--
-- Name: release_artifacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.release_artifacts (
    id bigint NOT NULL,
    release_version text NOT NULL,
    platform text NOT NULL,
    arch text DEFAULT ''::text NOT NULL,
    edition text DEFAULT 'customer'::text NOT NULL,
    artifact_name text NOT NULL,
    sha256 text DEFAULT ''::text NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    download_path text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


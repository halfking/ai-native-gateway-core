--
-- Name: instance_release_status; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.instance_release_status (
    release_id bigint NOT NULL,
    instance_id text NOT NULL,
    status text NOT NULL,
    version text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    error text,
    retry_count integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


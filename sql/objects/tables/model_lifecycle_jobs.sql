--
-- Name: model_lifecycle_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_lifecycle_jobs (
    id bigint NOT NULL,
    runtime_id bigint NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    progress_pct numeric(5,2) DEFAULT 0,
    log text,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_lifecycle_jobs_action_check CHECK ((action = ANY (ARRAY['pull'::text, 'rm'::text, 'load'::text, 'unload'::text, 'keepalive'::text]))),
    CONSTRAINT model_lifecycle_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'running'::text, 'success'::text, 'failed'::text, 'canceled'::text])))
);


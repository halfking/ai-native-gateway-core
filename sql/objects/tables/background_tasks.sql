--
-- Name: background_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.background_tasks (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    task_type text NOT NULL,
    provider_id bigint,
    credential_id bigint,
    status text DEFAULT 'running'::text NOT NULL,
    request_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    result_json jsonb,
    error text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone
);


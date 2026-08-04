--
-- Name: background_tasks_duplicates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.background_tasks_duplicates (
    id bigint NOT NULL,
    tenant_id text NOT NULL,
    task_type text NOT NULL,
    provider_id bigint,
    credential_id bigint,
    status text NOT NULL,
    request_json jsonb NOT NULL,
    result_json jsonb,
    error text,
    started_at timestamp with time zone NOT NULL,
    finished_at timestamp with time zone,
    removed_at timestamp with time zone DEFAULT now()
);


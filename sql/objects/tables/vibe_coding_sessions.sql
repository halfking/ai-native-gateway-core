--
-- Name: vibe_coding_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.vibe_coding_sessions (
    id bigint NOT NULL,
    project_id bigint,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    session_id text NOT NULL,
    task_type text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    messages jsonb DEFAULT '[]'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT vibe_coding_sessions_status_check CHECK ((status = ANY (ARRAY['active'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);


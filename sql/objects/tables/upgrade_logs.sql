--
-- Name: upgrade_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.upgrade_logs (
    id bigint NOT NULL,
    instance_id text NOT NULL,
    old_version text NOT NULL,
    new_version text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    error_message text,
    retry_count integer DEFAULT 0 NOT NULL,
    duration_ms integer,
    CONSTRAINT upgrade_logs_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'downloading'::text, 'ready_to_restart'::text, 'upgrading'::text, 'success'::text, 'failed'::text, 'rolled_back'::text])))
);


--
-- Name: approval_queue; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.approval_queue (
    id uuid NOT NULL,
    session_id text NOT NULL,
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    detect_result jsonb NOT NULL,
    snapshot jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    approved_by text,
    approved_at timestamp with time zone,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    resume_state text DEFAULT 'idle'::text NOT NULL,
    resume_owner text,
    resume_lease_until timestamp with time zone,
    resume_fencing_token bigint DEFAULT 0 NOT NULL,
    resume_started_at timestamp with time zone,
    resume_completed_at timestamp with time zone,
    resume_error text,
    CONSTRAINT approval_queue_status_chk CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'timeout'::text]))),
    CONSTRAINT approval_queue_resume_state_chk CHECK ((resume_state = ANY (ARRAY['idle'::text, 'running'::text, 'completed'::text, 'failed'::text]))),
    CONSTRAINT approval_queue_resume_fencing_token_chk CHECK ((resume_fencing_token >= 0))
);

ALTER TABLE ONLY public.approval_queue FORCE ROW LEVEL SECURITY;


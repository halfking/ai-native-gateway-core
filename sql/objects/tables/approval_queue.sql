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
    CONSTRAINT approval_queue_status_chk CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'timeout'::text])))
);

ALTER TABLE ONLY public.approval_queue FORCE ROW LEVEL SECURITY;


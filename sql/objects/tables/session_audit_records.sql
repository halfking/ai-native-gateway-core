--
-- Name: session_audit_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_audit_records (
    id bigint NOT NULL,
    session_id text NOT NULL,
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    client_ip text,
    client_user_agent text,
    client_model text,
    content_summary text,
    content_title text,
    content_hash text,
    intent_type text,
    intent_score double precision,
    intent_reason text,
    security_score integer,
    danger_score integer,
    trust_score integer,
    sensitive_score integer,
    detect_score integer DEFAULT 0 NOT NULL,
    detect_decision text DEFAULT 'pass'::text NOT NULL,
    threats jsonb DEFAULT '[]'::jsonb NOT NULL,
    sensitive_words jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'pass'::text NOT NULL,
    approval_status text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.session_audit_records FORCE ROW LEVEL SECURITY;


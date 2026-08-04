--
-- Name: approval_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.approval_requests (
    id integer NOT NULL,
    request_id character varying(64) NOT NULL,
    session_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    trigger_type character varying(32) NOT NULL,
    trigger_reason text,
    risk_level character varying(16) NOT NULL,
    session_summary jsonb,
    sensitive_info jsonb,
    user_message text,
    full_context jsonb,
    estimated_cost numeric(10,4),
    estimated_tokens integer,
    status character varying(32) DEFAULT 'pending'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    approved_by character varying(64),
    approved_at timestamp with time zone,
    approval_note text,
    rejected boolean DEFAULT false,
    rejection_reason text,
    metadata jsonb DEFAULT '{}'::jsonb,
    CONSTRAINT approval_requests_risk_level_check CHECK (((risk_level)::text = ANY (ARRAY[('LOW'::character varying)::text, ('MEDIUM'::character varying)::text, ('HIGH'::character varying)::text, ('CRITICAL'::character varying)::text]))),
    CONSTRAINT approval_requests_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('approved'::character varying)::text, ('rejected'::character varying)::text, ('timeout'::character varying)::text, ('canceled'::character varying)::text]))),
    CONSTRAINT approval_requests_trigger_type_check CHECK (((trigger_type)::text = ANY (ARRAY[('sensitive_content'::character varying)::text, ('high_cost'::character varying)::text, ('tool_call'::character varying)::text, ('policy_match'::character varying)::text, ('manual_mode'::character varying)::text])))
);


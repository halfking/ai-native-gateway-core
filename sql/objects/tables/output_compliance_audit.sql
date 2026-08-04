--
-- Name: output_compliance_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.output_compliance_audit (
    id bigint NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id character varying(255) NOT NULL,
    session_key character varying(255),
    detected_at timestamp with time zone DEFAULT now(),
    issue_type character varying(50) NOT NULL,
    issue_subtype character varying(50),
    severity integer NOT NULL,
    evidence text,
    location character varying(100),
    score numeric(5,4),
    action_taken character varying(20) NOT NULL,
    redacted boolean DEFAULT false,
    blocked boolean DEFAULT false,
    original_output text,
    redacted_output text,
    model character varying(100),
    client_ip character varying(45),
    policy_id integer,
    rule_triggered character varying(100),
    exception_matched boolean DEFAULT false,
    exception_scope character varying(50),
    alert_sent boolean DEFAULT false,
    skill_suggestion text,
    review_queue_id integer,
    CONSTRAINT output_compliance_audit_severity_check CHECK (((severity >= 1) AND (severity <= 10)))
);


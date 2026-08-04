--
-- Name: output_compliance_review_queue; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.output_compliance_review_queue (
    id integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    audit_id bigint NOT NULL,
    request_id character varying(255) NOT NULL,
    session_key character varying(255),
    issue_type character varying(50) NOT NULL,
    issue_subtype character varying(50),
    severity integer NOT NULL,
    status character varying(20) DEFAULT 'pending'::character varying,
    reviewer character varying(255),
    review_comment text,
    created_at timestamp with time zone DEFAULT now(),
    reviewed_at timestamp with time zone,
    CONSTRAINT output_compliance_review_queue_status_check CHECK (((status)::text = ANY ((ARRAY['pending'::character varying, 'approved'::character varying, 'rejected'::character varying])::text[])))
);


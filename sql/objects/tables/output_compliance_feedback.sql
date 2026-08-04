--
-- Name: output_compliance_feedback; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.output_compliance_feedback (
    id bigint NOT NULL,
    tenant_id character varying(255) NOT NULL,
    audit_id bigint NOT NULL,
    feedback_type character varying(20) NOT NULL,
    reporter character varying(255),
    comment text,
    created_at timestamp with time zone DEFAULT now(),
    CONSTRAINT output_compliance_feedback_feedback_type_check CHECK (((feedback_type)::text = ANY ((ARRAY['false_positive'::character varying, 'false_negative'::character varying, 'correct'::character varying])::text[])))
);


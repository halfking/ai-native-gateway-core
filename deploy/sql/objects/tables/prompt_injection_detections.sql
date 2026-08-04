--
-- Name: prompt_injection_detections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.prompt_injection_detections (
    id bigint NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id character varying(255) NOT NULL,
    session_key character varying(255),
    detected_at timestamp with time zone DEFAULT now(),
    risk_level integer NOT NULL,
    rule_id integer,
    rule_name character varying(100),
    category character varying(50),
    matched_pattern text,
    input_sample text,
    blocked boolean DEFAULT false,
    action_taken character varying(20) NOT NULL,
    evidence_text text,
    input_hash character varying(64),
    client_ip character varying(45),
    user_agent text,
    llm_engine_id integer,
    llm_confidence double precision,
    llm_reason text,
    categories public.injection_category[],
    canary_token_leaked character varying(255),
    similar_attack_id bigint,
    approval_id character varying(100),
    replaced_content text,
    original_content_hash character varying(64),
    CONSTRAINT prompt_injection_detections_risk_level_check CHECK (((risk_level >= 1) AND (risk_level <= 10)))
);


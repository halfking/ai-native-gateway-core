--
-- Name: goal_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.goal_sessions (
    id integer NOT NULL,
    session_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    state character varying(32) DEFAULT 'active'::character varying NOT NULL,
    original_goal text NOT NULL,
    retry_count integer DEFAULT 0,
    decision_count integer DEFAULT 0,
    auto_continue_count integer DEFAULT 0,
    last_activity_at timestamp without time zone DEFAULT now(),
    completed_at timestamp without time zone,
    audit_result jsonb,
    created_at timestamp without time zone DEFAULT now(),
    model_switch_count integer DEFAULT 0 NOT NULL,
    repeat_count integer DEFAULT 0 NOT NULL,
    last_response_hash character varying(64) DEFAULT ''::character varying,
    current_model character varying(128) DEFAULT ''::character varying
);


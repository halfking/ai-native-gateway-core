--
-- Name: request_context_attrs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_context_attrs (
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    identity_hash character varying(64),
    virtual_client_id character varying(32),
    virtual_ip inet,
    virtual_mac character varying(17),
    agent_name character varying(255),
    agent_type character varying(50),
    client_ip inet,
    client_forwarded_for text,
    api_key_fingerprint character varying(16),
    api_key_id bigint,
    application_id bigint,
    application_code text,
    owner_user text,
    end_user_id text,
    customer_id bigint,
    client_protocol character varying(50),
    is_retry boolean DEFAULT false NOT NULL,
    attempt_no integer,
    is_probe boolean DEFAULT false NOT NULL,
    origin_stage character varying(32),
    turn_no integer,
    source_channel character varying(32),
    client_request_id text,
    project_id text,
    session_title text,
    session_summary text,
    task_id text,
    fingerprint_raw jsonb
);


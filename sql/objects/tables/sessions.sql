--
-- Name: sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sessions (
    id bigint NOT NULL,
    session_id text NOT NULL,
    tenant_id character varying(255) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    closed_at timestamp with time zone,
    status text DEFAULT 'active'::text NOT NULL,
    total_turns integer DEFAULT 0 NOT NULL,
    total_tokens integer DEFAULT 0 NOT NULL,
    total_cost_usd numeric(12,6) DEFAULT 0 NOT NULL,
    last_turn_no integer,
    last_request_summary text,
    last_response_summary text,
    last_model text,
    last_provider text,
    task_type text,
    client_type text,
    topic text,
    intent text,
    primary_request_id text,
    turn_logs_summary jsonb,
    partition_date date DEFAULT CURRENT_DATE NOT NULL,
    CONSTRAINT sessions_status_check CHECK ((status = ANY (ARRAY['active'::text, 'closed'::text, 'archived'::text, 'deleted'::text])))
)
PARTITION BY RANGE (partition_date);


--
-- Name: TABLE sessions; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.sessions IS 'V2会话快照表：一个会话一条记录，存储会话级汇总信息。
     与request_logs并行运行，通过Feature Flag控制流量路由。
     通过primary_request_id可以关联到request_logs进行数据校验。
     Created: 2026-07-17, Migration 430';


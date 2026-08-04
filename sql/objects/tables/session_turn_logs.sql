--
-- Name: session_turn_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_turn_logs (
    id bigint NOT NULL,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id text NOT NULL,
    stage text NOT NULL,
    stage_status text NOT NULL,
    event_data jsonb DEFAULT '{}'::jsonb NOT NULL,
    error_message text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    latency_ms integer,
    expires_at timestamp with time zone DEFAULT (now() + '24:00:00'::interval) NOT NULL,
    CONSTRAINT session_turn_logs_stage_check CHECK ((stage = ANY (ARRAY['routing'::text, 'compression'::text, 'injection_check'::text, 'llm_call'::text, 'output_check'::text, 'response'::text, 'cache_update'::text]))),
    CONSTRAINT session_turn_logs_stage_status_check CHECK ((stage_status = ANY (ARRAY['pending'::text, 'running'::text, 'success'::text, 'failed'::text, 'skipped'::text])))
);


--
-- Name: TABLE session_turn_logs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_turn_logs IS 'V2环节状态日志：记录每个轮次的处理过程，用于故障诊断。
     24小时后自动清理，会话结束时汇总生成JSON存入sessions表。
     Created: 2026-07-17, Migration 430';


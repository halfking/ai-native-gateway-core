--
-- Name: session_bodies_trash; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/912_session_recording_trash.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-28 补录（分支审计轮）：wip/02 合入时漏带正典条目，
--   导致「按正典文件读会话族」的人读出一张不存在的表。

CREATE TABLE public.session_bodies_trash (
    tenant_id text NOT NULL,
    session_id text NOT NULL,
    turn_no integer DEFAULT 0 NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    request_delta jsonb,
    response_delta jsonb,
    outbound_body jsonb,
    request_attachments jsonb,
    response_attachments jsonb,
    kind text DEFAULT 'turn_delta'::text NOT NULL,
    partition_date date DEFAULT CURRENT_DATE NOT NULL,
    trashed_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (tenant_id, session_id, request_id, partition_date)
);

CREATE INDEX idx_session_bodies_trash_trashed_at ON public.session_bodies_trash USING btree (trashed_at);
CREATE INDEX idx_session_bodies_trash_session ON public.session_bodies_trash USING btree (tenant_id, session_id);

--
-- Name: TABLE session_bodies_trash; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_bodies_trash IS 'W1.5 recycle bin for session_bodies rows. Usage ledgers never enter here. Hourly cleanup erases rows past session_body_retention_hours. Created: 2026-10-28, Migration 912';

ALTER TABLE public.session_bodies_trash ENABLE ROW LEVEL SECURITY;

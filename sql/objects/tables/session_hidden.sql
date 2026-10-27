--
-- Name: session_hidden; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/912_session_recording_trash.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-28 补录（分支审计轮）：wip/02 合入时漏带正典条目，
--   导致「按正典文件读会话族」的人读出一张不存在的表。

CREATE TABLE public.session_hidden (
    tenant_id text NOT NULL,
    session_id text NOT NULL,
    hidden_until timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    reason text DEFAULT 'trash'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (tenant_id, session_id)
);

--
-- Name: TABLE session_hidden; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_hidden IS 'W1.5: sessions hidden from list after trash/purge (magpie gateway-sessions-hidden). Permanent purge keeps the row so the session stays out of lists. Created: 2026-10-28, Migration 912';

ALTER TABLE public.session_hidden ENABLE ROW LEVEL SECURITY;

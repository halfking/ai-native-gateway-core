--
-- Name: session_bodies_hot; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/614_session_bodies_hot.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-20 补录（审计 D3c）：本表此前在正典目录里没有任何条目，
--   导致「按正典文件读会话族」的人读出一张不存在的表。

CREATE TABLE public.session_bodies_hot (
    id bigint NOT NULL DEFAULT nextval('public.session_bodies_id_seq'::regclass),
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id text NOT NULL,
    request_delta jsonb,
    response_delta jsonb,
    outbound_body jsonb,
    request_attachments jsonb DEFAULT '[]'::jsonb,
    response_attachments jsonb DEFAULT '[]'::jsonb,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    partition_date date DEFAULT CURRENT_DATE NOT NULL
) WITH (fillfactor=90);


--
-- Name: TABLE session_bodies_hot; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_bodies_hot IS 'Hot table for session_bodies: 8-hour retention window before promotion to monthly partitions. All writes go here first.';

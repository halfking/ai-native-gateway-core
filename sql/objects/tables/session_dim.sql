--
-- Name: session_dim; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/805_session_dim_reconcile.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-20 补录（审计 D3c）：本表此前在正典目录里没有任何条目，
--   导致「按正典文件读会话族」的人读出一张不存在的表。

CREATE TABLE public.session_dim (
    gw_session_id      character varying NOT NULL,
    session_key        character varying NOT NULL,
    tenant_id          character varying NOT NULL,
    task_id            character varying,
    status             character varying NOT NULL,
    first_request_at   timestamp with time zone,
    last_active_at     timestamp with time zone,
    closed_at          timestamp with time zone,
    created_at         timestamp with time zone NOT NULL,
    project_id         text,
    api_key_id         bigint,
    application_id     bigint,
    application_code   character varying,
    owner_user         character varying,
    api_key_owner_user character varying,
    end_user_id        character varying,
    client_id          character varying,
    CONSTRAINT session_dim_pkey PRIMARY KEY (gw_session_id)
);

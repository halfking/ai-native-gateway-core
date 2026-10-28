--
-- Name: session_bodies_trash; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/912_session_recording_trash.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-28 补录（r1041 轮）：分支 02 引入的迁移 912 创建了本表，
--   但正典目录里没有条目 ⇒ 门 TestCanonicalObjectsCoverEveryMigratedSessionTable 判红
--   ⇒ 「按正典文件读会话族」的人会读出一张不存在的表。

CREATE TABLE public.session_bodies_trash (
    tenant_id              TEXT NOT NULL,
    session_id             TEXT NOT NULL,
    turn_no                INT  NOT NULL DEFAULT 0,
    request_id             TEXT NOT NULL,
    ts                     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    request_delta          JSONB,
    response_delta         JSONB,
    outbound_body          JSONB,
    request_attachments    JSONB,
    response_attachments   JSONB,
    kind                   TEXT NOT NULL DEFAULT 'turn_delta',
    partition_date         DATE NOT NULL DEFAULT CURRENT_DATE,
    trashed_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, session_id, request_id, partition_date)
);

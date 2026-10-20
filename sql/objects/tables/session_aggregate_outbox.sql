--
-- Name: session_aggregate_outbox; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/630_session_aggregate_outbox.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-20 补录（审计 D3c）：本表此前在正典目录里没有任何条目，
--   导致「按正典文件读会话族」的人读出一张不存在的表。

CREATE TABLE public.session_aggregate_outbox (
    id              bigserial PRIMARY KEY,
    tenant_id       text        NOT NULL,
    session_id      text        NOT NULL,
    partition_date  date        NOT NULL,
    request_id      text        NOT NULL,
    update_payload  jsonb       NOT NULL,
    status          text        NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'claimed', 'done', 'dead')),
    attempts        integer     NOT NULL DEFAULT 0,
    last_error      text,
    next_retry_at   timestamptz NOT NULL DEFAULT NOW(),
    claimed_at      timestamptz,
    completed_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT NOW(),
    updated_at      timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT session_aggregate_outbox_unique_request
        UNIQUE (tenant_id, session_id, partition_date, request_id)
);

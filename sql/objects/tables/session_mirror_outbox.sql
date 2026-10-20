--
-- Name: session_mirror_outbox; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/712_session_mirror_outbox.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-20 补录（审计 D3c）：本表此前在正典目录里没有任何条目，
--   导致「按正典文件读会话族」的人读出一张不存在的表。

CREATE TABLE public.session_mirror_outbox (
    id            bigserial PRIMARY KEY,
    -- tenant_id 物化自 payload（entry.TenantID；空落 'default'，与主表
    -- INSERT 侧 nonEmpty 语义一致），供 RLS 与运维检索使用。
    tenant_id     text        NOT NULL DEFAULT 'default',
    request_id    text        NOT NULL,
    session_id    text        NOT NULL,
    source        text        NOT NULL DEFAULT 'hook'
                  CHECK (source IN ('hook', 'backfill')),
    fail_reason   text        NOT NULL DEFAULT '',
    payload       jsonb       NOT NULL,
    status        text        NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'claimed', 'dead')),
    attempts      integer     NOT NULL DEFAULT 0,
    last_error    text,
    next_retry_at timestamptz NOT NULL DEFAULT NOW(),
    claimed_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT NOW(),
    updated_at    timestamptz NOT NULL DEFAULT NOW(),
    -- 回填幂等与 hook 重复失败收敛都锚在 request_id（v1 终态行唯一）。
    CONSTRAINT session_mirror_outbox_unique_request UNIQUE (request_id)
);

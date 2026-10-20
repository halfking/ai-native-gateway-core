--
-- Name: session_project_attribution; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/547_session_project_attribution.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-20 补录（审计 D3c）：本表此前在正典目录里没有任何条目，
--   导致「按正典文件读会话族」的人读出一张不存在的表。

CREATE TABLE public.session_project_attribution (
    id              BIGSERIAL PRIMARY KEY,
    gw_session_id   VARCHAR(128) NOT NULL,
    tenant_id       VARCHAR(255) NOT NULL,

    -- Inferred ACC project/task. project_ref is whatever the resolver
    -- could pin down: an ACC project id when a rule matched one, or a
    -- free-text label from the LLM tier when it could only name it.
    project_ref     TEXT,
    project_label   TEXT,
    task_ref        TEXT,

    -- Which tier produced this. Kept deliberately parallel to
    -- session_tags.tag_source (auto|llm|manual) from migration 351.
    --   rule    — deterministic signal (repo path, work type, git remote)
    --   inherit — same device/key recently attributed to this project
    --   llm     — model call, last resort
    --   manual  — a human confirmed or corrected it
    method          VARCHAR(20) NOT NULL
        CONSTRAINT session_project_attribution_method_check
        CHECK (method IN ('rule', 'inherit', 'llm', 'manual')),

    confidence      REAL NOT NULL DEFAULT 0.0
        CONSTRAINT session_project_attribution_confidence_check
        CHECK (confidence >= 0.0 AND confidence <= 1.0),

    -- pending  — awaiting human review
    -- confirmed— a human accepted it (or a rule was trusted outright)
    -- rejected — a human said no; kept so we never re-infer the same session
    status          VARCHAR(20) NOT NULL DEFAULT 'pending'
        CONSTRAINT session_project_attribution_status_check
        CHECK (status IN ('pending', 'confirmed', 'rejected')),

    -- Free-form trace of why: matched rule name, prompt digest, etc.
    evidence        JSONB,

    reviewed_by     VARCHAR(100),
    reviewed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- 按 (tenant_id, gw_session_id) 唯一而不是只按会话 id：本库不保证
    -- gw_session_id 跨租户唯一，只按会话去重会让第二个租户的同名会话被
    -- ON CONFLICT DO NOTHING 静默丢弃，永远拿不到归属。
    CONSTRAINT uq_session_project_attribution UNIQUE (tenant_id, gw_session_id)
);


--
-- Name: TABLE session_project_attribution; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_project_attribution IS '会话→项目的推断归属（非 ACC 认领路径）。与 session_dim.project_id 分离：
     后者是 ACC 传入的权威值用于计费，本表是推断值仅用于归集分析与人工确认。';

-- 373_vibecoding.sql
-- VibeCoding AI编程模块数据表
--
-- VibeCoding是独立顶层包，不与现有安全/路由模块耦合，
-- 未来可独立演进和商业化。
--
-- 三张表：
--   vibe_coding_projects  - 用户编程项目
--   vibe_coding_sessions  - AI编程交互会话
--   vibe_code_reviews     - 代码审查记录

CREATE TABLE IF NOT EXISTS vibe_coding_projects (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    name            TEXT NOT NULL,
    description     TEXT,
    language        TEXT,
    framework       TEXT,
    status          TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'archived', 'deleted')),
    settings        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS vcp_tenant ON vibe_coding_projects (tenant_id);
CREATE INDEX IF NOT EXISTS vcp_status ON vibe_coding_projects (status);

ALTER TABLE vibe_coding_projects ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_vcp ON public.vibe_coding_projects;
CREATE POLICY tenant_isolation_vcp ON public.vibe_coding_projects
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

CREATE TABLE IF NOT EXISTS vibe_coding_sessions (
    id              BIGSERIAL PRIMARY KEY,
    project_id      BIGINT REFERENCES vibe_coding_projects(id) ON DELETE SET NULL,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    session_id      TEXT NOT NULL,
    task_type       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'completed', 'failed', 'cancelled')),
    messages        JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS vcs_project ON vibe_coding_sessions (project_id);
CREATE INDEX IF NOT EXISTS vcs_session ON vibe_coding_sessions (session_id);
CREATE INDEX IF NOT EXISTS vcs_tenant ON vibe_coding_sessions (tenant_id, created_at DESC);

ALTER TABLE vibe_coding_sessions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_vcs ON public.vibe_coding_sessions;
CREATE POLICY tenant_isolation_vcs ON public.vibe_coding_sessions
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

CREATE TABLE IF NOT EXISTS vibe_code_reviews (
    id              BIGSERIAL PRIMARY KEY,
    session_id      BIGINT REFERENCES vibe_coding_sessions(id) ON DELETE SET NULL,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    file_path       TEXT,
    language        TEXT,
    original_code   TEXT,
    review_result   JSONB,
    score           NUMERIC(3,2),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS vcr_session ON vibe_code_reviews (session_id);
CREATE INDEX IF NOT EXISTS vcr_tenant ON vibe_code_reviews (tenant_id, created_at DESC);

ALTER TABLE vibe_code_reviews ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_vcr ON public.vibe_code_reviews;
CREATE POLICY tenant_isolation_vcr ON public.vibe_code_reviews
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

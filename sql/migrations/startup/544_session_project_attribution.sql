-- 544_session_project_attribution.sql
-- 2026-08-20: session → project attribution for non-ACC agents.
--
-- Two classes of traffic reach the gateway:
--
--   1. Agents that claimed a task from ACC. They carry the real ACC
--      primary keys in X-Gw-Project-Id / X-Gw-Task-Id. That value is
--      authoritative and lands in session_dim.project_id (migration 407).
--
--   2. Every other agent. Those headers are empty and, per the operator,
--      cannot be made mandatory — the clients are outside our control.
--      For this class the project has to be *inferred*.
--
-- Inferred values MUST NOT be written to session_dim.project_id. That
-- column feeds billing and per-project accounting, and mixing a ~85%
-- accurate guess into it makes every downstream number unauditable —
-- nobody can tell which rows were real. This mirrors the OpenTelemetry
-- GenAI rule for gen_ai.conversation.id: when no identifier is
-- available, leave it unset rather than substituting a fallback.
--
-- So inference gets its own table. Confirmed attributions are for
-- grouping and analysis only (improving AI usage efficiency/quality),
-- never for billing.
--
-- Both tables are additive and nullable; pre-existing rows are unaffected.

-- ============================================================
-- 1. session_project_attribution — one row per session
-- ============================================================
CREATE TABLE IF NOT EXISTS public.session_project_attribution (
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

CREATE INDEX IF NOT EXISTS idx_spa_tenant_status
    ON public.session_project_attribution (tenant_id, status, created_at DESC);

-- InheritFromHistory 按 updated_at DESC 取最近一条已确认归属；
-- 上面按 created_at 排序的索引支撑不了这个 ORDER BY。
CREATE INDEX IF NOT EXISTS idx_spa_tenant_confirmed_updated
    ON public.session_project_attribution (tenant_id, updated_at DESC)
    WHERE status = 'confirmed' AND project_ref IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_spa_project
    ON public.session_project_attribution (tenant_id, project_ref)
    WHERE project_ref IS NOT NULL;

-- Review queue: the admin UI pulls low-confidence rows first.
CREATE INDEX IF NOT EXISTS idx_spa_pending_confidence
    ON public.session_project_attribution (tenant_id, confidence)
    WHERE status = 'pending';

COMMENT ON TABLE public.session_project_attribution IS
    '会话→项目的推断归属（非 ACC 认领路径）。与 session_dim.project_id 分离：
     后者是 ACC 传入的权威值用于计费，本表是推断值仅用于归集分析与人工确认。';
COMMENT ON COLUMN public.session_project_attribution.method IS
    '推断方式：rule=确定性规则 / inherit=历史继承 / llm=模型兜底 / manual=人工标注';
COMMENT ON COLUMN public.session_project_attribution.status IS
    'pending=待人工确认 / confirmed=已确认 / rejected=已否决（不再重复推断）';
COMMENT ON COLUMN public.session_project_attribution.evidence IS
    '推断依据：命中的规则名、信号来源、模型原始输出等，供人工复核时判断';

ALTER TABLE public.session_project_attribution ENABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname = 'public'
          AND tablename = 'session_project_attribution'
          AND policyname = 'spa_tenant_isolation'
    ) THEN
        CREATE POLICY spa_tenant_isolation ON public.session_project_attribution
            USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname = 'public'
          AND tablename = 'session_project_attribution'
          AND policyname = 'spa_super_admin_bypass'
    ) THEN
        CREATE POLICY spa_super_admin_bypass ON public.session_project_attribution
            USING (current_setting('app.current_role', true) = 'super_admin'
                OR current_setting('app.bypass_rls', true) = 'true');
    END IF;
END $$;

-- ============================================================
-- 2. project_dim — local mirror of ACC projects
-- ============================================================
-- Pulled from ACC the same way work_type_config is (admin/acc_work_types.go,
-- GET /api/llm/work-types). The hot request path never calls ACC; it only
-- reads this table, so an ACC outage cannot affect request serving.
CREATE TABLE IF NOT EXISTS public.project_dim (
    project_ref     TEXT PRIMARY KEY,
    tenant_id       VARCHAR(255),
    name            TEXT NOT NULL,
    description     TEXT,
    -- Keywords/paths used by the rule tier to match a session onto this
    -- project without any model call.
    match_keywords  TEXT[],
    repo_paths      TEXT[],
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    synced_from_acc_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_project_dim_enabled
    ON public.project_dim (enabled) WHERE enabled;

COMMENT ON TABLE public.project_dim IS
    'ACC 项目本地维表。由后台定时从 ACC 同步（参照 work_type_config 模式），
     供推断层做规则匹配与展示项目名，热路径只读本表。';
COMMENT ON COLUMN public.project_dim.match_keywords IS
    '规则层关键词：命中即可 0 成本确定项目，无需调用模型';

-- ============================================================
-- 3. request_logs 支撑索引
-- ============================================================
-- LoadSignals 按 (tenant_id, gw_session_id) 取会话首条请求。migration 355
-- 的 idx_request_logs_gw_session_id 只有 gw_session_id 单列，且本查询还要
-- 按 ts 排序取第一条；补一个联合索引避免每次会话关闭都回表排序。
--
-- request_logs 按 ts RANGE 分区，查询侧已带 ts 窗口做分区裁剪，这里的索引
-- 在每个分区上生效。
CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_session_ts
    ON public.request_logs (tenant_id, gw_session_id, ts)
    WHERE gw_session_id IS NOT NULL;

COMMENT ON INDEX public.idx_request_logs_tenant_session_ts IS
    '会话归属推断：按租户+会话取首条请求的信号（projectattr.LoadSignals）';

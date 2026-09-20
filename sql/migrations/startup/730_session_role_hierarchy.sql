-- Migration 730: Session Role Hierarchy (主代理 / 编排代理 / 规划代理 / 子任务 识别)
--
-- Purpose: 支持多子代理并行场景下从会话级别识别身份与父子关联，并落地
--          "按角色 × 任务类型(TaskKind) 自动选 LLM" 的配置表 role_task_llm_mapping。
--
-- 修订记录:
--   2026-09-19 初稿（种子模型名为占位猜测，未应用）。
--   2026-09-20 R48 重写: ① 种子映射按用户口径修正为轻量池
--             minimax-m3/glm-5.3-flash/kimi-k3/deepseek-v4-flash 与
--             重量池 glm-5.3/claude-opus-5/gpt-5.6-sol/grok-4.6/deepseek-v4-pro
--             （与 202609_03 tier 分类中的 canonical_name 命名体系一致；
--             应用前仍需 SELECT canonical_name FROM models_canonical 实测核对，
--             见 handoff §7）；② 映射维度从粗粒度 TaskType 改为正交的细粒度
--             TaskKind（search/summarize/git_ops/ops/analysis/planning/solution/
--             unknown），不污染现有 21 个 TaskType；③ UNIQUE 键加入 priority
--             以支持同一 (role,kind) 多行偏好（primary/secondary）；
--             ④ provider_models 轻量模型 tier 修正 tier-b → tier-c。
--
-- 设计：
--   1. public.sessions 增加 3 列：agent_role / parent_session_id / parent_task_id。
--      sessions 是 PARTITION BY RANGE (partition_date) 表，PostgreSQL ≥ 11
--      ALTER TABLE 会自动级联到所有现存分区与未来分区，无需手工遍历。
--   2. 新增 public.role_task_llm_mapping 二维映射表（agent_role × task_kind →
--      llm_canonical_name，priority 升序为偏好顺序）。
--      决策顺序（Decider R48）：override_pin > role_route(本表) > work_type tier
--      policy > task_default_routing > 自动分类默认。
--   3. 默认种子（INSERT ... ON CONFLICT DO NOTHING，幂等可重放）：
--      按"任务类型定池、角色记录审计"的用户口径——
--      - 轻量池（搜索/总结/git 操作/运维 + 无法判定任务时的兜底）:
--        minimax-m3 / glm-5.3-flash / kimi-k3 / deepseek-v4-flash
--      - 重量池（分析/规划/方案编写）:
--        glm-5.3 / claude-opus-5 / gpt-5.6-sol / grok-4.6 / deepseek-v4-pro
--      - 仅子代理角色（worker/planner/orchestrator）播种；main 会话不写行
--        （保持 Decider 默认路径不变，main 的显式覆盖由管理员按需手工插入）。
--
-- Idempotent: YES (ADD COLUMN IF NOT EXISTS, CREATE TABLE IF NOT EXISTS,
--                      ON CONFLICT DO NOTHING, tier UPDATE 带 <> 守卫)
-- Breaking: NO（所有新列可空/带默认值，向后兼容现有会话行）
-- Down: 730_session_role_hierarchy.down.sql

BEGIN;

-- =====================================================================
-- 1. sessions 表加列
-- =====================================================================
-- ALTER TABLE 在 PostgreSQL 11+ 对分区父表的列操作会自动级联到所有子分区。

ALTER TABLE public.sessions
    ADD COLUMN IF NOT EXISTS agent_role         TEXT NOT NULL DEFAULT 'main'
        CHECK (agent_role IN ('main', 'orchestrator', 'planner', 'worker', 'unknown')),
    ADD COLUMN IF NOT EXISTS parent_session_id  TEXT,
    ADD COLUMN IF NOT EXISTS parent_task_id     TEXT;

COMMENT ON COLUMN public.sessions.agent_role IS
    '会话角色标识（730/R48）：main=用户直接会话, orchestrator=编排代理, planner=规划代理, worker=子任务/子代理, unknown=未声明。X-Gw-Agent-Role 头是来源（客户端声明式提示，与 X-Gw-Task-Hint 同信任级）。';
COMMENT ON COLUMN public.sessions.parent_session_id IS
    '父会话 ID（730）：当前 worker/planner 是哪个会话的子任务。X-Gw-Parent-Session-Id 头是来源。空 = 无父会话（top-level）。';
COMMENT ON COLUMN public.sessions.parent_task_id IS
    '父任务 ID（730）：当前 worker/planner 的父任务引用，便于跨会话追溯。';

-- 普通索引：sub-agent 运维场景下经常需要按父会话查询（"这个父会话下挂了哪些 worker"）。
CREATE INDEX IF NOT EXISTS idx_sessions_parent_session
    ON public.sessions (tenant_id, parent_session_id)
    WHERE parent_session_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_agent_role
    ON public.sessions (tenant_id, agent_role, created_at DESC)
    WHERE agent_role <> 'main';

-- =====================================================================
-- 2. role_task_llm_mapping 二维配置表
-- =====================================================================
-- 注意：task_kind 是 R48 引入的正交细粒度任务分类，与 TaskType
-- （chat/code/reasoning/... 21 值）语义不同层，勿混用。

CREATE TABLE IF NOT EXISTS public.role_task_llm_mapping (
    id                 BIGSERIAL PRIMARY KEY,
    tenant_id          VARCHAR(255),                       -- NULL = 平台级（默认）
    agent_role         TEXT NOT NULL,
    task_kind          TEXT NOT NULL,                      -- search/summarize/git_ops/ops/analysis/planning/solution/unknown
    llm_canonical_name TEXT NOT NULL,                      -- 目标模型 canonical_name
    priority           INT  NOT NULL DEFAULT 100,          -- 数值越小优先级越高；同 (role,kind) 多行按此排序
    enabled            BOOLEAN NOT NULL DEFAULT TRUE,
    note               TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT role_task_llm_mapping_role_check
        CHECK (agent_role IN ('main', 'orchestrator', 'planner', 'worker', 'unknown')),
    CONSTRAINT role_task_llm_mapping_kind_check
        CHECK (task_kind IN ('search', 'summarize', 'git_ops', 'ops', 'analysis', 'planning', 'solution', 'unknown')),
    CONSTRAINT role_task_llm_mapping_unique
        UNIQUE (tenant_id, agent_role, task_kind, priority)
);

COMMENT ON TABLE public.role_task_llm_mapping IS
    '730/R48: 按 agent_role × task_kind 的二维 LLM 路由配置（priority 升序为偏好顺序，SelectLLM 依次尝试首个在候选池中的模型）。Decider 决策顺序：override_pin > role_route(本表) > work_type tier policy > task_default_routing > 自动分类默认。灰度开关 AUTO_ROLE_ROUTING_ENABLED（默认关）。';

CREATE INDEX IF NOT EXISTS idx_role_task_llm_mapping_lookup
    ON public.role_task_llm_mapping (tenant_id, agent_role, task_kind, enabled, priority)
    WHERE enabled = TRUE;

-- =====================================================================
-- 3. 默认种子映射（仅在首次 apply 时插入；后续可由 admin 调整）
-- =====================================================================
-- 与 autoroute/role_llm_router.go 的内存默认表 builtinRoleLLMPreference 一一镜像：
-- Go 侧 SelectLLM 优先读本表快照（DB 覆盖），无行时回退内存默认——两处口径
-- 必须一致，否则 DB 行被删除后行为漂移。
--
-- 用户口径（2026-09-19 handoff §1）：
--   搜索/总结/git 操作/运维 → 轻量池；分析/规划/方案编写 → 重量池；
--   无法确定任务类型时默认 glm-5.3-flash / minimax-m3。
-- 三个子代理角色使用同一 kind→池映射（角色差异由管理员按需覆盖），仅播种
-- worker/planner/orchestrator；main 不播种（保持既有默认路由不变）。

INSERT INTO public.role_task_llm_mapping
    (tenant_id, agent_role, task_kind, llm_canonical_name, priority, enabled, note)
VALUES
    -- ---- 轻量池：search（搜索/检索/查找） ----
    (NULL, 'worker', 'search',        'minimax-m3',        100, TRUE, '730/R48: worker 搜索 → minimax-m3（轻量池）'),
    (NULL, 'worker', 'search',        'glm-5.3-flash',     110, TRUE, '730/R48: worker 搜索备选 → glm-5.3-flash'),
    (NULL, 'planner', 'search',       'minimax-m3',        100, TRUE, '730/R48: planner 搜索 → minimax-m3'),
    (NULL, 'planner', 'search',       'glm-5.3-flash',     110, TRUE, '730/R48: planner 搜索备选 → glm-5.3-flash'),
    (NULL, 'orchestrator', 'search',  'minimax-m3',        100, TRUE, '730/R48: orchestrator 搜索 → minimax-m3'),
    (NULL, 'orchestrator', 'search',  'glm-5.3-flash',     110, TRUE, '730/R48: orchestrator 搜索备选 → glm-5.3-flash'),

    -- ---- 轻量池：summarize（总结/摘要/概括） ----
    (NULL, 'worker', 'summarize',     'glm-5.3-flash',     100, TRUE, '730/R48: worker 总结 → glm-5.3-flash'),
    (NULL, 'worker', 'summarize',     'minimax-m3',        110, TRUE, '730/R48: worker 总结备选 → minimax-m3'),
    (NULL, 'planner', 'summarize',    'glm-5.3-flash',     100, TRUE, '730/R48: planner 总结 → glm-5.3-flash'),
    (NULL, 'planner', 'summarize',    'minimax-m3',        110, TRUE, '730/R48: planner 总结备选 → minimax-m3'),
    (NULL, 'orchestrator', 'summarize','glm-5.3-flash',    100, TRUE, '730/R48: orchestrator 总结 → glm-5.3-flash'),
    (NULL, 'orchestrator', 'summarize','minimax-m3',       110, TRUE, '730/R48: orchestrator 总结备选 → minimax-m3'),

    -- ---- 轻量池：git_ops（git 操作/提交/分支） ----
    (NULL, 'worker', 'git_ops',       'kimi-k3',           100, TRUE, '730/R48: worker git 操作 → kimi-k3'),
    (NULL, 'worker', 'git_ops',       'deepseek-v4-flash', 110, TRUE, '730/R48: worker git 备选 → deepseek-v4-flash'),
    (NULL, 'planner', 'git_ops',      'kimi-k3',           100, TRUE, '730/R48: planner git 操作 → kimi-k3'),
    (NULL, 'planner', 'git_ops',      'deepseek-v4-flash', 110, TRUE, '730/R48: planner git 备选 → deepseek-v4-flash'),
    (NULL, 'orchestrator', 'git_ops', 'kimi-k3',           100, TRUE, '730/R48: orchestrator git 操作 → kimi-k3'),
    (NULL, 'orchestrator', 'git_ops', 'deepseek-v4-flash', 110, TRUE, '730/R48: orchestrator git 备选 → deepseek-v4-flash'),

    -- ---- 轻量池：ops（运维/部署/排查） ----
    (NULL, 'worker', 'ops',           'deepseek-v4-flash', 100, TRUE, '730/R48: worker 运维 → deepseek-v4-flash'),
    (NULL, 'worker', 'ops',           'kimi-k3',           110, TRUE, '730/R48: worker 运维备选 → kimi-k3'),
    (NULL, 'planner', 'ops',          'deepseek-v4-flash', 100, TRUE, '730/R48: planner 运维 → deepseek-v4-flash'),
    (NULL, 'planner', 'ops',          'kimi-k3',           110, TRUE, '730/R48: planner 运维备选 → kimi-k3'),
    (NULL, 'orchestrator', 'ops',     'deepseek-v4-flash', 100, TRUE, '730/R48: orchestrator 运维 → deepseek-v4-flash'),
    (NULL, 'orchestrator', 'ops',     'kimi-k3',           110, TRUE, '730/R48: orchestrator 运维备选 → kimi-k3'),

    -- ---- 重量池：analysis（分析/根因/评估） ----
    (NULL, 'worker', 'analysis',      'glm-5.3',           100, TRUE, '730/R48: worker 分析 → glm-5.3（重量池）'),
    (NULL, 'worker', 'analysis',      'deepseek-v4-pro',   110, TRUE, '730/R48: worker 分析备选 → deepseek-v4-pro'),
    (NULL, 'planner', 'analysis',     'glm-5.3',           100, TRUE, '730/R48: planner 分析 → glm-5.3'),
    (NULL, 'planner', 'analysis',     'deepseek-v4-pro',   110, TRUE, '730/R48: planner 分析备选 → deepseek-v4-pro'),
    (NULL, 'orchestrator', 'analysis','glm-5.3',           100, TRUE, '730/R48: orchestrator 分析 → glm-5.3'),
    (NULL, 'orchestrator', 'analysis','deepseek-v4-pro',   110, TRUE, '730/R48: orchestrator 分析备选 → deepseek-v4-pro'),

    -- ---- 重量池：planning（规划/计划/排期） ----
    (NULL, 'worker', 'planning',      'claude-opus-5',     100, TRUE, '730/R48: worker 规划 → claude-opus-5（重量池）'),
    (NULL, 'worker', 'planning',      'glm-5.3',           110, TRUE, '730/R48: worker 规划备选 → glm-5.3'),
    (NULL, 'planner', 'planning',     'claude-opus-5',     100, TRUE, '730/R48: planner 规划 → claude-opus-5'),
    (NULL, 'planner', 'planning',     'glm-5.3',           110, TRUE, '730/R48: planner 规划备选 → glm-5.3'),
    (NULL, 'orchestrator', 'planning','claude-opus-5',     100, TRUE, '730/R48: orchestrator 规划 → claude-opus-5'),
    (NULL, 'orchestrator', 'planning','glm-5.3',           110, TRUE, '730/R48: orchestrator 规划备选 → glm-5.3'),

    -- ---- 重量池：solution（方案编写/解决方案） ----
    (NULL, 'worker', 'solution',      'gpt-5.6-sol',       100, TRUE, '730/R48: worker 方案编写 → gpt-5.6-sol（重量池）'),
    (NULL, 'worker', 'solution',      'grok-4.6',          110, TRUE, '730/R48: worker 方案备选 → grok-4.6'),
    (NULL, 'planner', 'solution',     'gpt-5.6-sol',       100, TRUE, '730/R48: planner 方案编写 → gpt-5.6-sol'),
    (NULL, 'planner', 'solution',     'grok-4.6',          110, TRUE, '730/R48: planner 方案备选 → grok-4.6'),
    (NULL, 'orchestrator', 'solution','gpt-5.6-sol',       100, TRUE, '730/R48: orchestrator 方案编写 → gpt-5.6-sol'),
    (NULL, 'orchestrator', 'solution','grok-4.6',          110, TRUE, '730/R48: orchestrator 方案备选 → grok-4.6'),

    -- ---- 轻量兜底：unknown（无法判定任务类型 → 用户口径默认） ----
    (NULL, 'worker', 'unknown',       'glm-5.3-flash',     100, TRUE, '730/R48: worker 任务类型未知 → glm-5.3-flash（用户口径默认）'),
    (NULL, 'worker', 'unknown',       'minimax-m3',        110, TRUE, '730/R48: worker 任务类型未知备选 → minimax-m3'),
    (NULL, 'planner', 'unknown',      'glm-5.3-flash',     100, TRUE, '730/R48: planner 任务类型未知 → glm-5.3-flash'),
    (NULL, 'planner', 'unknown',      'minimax-m3',        110, TRUE, '730/R48: planner 任务类型未知备选 → minimax-m3'),
    (NULL, 'orchestrator', 'unknown', 'glm-5.3-flash',     100, TRUE, '730/R48: orchestrator 任务类型未知 → glm-5.3-flash'),
    (NULL, 'orchestrator', 'unknown', 'minimax-m3',        110, TRUE, '730/R48: orchestrator 任务类型未知备选 → minimax-m3')

ON CONFLICT (tenant_id, agent_role, task_kind, priority) DO NOTHING;

-- =====================================================================
-- 4. provider_models 轻量模型 tier 修正（R48）
-- =====================================================================
-- 202609_03 把 minimax-m3 归为 tier-b、glm-5.3-flash / kimi-k3 被"未分类默认
-- tier-b"兜底；用户口径（handoff §1）将这四个模型定位为轻量池 → tier-c。
-- deepseek-v4-flash 在 202609_03 已是 tier-c，列入 IN 列表仅为幂等兜底，
-- WHERE 守卫保证已正确的行零改动（可重放）。

UPDATE public.provider_models
SET tier = 'tier-c'
WHERE canonical_name IN ('minimax-m3', 'glm-5.3-flash', 'kimi-k3', 'deepseek-v4-flash')
  AND COALESCE(tier, '') <> 'tier-c';

COMMENT ON COLUMN public.provider_models.tier IS
    'Model tier classification: tier-a (high-perf, $15-50/1M), tier-b (standard, $5-15/1M), tier-c (economy, $0.5-5/1M)。730/R48 修正：minimax-m3/glm-5.3-flash/kimi-k3/deepseek-v4-flash 归入 tier-c（轻量池，role_route 路由目标）。';

-- =====================================================================
-- 5. RLS — role_task_llm_mapping
-- =====================================================================

ALTER TABLE public.role_task_llm_mapping ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS role_task_llm_mapping_tenant_isolation ON public.role_task_llm_mapping;
CREATE POLICY role_task_llm_mapping_tenant_isolation ON public.role_task_llm_mapping
    USING (
        -- 平台级行（tenant_id IS NULL）任何租户可见
        tenant_id IS NULL
        OR tenant_id = current_setting('app.current_tenant', true)::TEXT
    );

DROP POLICY IF EXISTS role_task_llm_mapping_super_admin_bypass ON public.role_task_llm_mapping;
CREATE POLICY role_task_llm_mapping_super_admin_bypass ON public.role_task_llm_mapping
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

-- =====================================================================
-- 6. 验证
-- =====================================================================

DO $$
DECLARE
    col_count INT;
    tbl_count INT;
    mapping_count INT;
    bad_tier_count INT;
BEGIN
    -- 验证 sessions 3 个新列都已创建
    SELECT COUNT(*) INTO col_count
    FROM information_schema.columns
    WHERE schemaname = 'public'
      AND tablename  = 'sessions'
      AND column_name IN ('agent_role', 'parent_session_id', 'parent_task_id');

    IF col_count < 3 THEN
        RAISE EXCEPTION 'Migration 730: sessions columns missing (got %, want 3)', col_count;
    END IF;

    -- 验证 role_task_llm_mapping 表存在
    SELECT COUNT(*) INTO tbl_count
    FROM pg_tables
    WHERE schemaname = 'public' AND tablename = 'role_task_llm_mapping';

    IF tbl_count <> 1 THEN
        RAISE EXCEPTION 'Migration 730: role_task_llm_mapping table missing';
    END IF;

    -- 验证默认映射行数（3 子代理角色 × 8 kinds × 主备 2 行 = 48 条）
    SELECT COUNT(*) INTO mapping_count
    FROM public.role_task_llm_mapping
    WHERE tenant_id IS NULL AND enabled = TRUE;

    IF mapping_count < 48 THEN
        RAISE EXCEPTION 'Migration 730: default mappings not seeded (got %, want >= 48)', mapping_count;
    END IF;

    -- 验证轻量池 tier 修正生效
    SELECT COUNT(*) INTO bad_tier_count
    FROM public.provider_models
    WHERE canonical_name IN ('minimax-m3', 'glm-5.3-flash', 'kimi-k3', 'deepseek-v4-flash')
      AND COALESCE(tier, '') <> 'tier-c';

    IF bad_tier_count > 0 THEN
        RAISE EXCEPTION 'Migration 730: light-pool models still not tier-c (%)', bad_tier_count;
    END IF;

    RAISE NOTICE '===== Migration 730 (R48) SUCCESSFUL =====';
    RAISE NOTICE 'sessions: added agent_role, parent_session_id, parent_task_id';
    RAISE NOTICE 'role_task_llm_mapping: % default rows seeded', mapping_count;
    RAISE NOTICE 'provider_models: light-pool models normalized to tier-c';
END;
$$;

COMMIT;

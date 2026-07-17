-- 421_task_default_routing.sql
-- Phase: Auto 智能路由 — 显式默认路由表（M2）
-- 详见 docs/拆分/22-Auto智能路由与任务识别.md §22.6
-- 让运营为 (task_type, profile, tenant) 显式指定首选/兜底模型，优先级介于
-- override pin 与隐式 tag 评分之间。仿 routing_overrides 的表结构。
-- Idempotent: CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT EXISTS

BEGIN;

CREATE TABLE IF NOT EXISTS public.task_default_routing (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    task_type       text NOT NULL,
    -- '' 表示任意 profile（通用默认）；非空表示 profile 级覆盖
    profile         text NOT NULL DEFAULT '',
    -- primary/secondary/fallback：注入候选池时的 tier
    tier            text NOT NULL DEFAULT 'primary',
    -- models_canonical.canonical_name；写入时由 admin 校验 status='active'
    canonical_model text NOT NULL,
    -- NULL = 平台默认；非 NULL = 租户级覆盖（优先级更高）
    tenant_id       bigint,
    -- 数字越大优先级越高（同 (task,profile,tier,tenant) 内）
    priority        integer NOT NULL DEFAULT 100,
    reason          text NOT NULL DEFAULT '',
    created_by      text,
    created_at      timestamp with time zone NOT NULL DEFAULT now(),
    updated_at      timestamp with time zone NOT NULL DEFAULT now(),
    -- NULL = 永久；过期行不参与 Resolve
    expires_at      timestamp with time zone,
    CONSTRAINT task_default_routing_tier_check
        CHECK (tier = ANY (ARRAY['primary'::text, 'secondary'::text, 'fallback'::text])),
    CONSTRAINT task_default_routing_profile_check
        CHECK (profile = ANY (ARRAY[''::text, 'smart'::text, 'speed_first'::text, 'cost_first'::text]))
);

COMMENT ON TABLE public.task_default_routing IS
  'Auto 路由显式默认：为 (task_type, profile, tenant) 指定首选/兜底模型。tenant_id NULL=平台默认。Resolve 优先级：tenant+profile > tenant+通用 > platform+profile > platform+通用。';

-- 唯一约束：同一 (task_type, profile, tier, tenant) 只能有一条未过期行。
-- COALESCE 处理 NULL tenant_id（NULL 在 UNIQUE 中不冲突，需归一化）。
--
-- 2026-07-17 fix: 原 partial index 谓词 `WHERE expires_at IS NULL OR
-- expires_at > now()` 在 PostgreSQL 上报 "functions in index predicate must
-- be marked IMMUTABLE"（now() 是 STABLE），导致迁移失败、阻塞整个迁移链。
-- partial index predicate 不允许非 IMMUTABLE 函数。改用全表索引：唯一约束
-- 覆盖所有行，过期行的排重在应用层（default_routing_store 加载时按
-- expires_at 过滤）保证。task_default_routing 是运营手动配置的小表，全表
-- 索引无性能影响。
CREATE UNIQUE INDEX IF NOT EXISTS uq_task_default_routing
    ON public.task_default_routing (task_type, profile, tier, COALESCE(tenant_id, 0));

-- 热路径查询索引：default_routing_store.LoadAll 全表加载到内存 snapshot，
-- 按 (task_type, profile) 内存过滤。全表索引足够。
CREATE INDEX IF NOT EXISTS idx_task_default_routing_lookup
    ON public.task_default_routing (task_type, profile, tenant_id);

-- 审计表：记录每次 CRUD 的 actor / action / before / after
CREATE TABLE IF NOT EXISTS public.task_default_routing_audit (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ts              timestamp with time zone NOT NULL DEFAULT now(),
    action          text NOT NULL,
    routing_id      bigint,
    task_type       text,
    profile         text,
    tier            text,
    canonical_model text,
    tenant_id       bigint,
    priority        integer,
    reason          text,
    expires_at      timestamp with time zone,
    old_expires_at  timestamp with time zone,
    actor           text,
    CONSTRAINT task_default_routing_audit_action_check
        CHECK (action = ANY (ARRAY['insert'::text, 'update'::text, 'delete'::text]))
);

COMMENT ON TABLE public.task_default_routing_audit IS
  'task_default_routing 的变更审计；actor 必须是已认证的 super_admin 或 tenant_admin（后者仅可见本租户行）。';

COMMIT;

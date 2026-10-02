-- Migration 731: auto_route_selections 角色路由归因三列（R50 审计收口）。
--
-- 背景：R48 会话角色×任务类型路由上线后，role_route 强制选型的行与自然
-- 胜出的行在 auto_route_selections（训练/亲和学习源表）中不可区分——
-- AutoRouteSettleWorker 按 (task_type, profile) 格子归因 reward 时，role
-- 强制选型会污染同格子的学习信号；离线分析只能解析 request_logs.auto_decision
-- 的 JSONB。本迁移把决策侧已有的 wire.SessionRole/TaskKind/RoutingSource
-- 落为三列（buildAutoSelection 同步映射，见 domains/streaming/auto_route.go）。
--
-- 语义：
--   agent_role     — 请求声明的会话角色（main/orchestrator/planner/worker/unknown），
--                    flag-off 恒 NULL（wire 不透出）；
--   task_kind      — 细粒度任务分类（search/summarize/git_ops/ops/analysis/
--                    planning/solution/unknown），与 role_task_llm_mapping 同词表；
--   routing_source — 当前仅 role_route 命中时非空（与 wire.RoutingSource
--                    同口径：flag-off 字节不变承诺延伸到本表），亲和学习按
--                    routing_source IS DISTINCT FROM 'role_route' 剔除强制行。
--
-- 双表：写入方 selection_writer 插 auto_route_selections_hot（656 独立
-- heap），promote 后进分区父表——两表同加列（658 §2 先例）。
--
-- Idempotent: ADD COLUMN IF NOT EXISTS（父表+hot 表），可重放。
-- Breaking: NO（全可空列，旧行 NULL=未归因）。
-- Down: 731_auto_route_selection_role_attribution.down.sql

\set ON_ERROR_STOP on
BEGIN;

ALTER TABLE public.auto_route_selections
    ADD COLUMN IF NOT EXISTS agent_role TEXT,
    ADD COLUMN IF NOT EXISTS task_kind TEXT,
    ADD COLUMN IF NOT EXISTS routing_source TEXT;

ALTER TABLE public.auto_route_selections_hot
    ADD COLUMN IF NOT EXISTS agent_role TEXT,
    ADD COLUMN IF NOT EXISTS task_kind TEXT,
    ADD COLUMN IF NOT EXISTS routing_source TEXT;

COMMENT ON COLUMN public.auto_route_selections.agent_role IS
    '731/R50: 会话角色归因（R48 角色路由）。flag-off 恒 NULL。来源 wire.SessionRole。';
COMMENT ON COLUMN public.auto_route_selections.task_kind IS
    '731/R50: 细粒度任务分类（与 role_task_llm_mapping 同词表）。来源 wire.TaskKind。';
COMMENT ON COLUMN public.auto_route_selections.routing_source IS
    '731/R50: 路由功劳标签，当前仅 role_route 命中时非空；亲和学习按 IS DISTINCT FROM ''role_route'' 剔除强制选型行。';

INSERT INTO public.schema_migrations (version, description)
VALUES (
    '731',
    'auto_route_selections: role/kind/routing_source attribution for role-route learning hygiene'
)
ON CONFLICT (version) DO NOTHING;

COMMIT;

-- Down 731: 移除角色路由归因三列（父表 + hot 表）。
-- 仅删 731 引入的三列，不动 658 特征列与 650 归因列。

\set ON_ERROR_STOP on
BEGIN;

ALTER TABLE public.auto_route_selections
    DROP COLUMN IF EXISTS agent_role,
    DROP COLUMN IF EXISTS task_kind,
    DROP COLUMN IF EXISTS routing_source;

ALTER TABLE public.auto_route_selections_hot
    DROP COLUMN IF EXISTS agent_role,
    DROP COLUMN IF EXISTS task_kind,
    DROP COLUMN IF EXISTS routing_source;

DELETE FROM public.schema_migrations WHERE version = '731';

COMMIT;

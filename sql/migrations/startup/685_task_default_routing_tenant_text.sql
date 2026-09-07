-- Migration 685: task_default_routing(_audit).tenant_id bigint → text — 对齐 Go 的 text 租户模型
--
-- Background (2026-09-07 部署后观察, 22P02 x ~9/25min):
--   domains/streaming/model_alternatives.go 的 alternativesSQL 以
--   `tenant_id = $2 OR tenant_id = 'default'`(text)查询 task_default_routing;
--   autoroute/default_routing_store.go 把 tenant_id 扫进 *string。
--   但本库两张表是 421 的 bigint 形状(388 的 text 形状与之竞争),
--   $2 被推断为 bigint,pgx 传 "default" → SQLSTATE 22P02
--   "invalid input syntax for type bigint: \"default\"",
--   每个无候选请求的 alternatives 兜底查询全部失败。
--
-- Fix: bigint → text USING tenant_id::text(NULL 保持 NULL),并按 388 的
--   text 哨兵 COALESCE(tenant_id,'') 重建 uq_task_default_routing(PG 自动
--   重建会沿用 (0)::bigint 哨兵,直接报类型错)。表在本库为空(0 行);
--   非空库 USING 转换保留数值字面的可读形式。
--
-- Idempotent: YES(按 data_type 守卫,已是 text 则跳过)。

DO $$
DECLARE
  v_tenant_type text;
BEGIN
  SELECT data_type INTO v_tenant_type
    FROM information_schema.columns
   WHERE table_schema = 'public' AND table_name = 'task_default_routing'
     AND column_name = 'tenant_id';

  IF v_tenant_type IS NULL OR v_tenant_type = 'text' THEN
    RAISE NOTICE '685: task_default_routing.tenant_id missing or already text; skipping';
    RETURN;
  END IF;

  -- PG 的 ALTER COLUMN TYPE 会尝试沿用含 (0)::bigint 哨兵的旧索引重建,
  -- 必先手工拆掉两个 tenant 相关索引,ALTER 后按 388 规范重建。
  DROP INDEX IF EXISTS public.uq_task_default_routing;
  DROP INDEX IF EXISTS public.idx_task_default_routing_lookup;

  ALTER TABLE public.task_default_routing
      ALTER COLUMN tenant_id TYPE text USING tenant_id::text;

  CREATE UNIQUE INDEX uq_task_default_routing
      ON public.task_default_routing (task_type, profile, tier, (COALESCE(tenant_id, '')));
  CREATE INDEX idx_task_default_routing_lookup
      ON public.task_default_routing (task_type, profile, tenant_id);

  RAISE NOTICE '685: task_default_routing.tenant_id converted to text; indexes rebuilt with '''' sentinel';
END
$$;

-- audit 表同款漂移,同款处理(无索引依赖 tenant_id,仅列类型)。
DO $$
DECLARE
  v_tenant_type text;
BEGIN
  SELECT data_type INTO v_tenant_type
    FROM information_schema.columns
   WHERE table_schema = 'public' AND table_name = 'task_default_routing_audit'
     AND column_name = 'tenant_id';

  IF v_tenant_type IS NULL OR v_tenant_type = 'text' THEN
    RAISE NOTICE '685: task_default_routing_audit.tenant_id missing or already text; skipping';
    RETURN;
  END IF;

  ALTER TABLE public.task_default_routing_audit
      ALTER COLUMN tenant_id TYPE text USING tenant_id::text;
  RAISE NOTICE '685: task_default_routing_audit.tenant_id converted to text';
END
$$;

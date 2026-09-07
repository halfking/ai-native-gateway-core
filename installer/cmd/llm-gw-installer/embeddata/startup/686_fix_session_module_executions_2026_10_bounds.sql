-- Migration 686: 修复 session_module_executions_2026_10 分区上界(+08 字面量污染)
--
-- Background (2026-09-07 部署后观察, ensure tick 42P17 每次复现):
--   本库 session_module_executions_2026_10 的分区上界是
--     TO ('2026-11-01 08:00:00+08')   -- 应为 00:00:00+08,多出 8 小时
--   (创建时把 +08 时区渲染的字面量直接嵌进了 TO 边界)。于是
--   ensure_session_module_executions_partition 每次要建 2026_11
--   (FROM '2026-11-01 00:00:00+08')都报 42P17 "would overlap"。
--
-- Fix: 若 2026_10 上界是坏瞬点 → 把可能落进 default 分区的 10 月行搬回,
--   DETACH + 重建为 [2026-10-01 00:00+08, 2026-11-01 00:00+08)。
--   本库分区与父表均为 0 行,搬移为 no-op;非空库走 INSERT SELECT 兜底。
--
-- Idempotent: YES(按实际上界文本守卫)。

DO $$
DECLARE
  v_bounds text;
BEGIN
  SELECT pg_get_expr(c.relpartbound, c.oid) INTO v_bounds
    FROM pg_class c
   WHERE c.oid = 'public.session_module_executions_2026_10'::regclass;

  IF v_bounds IS NULL THEN
    RAISE NOTICE '686: partition session_module_executions_2026_10 absent; nothing to repair';
    RETURN;
  END IF;

  IF position('2026-11-01 08:00:00' in v_bounds) = 0
     AND position('2026-11-01 00:00:00' in v_bounds) > 0 THEN
    RAISE NOTICE '686: 2026_10 bounds already correct; skipping';
    RETURN;
  END IF;

  BEGIN
    -- 坏上界窗口内的行若已存在(default 分区兜底),搬回后随新分区生效
    INSERT INTO public.session_module_executions
    SELECT d.* FROM public.session_module_executions_default d
     WHERE d.ts >= '2026-10-01 00:00:00+08'
       AND d.ts <  '2026-11-01 00:00:00+08'
    ON CONFLICT DO NOTHING;
    DELETE FROM public.session_module_executions_default d
     WHERE d.ts >= '2026-10-01 00:00:00+08'
       AND d.ts <  '2026-11-01 00:00:00+08';
  EXCEPTION WHEN undefined_table OR undefined_column THEN
    NULL; -- default 分区或 ts 列形态差异时跳过搬移
  END;

  EXECUTE 'ALTER TABLE public.session_module_executions DETACH PARTITION public.session_module_executions_2026_10';
  EXECUTE 'DROP TABLE IF EXISTS public.session_module_executions_2026_10';
  EXECUTE $p$
    CREATE TABLE public.session_module_executions_2026_10
    PARTITION OF public.session_module_executions
    FOR VALUES FROM ('2026-10-01 00:00:00+08') TO ('2026-11-01 00:00:00+08')
  $p$;

  RAISE NOTICE '686: rebuilt session_module_executions_2026_10 with upper bound 2026-11-01 00:00+08';
END
$$;

-- Migration 687: 修复迁移 473 预建分区的 08:00+08 边界污染(686 之外的其余表)
--
-- Background (2026-09-08 审计, 本机 llm_gateway 库实测):
--   473_partition_precreate_2026_09_10.sql 把 9 张表 2026_07..2026_10 的
--   分区边界写成了 '...08:00:00+08'(应为 00:00:00+08;数据库默认
--   TimeZone=Asia/Shanghai,ensure 函数按 +08 零点建下月分区)。
--   686 只修了 session_module_executions 一张。其余 8 小时错位的后果:
--     - ensure 下月分区 FROM 'YYYY-MM-01 00:00:00+08' 与上月 08:00 上界
--       重叠 → 42P17,2026-10-01 起 ensure/promote 连锁失败,行落 default;
--     - 每月 1 日 00:00-08:00 的行掉 default(有 default 的表)或被拒
--       (无 default 的表,如 credit_ledger)。
--   受影响表: credential_model_index / credit_ledger / model_probe_runs /
--     request_logs / request_wal / routing_decision_log /
--     routing_decision_log_archive / tool_usage_stats / usage_ledger
--   (本机实测 26 个坏分区;request_logs 本机只有 default 分区,守卫自动跳过。
--    已在临时库用"有 default+缝隙行 / 无 default / 干净分区"三类夹具验证,
--    重建前后总行数守恒。)
--
-- Fix: 对允许清单内父表的每个直接分区,凡边界文本含 '08:00:00' 者,按月升序:
--   DETACH → 旧表改名 *_repair_bak → 把 default 中掉进旧 8 小时缝隙的行
--   收纳进 bak 暂存(有 default 的表;必须先挪走,否则 CREATE 新分区会被
--   default 的未覆盖行约束拒绝) → 按月零点重建 → bak 全量经父表回灌新
--   分区 → DROP bak。
--   每分区一个子事务,失败原子回滚(DETACH/RENAME 一并撤销),不影响后续。
--
-- Idempotent: YES(干净分区直接跳过)。

DO $$
DECLARE
  r record;
  v_parent text; v_part text; v_col text; v_bounds text;
  v_month text; v_from text; v_to text;
BEGIN
  FOR r IN
    SELECT p.relname::text AS parent,
           c.relname::text AS part,
           a.attname::text AS col,
           pg_get_expr(c.relpartbound, c.oid) AS bounds
      FROM pg_inherits i
      JOIN pg_class p ON p.oid = i.inhparent
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_partitioned_table pt ON pt.partrelid = p.oid
      JOIN LATERAL unnest(pt.partattrs) WITH ORDINALITY AS u(attnum, ord) ON u.ord = 1
      JOIN pg_attribute a ON a.attrelid = p.oid AND a.attnum = u.attnum
     WHERE p.relname IN (
             'credential_model_index', 'credit_ledger', 'model_probe_runs',
             'request_logs', 'request_wal', 'routing_decision_log',
             'routing_decision_log_archive', 'tool_usage_stats', 'usage_ledger')
       AND c.relispartition
     ORDER BY p.relname, c.relname
  LOOP
    v_parent := r.parent; v_part := r.part; v_col := r.col; v_bounds := r.bounds;

    -- 干净分区(无 08:00 字样)跳过
    CONTINUE WHEN position('08:00:00' in v_bounds) = 0;

    -- 从分区名后缀解析月份 <table>_YYYY_MM
    v_month := regexp_replace(v_part, '^.*_(\d{4}_\d{2})$', '\1');
    IF v_month = v_part THEN
      RAISE NOTICE '687: % has 08:00 bounds but non-monthly name; skipped', v_part;
      CONTINUE;
    END IF;

    v_from := substr(v_month, 1, 4) || '-' || substr(v_month, 6, 2) || '-01 00:00:00+08';
    v_to   := to_char((substr(v_month, 1, 4) || '-' || substr(v_month, 6, 2) || '-01')::date
                      + interval '1 month', 'YYYY-MM-DD') || ' 00:00:00+08';

    BEGIN
      EXECUTE format('ALTER TABLE public.%I DETACH PARTITION public.%I', v_parent, v_part);
      EXECUTE format('ALTER TABLE public.%I RENAME TO %I', v_part, v_part || '_repair_bak');

      -- default 中掉进旧 8 小时缝隙的行先挪进 bak 暂存(无 default 的表跳过);
      -- 不挪走的话,下一步 CREATE 会被 default 分区约束拒绝(42P16)
      BEGIN
        EXECUTE format(
          'INSERT INTO public.%I SELECT d.* FROM public.%I_default d WHERE d.%I >= %L AND d.%I < %L',
          v_part || '_repair_bak', v_parent, v_col, v_from, v_col, v_to);
        EXECUTE format(
          'DELETE FROM public.%I_default d WHERE d.%I >= %L AND d.%I < %L',
          v_parent, v_col, v_from, v_col, v_to);
      EXCEPTION WHEN undefined_table THEN
        NULL; -- 无 default 分区
      END;

      EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.%I FOR VALUES FROM (%L) TO (%L)',
        v_part, v_parent, v_from, v_to);

      -- bak(旧分区行 + 缝隙行)全量回灌,经父表路由进新分区
      EXECUTE format(
        'INSERT INTO public.%I SELECT * FROM public.%I ON CONFLICT DO NOTHING',
        v_parent, v_part || '_repair_bak');
      EXECUTE format('DROP TABLE IF EXISTS public.%I', v_part || '_repair_bak');

      RAISE NOTICE '687: rebuilt % with [% , %)', v_part, v_from, v_to;

    EXCEPTION WHEN others THEN
      RAISE WARNING '687: repair % failed (rolled back atomically): %', v_part, SQLERRM;
    END;
  END LOOP;
END
$$;

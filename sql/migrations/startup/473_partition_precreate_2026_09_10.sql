-- Migration 473: 2026_09 + 2026_10 partition pre-creation for all RANGE-partitioned tables
--
-- Background:
--   2026-08-07 审计发现：15 个 RANGE 分区表**只有 2026_07 + 2026_08 分区**，
--   缺 2026_09 + 2026_10。当 2026-08-31 23:59 → 2026-09-01 00:00 后，
--   INSERT with NOW() = 2026-09-01 日期会因「no partition found for row」失败，
--   整个生产环境（154 + 245）的核心路径（sessions, session_turns, request_wal,
--   usage_ledger, routing_decision_log 等）会全面宕机。
--
--   本迁移对齐 rule 33 §6.1「预创建下月分区」，对所有缺 2026_09 的 RANGE 表
--   统一补 2026_09 + 2026_10（双月预创建），同时建 default 兜底（rule 33 §2）。
--
-- Idempotent: pg_class.relname 检查（regclass cast 在缺失 relation 时报错）。

BEGIN;

DO $$
DECLARE
  -- Per-table config: parent, suffix_2026_09, from_2026_09, to_2026_09,
  -- suffix_2026_10, from_2026_10, to_2026_10
  parent_name text;
  rec record;
  has_2026_09 bool;
  has_2026_10 bool;
  has_default bool;
  cfg cursor FOR
    SELECT * FROM (VALUES
      ('credential_model_index', '2026_09', '2026-09-01 08:00:00+08', '2026-10-01 08:00:00+08', '2026_10', '2026-10-01 08:00:00+08', '2026-11-01 08:00:00+08'),
      ('credit_ledger', '2026_09', '2026-09-01 08:00:00+08', '2026-10-01 08:00:00+08', '2026_10', '2026-10-01 08:00:00+08', '2026-11-01 08:00:00+08'),
      ('dashboard_access_events', '2026_09', '2026-09-01 00:00:00+08', '2026-10-01 00:00:00+08', '2026_10', '2026-10-01 00:00:00+08', '2026-11-01 00:00:00+08'),
      ('model_probe_runs', '2026_09', '2026-09-01 00:00:00+08', '2026-10-01 00:00:00+08', '2026_10', '2026-10-01 00:00:00+08', '2026-11-01 08:00:00+08'),
      ('request_logs', '2026_09', '2026-09-01 08:00:00+08', '2026-10-01 08:00:00+08', '2026_10', '2026-10-01 08:00:00+08', '2026-11-01 08:00:00+08'),
      ('request_wal', '2026_09', '2026-09-01 08:00:00+08', '2026-10-01 08:00:00+08', '2026_10', '2026-10-01 08:00:00+08', '2026-11-01 08:00:00+08'),
      ('routing_decision_log', '2026_09', '2026-09-01 08:00:00+08', '2026-10-01 08:00:00+08', '2026_10', '2026-10-01 08:00:00+08', '2026-11-01 08:00:00+08'),
      ('routing_decision_log_archive', '2026_09', '2026-09-01 08:00:00+08', '2026-10-01 08:00:00+08', '2026_10', '2026-10-01 08:00:00+08', '2026-11-01 08:00:00+08'),
      ('session_bodies', '2026_09', '2026-09-01', '2026-10-01', '2026_10', '2026-10-01', '2026-11-01'),
      ('session_module_executions', '2026_09', '2026-09-01 00:00:00+08', '2026-10-01 00:00:00+08', '2026_10', '2026-10-01 00:00:00+08', '2026-11-01 08:00:00+08'),
      ('session_turns', '2026_09', '2026-09-01', '2026-10-01', '2026_10', '2026-10-01', '2026-11-01'),
      ('sessions', '2026_09', '2026-09-01', '2026-10-01', '2026_10', '2026-10-01', '2026-11-01'),
      ('tool_usage_stats', '2026_09', '2026-09-01 08:00:00+08', '2026-10-01 08:00:00+08', '2026_10', '2026-10-01 08:00:00+08', '2026-11-01 08:00:00+08'),
      ('usage_ledger', '2026_09', '2026-09-01 08:00:00+08', '2026-10-01 08:00:00+08', '2026_10', '2026-10-01 08:00:00+08', '2026-11-01 08:00:00+08')
    ) AS t(parent_name, suffix_09, from_09, to_09, suffix_10, from_10, to_10);
BEGIN
  OPEN cfg;
  LOOP
    FETCH cfg INTO rec;
    EXIT WHEN NOT FOUND;
    parent_name := rec.parent_name;

    -- 1. Check if 2026_09 already exists (use pg_class.relname to avoid regclass cast error)
    SELECT EXISTS (
      SELECT 1 FROM pg_inherits inh
      JOIN pg_class c ON c.oid = inh.inhrelid
      JOIN pg_class p ON p.oid = inh.inhparent
      WHERE p.relname = parent_name AND p.relnamespace = 'public'::regnamespace
        AND c.relname = parent_name || '_' || rec.suffix_09
    ) INTO has_2026_09;

    IF NOT has_2026_09 THEN
      EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.%I FOR VALUES FROM (%L) TO (%L)',
        parent_name || '_' || rec.suffix_09, parent_name, rec.from_09, rec.to_09
      );
    END IF;

    -- 2. Check if 2026_10 already exists
    SELECT EXISTS (
      SELECT 1 FROM pg_inherits inh
      JOIN pg_class c ON c.oid = inh.inhrelid
      JOIN pg_class p ON p.oid = inh.inhparent
      WHERE p.relname = parent_name AND p.relnamespace = 'public'::regnamespace
        AND c.relname = parent_name || '_' || rec.suffix_10
    ) INTO has_2026_10;

    IF NOT has_2026_10 THEN
      EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.%I FOR VALUES FROM (%L) TO (%L)',
        parent_name || '_' || rec.suffix_10, parent_name, rec.from_10, rec.to_10
      );
    END IF;

    -- 3. Add default partition if missing (rule 33 §2 兜底)
    SELECT EXISTS (
      SELECT 1 FROM pg_inherits inh
      JOIN pg_class c ON c.oid = inh.inhrelid
      JOIN pg_class p ON p.oid = inh.inhparent
      WHERE p.relname = parent_name AND p.relnamespace = 'public'::regnamespace
        AND c.relname = parent_name || '_default'
    ) INTO has_default;

    IF NOT has_default THEN
      EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.%I DEFAULT',
        parent_name || '_default', parent_name
      );
    END IF;

    RAISE NOTICE 'partition done: % (09=%, 10=%, default=%)',
      parent_name, NOT has_2026_09, NOT has_2026_10, NOT has_default;
  END LOOP;
  CLOSE cfg;
END $$;

COMMIT;
-- Migration 481: 补 request_logs_bodies 缺失的 2026_07 分区
--
-- 日期: 2026-08-13
--
-- Background
-- ──────────
-- 2026-08-12 审计发现（生产 154）: data-lifecycle hot cron 持续报
-- SQLSTATE 23514 "no partition of relation 'request_logs_bodies' found for row"。
--
-- Root cause:
--   bg.PartitionManager.promote_request_logs_bodies_hot_to_partition
--   (sql/migrations/startup/455_request_id_unique_for_hot_tables.sql:139)
--   用 INSERT INTO request_logs_bodies (父表) 把 hot 表的 7 天前数据 promote
--   出去。WHERE ts < now() - 7 days 在 2026-08-13 触发时即 ts < 2026-08-06，
--   这部分数据应路由到 request_logs_bodies_2026_07 (2026-07-01 → 2026-08-01)
--   分区，但 154 的 request_logs_bodies 只有 2026_08 + 2026_09 分区，
--   缺 2026_07，导致 INSERT 失败 → hot 数据堆积。
--
--   Migration 473 (2026-08-07) 预创建了 14 个表的 2026_09 + 2026_10，
--   但漏掉了 request_logs_bodies（473 cfg cursor 没列出这张表）。
--   baseline schema 启动时只 inline 创建了 2026_07 + 2026_08 两个
--   分区；473 又只补了 2026_09 + 2026_10，**2026_07 的 promote 路径
--   从未存在**。
--
--   154 的 245 / 252 PG17 上同一现象（共享数据库）。
--
-- Fix
-- ────
-- 补一个 2026_07 分区，与现有 2026_08 的边界对齐 (FROM/TO = month bounds)。
-- 与 473 迁移一致用 pg_inherits + pg_class.relname 检查以避免 regclass
-- cast 在缺失 relation 时报错。
--
-- Idempotent: 是（IF NOT EXISTS + pg_class existence check）
-- Down: 见 481_*.down.sql

BEGIN;

DO $$
DECLARE
  parent_name text := 'request_logs_bodies';
  suffix text := '2026_07';
  from_ts text := '2026-07-01 00:00:00+08';
  to_ts text := '2026-08-01 00:00:00+08';
  has_partition bool;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_inherits inh
    JOIN pg_class c ON c.oid = inh.inhrelid
    JOIN pg_class p ON p.oid = inh.inhparent
    WHERE p.relname = parent_name AND p.relnamespace = 'public'::regnamespace
      AND c.relname = parent_name || '_' || suffix
  ) INTO has_partition;

  IF NOT has_partition THEN
    EXECUTE format(
      'CREATE TABLE IF NOT EXISTS public.%I PARTITION OF public.%I
         FOR VALUES FROM (%L) TO (%L)',
      parent_name || '_' || suffix, parent_name, from_ts, to_ts
    );
    RAISE NOTICE 'created partition %_%', parent_name, suffix;
  ELSE
    RAISE NOTICE 'partition %_% already exists, skipping', parent_name, suffix;
  END IF;
END $$;

COMMIT;

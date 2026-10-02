-- ===========================================================================
-- File:          sql/migrations/startup/808_request_logs_default_partition.sql
-- Migration:     808
-- Database:      llm_gateway
-- Purpose:       补建 request_logs 的 DEFAULT 分区 request_logs_default
--
-- Status:        active
-- Idempotent:    YES (存在性检查 + RAISE NOTICE 跳过)
--
-- Background:
--   2026-10-01 Round 44 收口轮，在真实 installer 形态的一次性库上实测到：
--
--     SELECT c.relname FROM pg_class c
--       JOIN pg_inherits i ON i.inhrelid = c.oid
--       JOIN pg_class p   ON p.oid = i.inhparent
--      WHERE p.relname = 'request_logs';
--     => request_logs_2026_07, request_logs_2026_08      （仅 2 个月，无 DEFAULT）
--
--   全新安装产出的 request_logs 母表**没有任何 DEFAULT 分区**，于是
--   ts 落在 2026-08 之外的任何 INSERT 都直接失败：
--
--     ERROR: no partition of relation "request_logs" found for row
--     SQLSTATE 23514
--
--   这不是测试夹具问题，是全新安装的可用性缺陷：母表在 2026-09 之后就
--   写不进任何一行。已实测命中 3 个 integration 测试
--   （admin: TestHandleModelBreakdown_Success / _LongTailMerge / _HotMiss），
--   它们只是最先撞上它的那批写入方。
--
--   为什么 baseline 与迁移链都没建它：
--     - sql/schema/01-schema.sql 是 2026-08-04 的 pg_dump，dump 里
--       request_logs_default 只以函数体（archive_request_logs_default /
--       promote_request_logs_default_batch）里的引用出现，**没有对应的
--       CREATE TABLE ... PARTITION OF ... DEFAULT**；
--     - installer/cmd/llm-gw-installer/embeddata/startup/ 下 267 个启动
--       迁移里没有任何一条创建它（705 只负责把 337 摘下的月分区 ATTACH
--       回来，它的 repair 路径本身假定 DEFAULT 已存在）。
--   本机 llm_gateway 之所以有，是因为历史上有人手工建过——所以「生产有、
--   全新安装没有」这个差异一直没被发现。
--
--   DEFAULT 分区是承重的，不是可有可无：
--     - 705 的 repair 流程以「DETACH DEFAULT → 逐月 ATTACH → 把 DEFAULT
--       重新 ATTACH 回去」为骨架，DEFAULT 缺席时该流程无法收尾；
--     - bg/partition_manager.go:1238 在常驻循环里调用
--       ensure_request_logs_partition，而该函数体引用
--       request_logs_default —— 全新安装上它必然报错；
--     - promote_request_logs_default_batch 同样引用它。
--   4 个函数引用这张表，全新安装上全部不可用。
--
-- 为什么不预先建未来 12 个月分区：ensure_request_logs_partition 已经在
-- 按月补建，DEFAULT 的职责是接住「还没被 ensure 覆盖到」的窗口期写入，
-- 而不是取代 ensure。两条链的职责边界保持不变。
--
-- 验证（2026-10-01，本机一次性库）：
--   修前：INSERT ... ts=now() => ERROR 23514 no partition of relation
--         "request_logs" found for row
--   修后：同一语句路由进 request_logs_default（报错推进到该表自身的
--         NOT NULL 校验，说明分区路由已生效）
--   重跑：RAISE NOTICE 'request_logs_default already exists'（幂等）
-- ===========================================================================

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs'
                AND relkind IN ('r', 'p')) THEN
    IF NOT EXISTS (
      SELECT 1
      FROM pg_class c
      JOIN pg_inherits i ON i.inhrelid = c.oid
      JOIN pg_class p   ON p.oid   = i.inhparent
      JOIN pg_namespace n ON n.oid = c.relnamespace
      WHERE p.relname   = 'request_logs'
        AND n.nspname   = 'public'
        AND pg_get_expr(c.relpartbound, c.oid) = 'DEFAULT'
    ) THEN
      EXECUTE 'CREATE TABLE public.request_logs_default '
           || 'PARTITION OF public.request_logs DEFAULT';
      RAISE NOTICE '808: created public.request_logs_default';
    ELSE
      RAISE NOTICE '808: public.request_logs_default already exists';
    END IF;
  ELSE
    RAISE WARNING '808: public.request_logs does not exist; nothing to do';
  END IF;
END
$$;

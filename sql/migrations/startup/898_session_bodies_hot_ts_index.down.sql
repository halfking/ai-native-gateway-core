-- Migration 898 down: drop the ts-leading index added by 898.
--
-- ⚠ 如实登记：这是**一步不可逆回退**。
--   public.session_bodies_hot 的 ts 首列索引在生产上**从未存在过** ——
--   614 @ 6973a9fb8（= 生产 schema_migrations 记账时刻 2026-08-29 16:48:05
--   那一版）把它定义成带 `WHERE ts > now() - interval '8 hours'` 谓词的
--   partial index，而 now() 非 IMMUTABLE ⇒ PG 拒绝 ⇒ 该索引从未建成，
--   且它后面同文件的 idx_session_bodies_hot_partition_date 也从未执行。
--   ⇒ 本迁移（898）把它**首次**建到生产上 ⇒ down 删掉它 ⇒
--   ⇒ 回到「批次选取 Seq Scan + Sort」的形态（2026-10-23 实测：
--   session_bodies_hot 批次选取 Seq Scan + Sort cost 1623，
--   对照组 session_turns_hot 走 Index Only Scan cost 385）。
--
--   这不是缺陷，是如实登记：down 的语义 = 「回到 898 之前的状态」。
--
-- ★ 为什么用 CONCURRENTLY：session_bodies_hot 是活写表
--   （bodies_writer 持续写入）⇒ 普通 DROP INDEX 会持 ACCESS EXCLUSIVE
--   阻塞写入 ⇒ 用 CONCURRENTLY（888 第 4 段同款）。
--
-- ★ 为什么需要 no-transaction 标记：CONCURRENTLY 不能在事务块内执行。

-- dbinit:no-transaction —— 本文件含 DROP INDEX CONCURRENTLY。

-- ★ 等锁保护（★ installer 的 startup_ddl_lock_guard_gate_test.go 的硬要求：
-- ★ >cutoff 的强锁 DDL 迁移必须带 lock_timeout）。
-- ★ ★ 会话级而非 SET LOCAL：★ 本文件走**非事务通道**（no-transaction 标记）
-- ★ ★ ⇒ 没有事务块 ⇒ SET LOCAL 无意义（★ 877 同款）。
SET lock_timeout = '60s';
SET statement_timeout = '10min';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_bodies_hot_ts;

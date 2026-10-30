-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY（PG 不允许在事务块内执行），installer 的 applySQL 据此走非事务通道（见 installer/internal/dbinit/runner.go noTransactionMarker，887/888/729/916/920 同款）。无该标记时全新安装会失败于 "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
-- ===========================================================================
-- File:          sql/migrations/startup/924_credential_probe_queue_command_key_idx.sql
-- Migration:     924
-- Database:      llm_gateway
-- Purpose:       给 credential_probe_queue 补 (probe_command, tenant_id,
--                credential_id, raw_model, created_at) 复合索引，承接
--                integrity_probe_planner 的「未决完整性事件 → 入队去重」反连接。
--
-- ── 缺口本体（2026-10-30 R184 轮，252 生产实测，非推断）────────────────────
-- 252 SQL 日志 24h 窗（10-29 06:03 → 10-30 06:00）：bg/integrity_probe_planner.go
-- cycle() 的 WITH pick 查询（DISTINCT ON + NOT EXISTS credential_probe_queue +
-- EXISTS bindings）：
--   · 慢查 132 次 / 1,369.7s（均值 10.38s / max 29.8s），每小时恒定 ~6 次
--     （= 10min tick 全数上榜），全天无峰谷 = 内在成本而非连带受害
--   · 慢查榜第十大族（占全窗 51,326s 的 ~2.7%）
--
-- 为什么慢：NOT EXISTS 臂按 (tenant_id, credential_id, raw_model,
-- probe_command, created_at>=now()-dedup窗口) 找「近 24h 已入队的任务」，
-- 而 credential_probe_queue 上**没有任何一列前导匹配的索引** —— 7 条既有
-- 索引要么 pkey(id)、要么 partial 于 status（claim/expiry/lease/dedup），
-- 反连接只能走 Merge Anti Join 全量过队：252 EXPLAIN 实测单次
-- **15,455 buffers**（queue 全表 267,805 行、自 2026-08-13 起只增不清：
-- expired 111,150 + success 98,874 + failed 57,340），而产出 = **0 行**
--（792 个可 pick 事件全部已有 24h 内的队列任务 = 去重正常工作，成本纯浪费）。
-- 生产均值 10.38s ≫ EXPLAIN 的亚秒 = 冷缓存 + 4 核宿主排队放大。
--
-- ── 形态选择 ─────────────────────────────────────────────────────────────
--   btree (probe_command, tenant_id, credential_id, raw_model, created_at)
--
--   · NOT EXISTS 的全部四个等值键 + 一个范围键按序入键 ⇒ 关联子查询可走
--     Index Only 形态的逐行探针（外层 792 行 ⇒ 792 次索引 seek，每次只触
--     「同命令同对象近 24h」的少量行）
--   · 不做 partial（created_at >= now()-X 是动态界，partial 谓词必须是
--     常量）；非 partial 的代价 = 索引随 queue 增长（~267k 行 ≈ 25MB 量级）
--   · credential_probe_queue 是普通表（非分区）⇒ 单条 CONCURRENTLY 即可；
--     队列 DML（入队/claim/complete）持续在跑 ⇒ 必须 CONCURRENTLY
--
-- ── 不在本条处理（如实登记）────────────────────────────────────────────
--   queue 终态行（expired/success/failed）无 TTL 清扫，267k 行只增不清
--   ⇒ 本索引把反连接成本从「全量过队」降到「按对象 seek」，但表本身的
--   无界增长是独立的属主决策项（终态行 TTL 需要评估 probe dashboard 的
--   历史读面，未在本轮立项）。
-- ===========================================================================

-- ── 锁守卫（★ 会话级 SET，不是 SET LOCAL：本通道 psql -f 逐文件执行、
--    不在事务块内，SET LOCAL 只打 WARNING 后完全失效 —— 914/916/920 文件头实测）
SET lock_timeout = '2s';
SET statement_timeout = '30min';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_credential_probe_queue_command_key_created
    ON public.credential_probe_queue (probe_command, tenant_id, credential_id, raw_model, created_at);

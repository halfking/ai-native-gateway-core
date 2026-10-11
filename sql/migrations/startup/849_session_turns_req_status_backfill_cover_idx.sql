-- ===========================================================================
-- File:          sql/migrations/startup/849_session_turns_req_status_backfill_cover_idx.sql
-- Migration:     849
-- Database:      llm_gateway
-- Purpose:       给 session_turns 补覆盖式部分索引 (request_id)
--                WHERE request_status IS NULL，让 remaining-count gauge 的
--                t 侧走 index-only，消除 30s 角色级击杀
--
-- Status:        active
-- Idempotent:    YES (CREATE INDEX IF NOT EXISTS)
-- Dependencies:  823（session_turns.request_status 列）、846（同谓词 (id)
--                部分索引——两索引服务不同查询形状，见 Background）
--
-- Background:
--   2026-10-10 252 PG SQL 日志审计第 31 轮
--   (docs/audit/2026-10-10-252-sql-log-audit-round31.md)。
--   domains/session/v2/session_request_status_backfill.go 的
--   sessionRequestStatusRemainingSQL（D9 clause-4 退役 release gate 的
--   可执行形式，**故意不带窗口**、覆盖父表+_hot 两个存储面）：
--     SELECT count(*) FROM session_turns t
--       JOIN request_logs l ON l.request_id = t.request_id
--      WHERE t.request_status IS NULL AND l.request_status IS NOT NULL
--   在 252 部署 846（10-09 10:00）之后仍是主要击杀源：24h 窗 30s 击杀
--   ~60 次（~3/h）+ parallel worker 陪葬 152 次（max_parallel_workers_
--   per_gather=2 × 被杀 leader）。gauge 永远拿不到值（-1 = 无法测量），
--   D9 release gate 事实上不可用。
--
--   击杀根因（252 EXPLAIN 实证）：846 的 (id) 部分索引不覆盖 JOIN 所需的
--   request_id，规划器只能退回 per-partition (id) btree + 逐行 heap 回表，
--   再对每行做 request_logs 全部月分区（~5 个）的索引探测——79.3 万行 ×
--   5 分区 ≈ 4M 次探测，30s 必杀。本迁移把 request_id 纳入部分索引：
--   t 侧翻成 index-only（79.3 万索引条目，无需回表），探测次数不变但
--   每次探测的数据准备成本大幅下降；配合代码侧同批落地的 SET LOCAL
--   statement_timeout 抬升（60s 预算内，与 REFRESH 家族 180s 钉、
--   BackfillOnce 120s 钉同一先例系），gauge 在 252 预计 10-25s 内可完成。
--
--   与 846 的分工：846 服务候选捞取（keyset 按 id 序扫 NULL 行），
--   本索引服务 gauge（按 request_id 投影 JOIN）。同谓词不同列集，
--   两个部分索引各自承重，不互替。
--
-- ⚠️ 上线代价：session_turns 是分区父表（6 分区），分区父表拒绝
--   CREATE INDEX CONCURRENTLY（R46/843/846 先例），故用普通 CREATE INDEX——
--   父级与全部分区取 ShareLock：写被阻塞、读照常。79.3 万 NULL 行入索引
--   （252 预估 ~25-35MB），秒到十秒级，随部署窗口应用。
--   注：本机库验证时 PG 会把既有等价 per-partition 索引 ATTACH 收编而非
--   新建（R30 纪律 73）；252 无同谓词子索引，本迁移为全新建。
-- ===========================================================================
BEGIN;

-- 252 共享 PG 的 statement_timeout=30s 在部署链上直接击杀本迁移
-- （2026-10-10 实录：245 deploy-2517 在此处 57014 canceling statement due to
-- statement timeout 失败并中止切换）。6 分区父表普通 CREATE INDEX 持 ShareLock
-- 建 ~79.3 万条索引项，在共享库负载下超过 30s 属预期——按 649/834/836/837
-- 同一先例系把预算钉在本事务内。
SET LOCAL statement_timeout = '10min';

CREATE INDEX IF NOT EXISTS idx_session_turns_req_status_backfill_cov
    ON public.session_turns (request_id)
    WHERE request_status IS NULL;

COMMIT;

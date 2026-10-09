-- ===========================================================================
-- File:          sql/migrations/startup/846_session_turns_req_status_backfill_idx.sql
-- Migration:     846
-- Database:      llm_gateway
-- Purpose:       给 session_request_status 回填 worker 的候选捞取查询补
--                部分索引 (id) WHERE request_status IS NULL，
--                消除积压清零后 idle 空扫的 30s 击杀
--
-- Status:        active
-- Idempotent:    YES (CREATE INDEX IF NOT EXISTS)
-- Dependencies:  823（session_turns.request_status 列，两存储面同批加列）
--
-- Background:
--   2026-10-09 252 PG SQL 日志审计第 30 轮
--   (docs/audit/2026-10-09-252-sql-log-audit-round30.md)。
--   domains/session/v2/session_request_status_backfill.go 的
--   sessionRequestStatusCandidateSQL（keyset 分页捞 NULL 标签行）在 252 实测
--   24h 窗 30s statement_timeout 击杀 103 次 ≈ 2 实例 × 每 ~28 分钟一次，
--   与 MaxIdle=30min 精确吻合。DB 侧可回填水位已为 0（48h 窗 36,912/36,912
--   已回填），worker 数据面无事可做，空扫成为纯开销（~3,090s/天 DB 墙钟）。
--
--   击杀根因链（252 实测）：session_turns 有 793,519 行 request_status IS NULL
--   ——全部是 823 之前的 v1 历史行（partition_date ≤ 2026-09-30，request_logs
--   源行已被 TTL 清除，结构性不可回填，823 头注明文 No backfill）。候选查询
--   证明「无候选」必须从 id=0 走完 pkey 全程，逐行 JOIN 探测这 79.3 万行，
--   >30s 必杀。本查询同时是 R30 的代码修复对象：候选 SELECT 现带
--   `partition_date >= (now() - interval '7 days')::date` 滚动下界（可回填
--   候选的存在窗口 = 823 后写入 + promote 保留期 8h，小时级，7 天为 ~21 倍
--   系数；窗外 = 永久 NULL 历史行），把空扫裁剪到当月分区的零星 NULL 行。
--
--   本索引在该形态下的价值：部分索引只含 NULL 行，与窗口谓词叠加后，
--   当月分区的候选定位（有积压）与无候选证明（空索引即刻返回）都走
--   index-only；2026_10/11 分区 NULL 行 ≈ 0，空扫 = 读空索引。
--   822（session_summaries health_score IS NULL 部分索引）同构先例。
--
-- ⚠️ 上线代价：session_turns 是分区父表（6 分区，2026_09 79.8 万 +
--   2026_10 5.9 万行），`CREATE INDEX CONCURRENTLY` 在分区父表上直接被拒
--   （R46/843 先例：ERROR: cannot create index on partitioned table
--   concurrently），故用普通 CREATE INDEX —— 父级与全部分区取 ShareLock：
--   写被阻塞、读照常。partial 谓词评估需扫全行（79.3 万 NULL 全部进索引，
--   252 预估索引 ~25-35MB），秒到十秒级，随部署（维护）窗口应用，观察
--   pg_stat_progress_create_index。
--   注：per-partition 形态的同谓词部分索引在本机库早已存在（六个分区各自的
--   session_turns_2026_XX_id_idx 即 `(id) WHERE request_status IS NULL`）——
--   252 与本机 schema 有漂移，本迁移把该形态在 252 补齐并统一到父表管理。
-- ===========================================================================

BEGIN;

CREATE INDEX IF NOT EXISTS idx_session_turns_req_status_backfill
    ON public.session_turns (id)
    WHERE request_status IS NULL;

COMMIT;

COMMENT ON INDEX idx_session_turns_req_status_backfill IS
    '846: session_request_status 回填候选查询的承重部分索引 —— 只含待回填 NULL 行；积压清零后 idle 空扫由 pkey 全扫(>30s 击杀)降为空索引毫秒级证明。行回填后即离开索引。';

-- Migration 904: 给 inspector-stats 的两条「恒扫全表」聚合腿补部分索引。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 全部为 2026-10-27 生产只读实测，结论可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【症状】GET /api/admin/sessions/inspector-stats 生产偏慢。
--   154 直连 REPEAT=5（build 2839）：min 2.411s / med 3.989s / max 6.160s。
--   三条聚合腿**都没有时间窗** ⇒ 都是全表聚合：
--     L1 (admin/session_health_api.go:676-685) session_summaries ⋈ session_dim
--     L2 (admin/session_health_api.go:694-698) AVG(health_score) WHERE NOT NULL
--     L3 (admin/session_health_api.go:732-737) COUNT(*) WHERE status='closed'
--
-- 【★ 根因：L2 与 L3 的谓词各自都没有索引可承接】
--   L3 的谓词是 `status = 'closed' AND closed_at >= CURRENT_DATE`，
--     而 session_dim 的 7 条既有索引里**没有一条**以 (status, closed_at)
--     或任何含 closed_at 的组合开头 ⇒ 规划器只能
--     `Parallel Seq Scan on session_dim` + Filter ⇒ 把 90 万行全读一遍
--     再丢掉 100%（实测 Rows Removed by Filter: 301,024）。
--   L2 的谓词是 `health_score IS NOT NULL`，而 session_summaries 的 19 条
--     既有索引里，只有 `idx_session_summaries_health_pending
--     (last_request_at DESC) WHERE health_score IS NULL` —— 那是
--     **NULL 侧**的索引；**NOT NULL 侧没有对应索引**
--     ⇒ 规划器只能 `Parallel Seq Scan on session_summaries` + Filter
--     ⇒ 读完 86 万行丢掉 55 万行（实测 Rows Removed by Filter: 184,361）。
--
-- 【★ 修复效果（252 生产库：事务内 CREATE INDEX → EXPLAIN ANALYZE →
--    ROLLBACK ⇒ 生产零持久变更；回滚后残留索引数实测 = 0）】
--   腿                          修复前        修复后（热）      计划形态变化
--   ────────────────────────────────────────────────────────────────────
--   L2 AVG(health_score)        389.373 ms → **119.706 ms**   Parallel Seq Scan
--                                                            → Parallel Index
--                                                              Only Scan using
--                                                              idx_session_summaries_
--                                                              health_score_notnull
--   L3 COUNT(*) closed          195.432 ms → **0.100 ms**     Parallel Seq Scan
--                                                            → Index Only Scan using
--                                                              idx_session_dim_
--                                                              closed_at_closed
--   L1 JOIN 聚合              1,615 ms    → 1,401 ms         不变（噪声带内）
--
--   索引体积（实测）：idx_session_dim_closed_at_closed = **8,192 B**；
--                     idx_session_summaries_health_score_notnull = **2,120 kB**
--   ⇒ 两条合计 ≈2.1 MB，换掉两条全表聚合腿。
--
-- 【★ 口径零变化（本迁移不改任何查询）】
--   本迁移**只加索引，不改任何一行 SQL**。索引只影响规划器的计划选择，
--   不影响结果集的取值 ⇒ 与 903 的「改写查询后逐字比对」不同，
--   本迁移的口径零变化是**构造性成立**的，不依赖快照比对。
--
-- 【★ 诚实边界：L2 还差一步，但这一步是生产写操作，本迁移不做】
--   L2 的 Index Only Scan 实测 Heap Fetches: 86,938 ⇒ 可见性地图
--   （relallvisible = 37,826 / relpages = 50,660 = 74.6%）尚未覆盖全部页
--   ⇒ 约 28% 的行仍需回堆。若对 public.session_summaries 做一次
--   VACUUM (ANALYZE)，Heap Fetches 可降到 0，L2 可进一步下降。
--   ★ VACUUM 属生产写操作 ⇒ 不在本迁移内（见同轮准备件
--   sql/ops/vacuum_session_family_vm.sql，另行授权）。
--
-- 【★ 诚实边界二：C1 索引当前是空的，而这是「被证实的空」不是「建错了」】
--   252 生产实测：session_dim 的 status **distinct = 1**（全表只有 'active'）、
--   closed_at **100% NULL** ⇒ `status = 'closed'` 当前命中 0 行
--   ⇒ 该索引现在是空索引（8,192 B）。
--   ★ 但这**恰恰是它存在的理由**：当前 L3 每次都要全表扫 90 万行才能
--   证明「一行都没有」；加了索引后，证明这件事只要 0.1 ms。
--   ⇒ 收益形态 = 「把恒空的谓词从『全表扫』降级为『索引扫』」，
--   而不是「把有序扫描变快」。
--   ★ 根因不在索引：会话**关闭**路径当前是坏的 ——
--   admin/session_health_api.go 的 HandleSessionRecycle 与
--   bg/session_lifecycle_worker.go:496-500 都 UPDATE 了
--   public.session_dim 上**并不存在**的列（stop_reason / updated_at），
--   真库上会 42703 ⇒ 所以 status 永远停在 'active'、closed_at 永远 NULL。
--   ⇒ 那 4 个「恒 0 字段」（idle_sessions / closed_sessions /
--   recycled_today / findings_last_1h）的真因在此，**不在索引**。
--
-- 【锁与时长】
--   public.session_dim（206 MB / 90 万行）与 public.session_summaries
--   （396 MB / 86 万行）都是**热写表**（session_dim 由
--   internal/sessionv2mirror/session_dim.go 写、session_summaries 由
--   domains/sessionforensics/export.go 等写）⇒ 普通 CREATE INDEX 构建期
--   持 SHARE 锁、阻塞该表写入。
--   ★ 两张表都**不是分区表**（pg_class.relkind='r' 且 pg_inherits 无子行）
--   ⇒ 与 903 的分区父表不同，**可以用普通 CREATE INDEX**（仍不用
--   CONCURRENTLY：startup 迁移默认在事务里跑，CONCURRENTLY 不能在
--   事务块内执行）。
--   · lock_timeout=60s：抢不到锁快速失败、整体回滚、稍后重跑
--     （882/876/891/901/903 同款）
--   · statement_timeout=10min：共享 PG 服务端 statement_timeout 30s
--     会掐断索引构建，57014 击穿后迁移永不记账，成为永久卡点
--
-- ★ 【实测时长与锁窗口（252 生产，2026-10-27，真实文件体 + ROLLBACK）】
--   C1 idx_session_dim_closed_at_closed            CREATE INDEX  **3.18 s**
--   C2 idx_session_summaries_health_score_notnull  CREATE INDEX  **6.68 s**
--   ⇒ 合计 ≈ **10 s** ⇒ ★ 这 10 秒里 session_dim / session_summaries 的
--     **写入被阻塞**（SHARE 锁）⇒ 迁移应落在低峰窗口。
--   ⇒ 该 10 s 远小于角色级 statement_timeout=30s ⇒ 即使 SET LOCAL 失效
--     也不会被掐断；实测 SET LOCAL statement_timeout='10min' 生效。
--   ⇒ 部署通道的失败语义（实测，非推断）：deploy-lib 用
--     `psql -v ON_ERROR_STOP=1 -v ON_ERROR_ROLLBACK=1 -f <file>`
--     ⇒ 实测「事务内出错」时 psql **退出码 = 3 且事务回滚**
--     ⇒ **fail-closed** ⇒ 不会出现「迁移失败却被记账成已应用」。
--
-- 【幂等】IF NOT EXISTS。Down：按名删两条索引。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

-- C1：L3（今日关闭数）的谓词索引。
--   谓词 status = 'closed' 必须与 admin/session_health_api.go 的
--   `WHERE sd.status = 'closed'` 逐字对应 —— 由
--   migration_904_inspector_stats_partial_idx_test.go 的语义耦合门钉住。
CREATE INDEX IF NOT EXISTS idx_session_dim_closed_at_closed
    ON public.session_dim (closed_at)
    WHERE status = 'closed';

-- C2：L2（平均健康分）的谓词索引。
--   注意与既有 idx_session_summaries_health_pending
--   (last_request_at DESC) WHERE health_score IS NULL 是**互补**关系
--   （NULL 侧 vs NOT NULL 侧）⇒ 不是冗余索引。
CREATE INDEX IF NOT EXISTS idx_session_summaries_health_score_notnull
    ON public.session_summaries (health_score)
    WHERE health_score IS NOT NULL;

COMMIT;

-- POST_CONDITION:
--   SELECT indexname, pg_size_pretty(pg_relation_size(indexname::regclass))
--    FROM pg_indexes
--    WHERE schemaname = 'public'
--      AND indexname IN ('idx_session_dim_closed_at_closed',
--                         'idx_session_summaries_health_score_notnull');
--   -- 预期：8192 bytes / 2120 kB（2026-10-27 实测）
--   -- 预期收益（EXPLAIN 口径，2026-10-27 实测）：
--   --   L2 AVG(health_score) 389.373 ms → 119.706 ms
--   --   L3 COUNT(*) closed      195.432 ms →   0.100 ms

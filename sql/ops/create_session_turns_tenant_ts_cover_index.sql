--
-- create_session_turns_tenant_ts_cover_index.sql
--   `/api/admin/turns/sessions/filter-options` 的 **turns 臂** 的覆盖索引（★ 待属主放行 ★）
--
-- 2026-10-27 审计轮（R87/R88）的实测结论：
--   · 该端点只有 **3 条串行 SQL 腿**（ss 8s / tags 2s / turns 8s 子预算）。
--   · 端点 med 10.2s 的 **76~78%** 是 **turns 臂打满 8s 子预算后被丢弃的 8 秒**（纯浪费）。
--   · turns 臂本身的主导成本在 **`public.session_turns_2026_10` 的 Seq Scan**：
--     305,313 行 / **55,185 buffers（占该臂 64%）**；而 9 月分区走索引只要 29,630 buffers。
--     ⇒ **30 天窗滚动后主导扫描从 9 月移到了 10 月**（R86 当时测的是 9 月，已过期）。
--
-- ★ 实测收益（252 上 `BEGIN … ROLLBACK` 内建索引的 A/B，2026-10-27）：
--     turns 臂 10 月分区节点 buffers **55,185 → 6,244（−88.7%）**
--     整臂 buffers           **86,421 → 35,222（−59%）**
--     热态墙钟              **1,311.6 → 581.3 ms（−56%）**
--     索引体积              **18 MB/分区**
--
-- ★★ **关键设计决定：索引建在【父表】上，不是逐分区建**
--   实测（252）：`public.session_turns` 父表上**已有 25 个索引**（含 `idx_session_turns_tenant (tenant_id, ts DESC)`），
--   而新建月分区是由 `ensure_sessions_v2_partitions(target_date)` 这个 SQL 函数创建的
--   ⇒ ★ **PostgreSQL 会自动把父表上的索引传播到新建分区** ⇒
--   ★★ **建在父表上就自动「随月滚动」，不需要任何额外的分区滚动改造**
--   （⚠ 这一点纠正了 R87 审计档里「须随月分区滚动」的说法：逐分区建才需要滚动改造，父表建不需要。）
--
-- ★★ **代价与放行要点**：
--   · 建索引期间对**每个分区**取 SHARE 锁 ⇒ **会短暂阻塞该分区的写入** ⇒ ★ 需维护窗口。
--   · ★ **不能**做成「启动迁移」：启动迁移默认在事务里跑 ⇒ 整段建索引期间锁不释放 ⇒ 不可接受。
--     ⇒ 本文件是**运维动作**，由属主放行后在维护窗口执行。
--   · 总耗时估算：2026_10 单分区建索引实测 **848 ms**；全库 6 个分区 + 父表 ⇒ **数秒级**
--     （★ 精确值未实测：以本机实测为准，实际执行前请先跑 ① 前置盘点）。
--
-- ★★ **顺带登记的冗余索引**：`session_turns_2026_09` 上有**两条同列索引**
--     `session_turns_2026_09_heap_tenant_id_ts_idx`（迁移 872 列存→heap 时逐分区重建）
--     + `session_turns_2026_09_tenant_id_ts_idx`（从父表继承）⇒ **约 42 MB 冗余**
--     ⇒ ★ **本脚本不删任何索引**（删索引需要全量 usage 分析，另行裁决）；
--        这里只做登记，供 `/etc/cron.d/pg17` 里的 `pg17-index-bloat.sh` 周级作业参考。
--

\set ON_ERROR_STOP on

-- ═══════════════════════════════════════════════════════════════════════════
-- ① 前置盘点（执行前必看：将被索引的分区、大小、现有同列索引）
-- ═══════════════════════════════════════════════════════════════════════════
\echo '=== ① 将被索引的分区（父表索引会传播到全部现有分区）==='
SELECT c.relname AS partition,
       c.reltuples::bigint AS est_rows,
       pg_size_pretty(pg_relation_size(c.oid)) AS heap_size,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
       (SELECT count(*) FROM pg_index i WHERE i.indrelid = c.oid) AS idx_count,
       EXISTS (SELECT 1 FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
                WHERE i.indrelid = c.oid
                  AND ic.relname LIKE '%tenant_id_ts_idx%') AS has_tenant_ts_idx
FROM pg_class c
JOIN pg_inherits h ON h.inhrelid = c.oid
WHERE h.inhparent = 'public.session_turns'::regclass
  AND c.relkind = 'r'
ORDER BY 1;

\echo ''
\echo '=== ①b 同列冗余索引登记（不删，仅登记）==='
SELECT c.relname AS partition, ic.relname AS redundant_index,
       pg_size_pretty(pg_relation_size(ic.oid)) AS size
FROM pg_class c
JOIN pg_inherits h ON h.inhrelid = c.oid
JOIN pg_index i ON i.indrelid = c.oid
JOIN pg_class ic ON ic.oid = i.indexrelid
WHERE h.inhparent = 'public.session_turns'::regclass
  AND ic.relname LIKE '%heap_tenant_id_ts_idx%'
  AND EXISTS (SELECT 1 FROM pg_index i2 JOIN pg_class ic2 ON ic2.oid = i2.indexrelid
               WHERE i2.indrelid = c.oid AND ic2.relname LIKE '%tenant_id_ts_idx%')
ORDER BY 1;

-- ═══════════════════════════════════════════════════════════════════════════
-- ② 建索引（★ 需属主放行 + 维护窗口 ★）
--    ★ 建在父表上 ⇒ 自动传播到全部现有分区与未来新分区
-- ═══════════════════════════════════════════════════════════════════════════
\echo ''
\echo '=== ② 建覆盖索引（父表级，自动滚动）==='

-- ★ 锁与超时：拿不到锁就快速失败，不阻塞在线写入
SET lock_timeout = '5s';
-- ★ 建索引本身可能超过默认 statement_timeout（30s）⇒ 放开；但仍设一个上限
SET statement_timeout = '600s';

-- ★ vacuum 成本：建索引时对 IO 友好
SET maintenance_work_mem = '256MB';

CREATE INDEX IF NOT EXISTS idx_session_turns_tenant_ts_cover
    ON public.session_turns (tenant_id, ts DESC)
    INCLUDE (model, provider, status_code);

RESET lock_timeout;
RESET statement_timeout;

-- ═══════════════════════════════════════════════════════════════════════════
-- ③ 执行后核验：每个分区都应有该索引且 indisvalid
-- ═══════════════════════════════════════════════════════════════════════════
\echo ''
\echo '=== ③ 核验：分区级索引落地与有效性 ==='
SELECT c.relname AS partition,
       EXISTS (SELECT 1 FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
                WHERE i.indrelid = c.oid
                  AND ic.relname = 'session_turns_' || right(c.relname, 7) || '_tenant_ts_cover_idx') AS has_cover_idx,
       (SELECT count(*) FROM pg_index i WHERE i.indrelid = c.oid AND NOT i.indisvalid) AS invalid_idx,
       (SELECT pg_size_pretty(pg_relation_size(ic.oid))
          FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
         WHERE i.indrelid = c.oid AND ic.relname = 'session_turns_' || right(c.relname, 7) || '_tenant_ts_cover_idx') AS cover_size
FROM pg_class c
JOIN pg_inherits h ON h.inhrelid = c.oid
WHERE h.inhparent = 'public.session_turns'::regclass
  AND c.relkind = 'r'
ORDER BY 1;

-- ═══════════════════════════════════════════════════════════════════════════
-- ④ 计划核验：turns 臂应从 Seq Scan 变成 Index Only Scan
--    ★ EXPLAIN ANALYZE 会真的执行 ⇒ 生产上请用 EXPLAIN（不加 ANALYZE）
-- ═══════════════════════════════════════════════════════════════════════════
\echo ''
\echo '=== ④ 计划核验（EXPLAIN，不执行）==='
EXPLAIN (COSTS OFF)
SELECT model, provider, status_code::text, ts
FROM public.session_turns
WHERE ts > NOW() - INTERVAL '30 days' AND tenant_id = 'default';

-- ★★ 本文件不在任何部署序列中（生产写操作，需属主放行）。

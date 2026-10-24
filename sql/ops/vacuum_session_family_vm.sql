--
-- vacuum_session_family_vm.sql —— 会话族【冷分区】的「可见性地图」感知维护（★ 待属主放行 ★）
--
-- 2026-10-27 审计轮（R87/R88）发现的问题：
--   `/api/admin/data-lifecycle/stats` 的腿1 扫描 `session_turns_2026_09`（798,278 行 / 1.1 GB）
--   要 **6,444 ms**，而它的兄弟分区 `session_turns_2026_10`（305,325 行）走**同一形状的索引**
--   只要 **95 ms** ⇒ **每行成本差 26×**。
--
--   根因**不是缺索引**：两条分区都有 `(tenant_id, ts DESC)` 索引（2026_09 甚至有两条同列索引：
--   `session_turns_2026_09_heap_tenant_id_ts_idx` + 从父表继承的 `session_turns_2026_09_tenant_id_ts_idx`），
--   但 **`session_turns_2026_09.relallvisible = 0`**（可见性地图为空，**从未 vacuum**）
--   ⇒ Index Only Scan 不可用 ⇒ 规划器只能选 1.1 GB 顺序读。
--
-- ★★ **结构性缺口**：库内所有维护都以「死元组比例」为判据
--   （autovacuum 的 20% 阈值、`/etc/cron.d/pg17` 里 `pg17-vacuum-bloat.sh` 的 `dead_pct >= 30 AND size > 256MB`、
--    迁移 839 的 ANALYZE 名单），而 `session_turns_2026_09` 的 `n_dead_tup = 4394 / n_live_tup ≈ 798,278`
--   ⇒ **dead_pct ≈ 0.55%** ⇒ ★ **永远不会被任何现有作业选中** ⇒ **可见性地图永远不会自愈**。
--   （冷月分区无新插入 ⇒ autovacuum 也不会因为「插入行数」而触发。）
--
-- ★ 本脚本的判据是**可见性地图可见率**，与「死元组比例」正交 ⇒ 补上这块缺口。
--
-- ── 安全设计（为什么可以用普通 VACUUM 而不是 VACUUM FULL）──
--   · 普通 `VACUUM (ANALYZE)` 取 **SHARE UPDATE EXCLUSIVE** 锁 ⇒ **不阻塞 SELECT / INSERT / UPDATE**；
--     `VACUUM FULL` 会取 ACCESS EXCLUSIVE + 整表重写 ⇒ 对 1.1 GB 的表代价大且危险 ⇒ **不采用**。
--   · `lock_timeout` / `statement_timeout` 兜底 ⇒ 拿不到锁就放弃这一张，不拖累其它表。
--   · `vacuum_cost_delay` 抬高 ⇒ 与在线负载错峰（IO 友好）。
--   · ★ **默认只打印候选，不执行** ⇒ 执行需显式把 `DO_EXECUTE` 改成 `EXECUTE`（见下）。
--
-- ── 用法 ──
--   ① 先干跑（只看候选）：  psql -X -U llm_gateway -d llm_gateway -f vacuum_session_family_vm.sql
--   ② 确认候选无误后再执行：psql ... -v execute_vacuum=true -f vacuum_session_family_vm.sql
--   ③ 建议 cron：每周一次、错峰（252 上 /etc/cron.d/pg17 已有 03:15 bloat / 04:40 index-bloat /
--      05:10 duplicate-index / 02:30 drop-old-columnar / 02:00 cleanup 五个作业 ⇒ **建议 05:40**）。
--
-- ★★ **本文件不在任何部署序列中**：它是**运维动作**（生产写操作），不是 schema 迁移。
--    ★ 待属主放行后再决定：① 作为受控运维步骤执行；② 还是登记成启动迁移（⚠ 启动迁移默认在事务里跑，
--    而 VACUUM 不能在事务块内执行 ⇒ **不能**做成启动迁移 ⇒ 只能走运维路径）。
--

\set ON_ERROR_STOP on
\timing off

-- ═══════════════════════════════════════════════════════════════════════════
-- ① 候选清单（干跑时这就是全部输出）
-- ═══════════════════════════════════════════════════════════════════════════
\echo '=== 候选：会话族冷分区（可见性地图可见率 < 50%，或 relallvisible = 0）==='

SELECT
    c.relname                                        AS partition,
    c.reltuples::bigint                              AS est_rows,
    c.relpages                                       AS relpages,
    c.relallvisible                                  AS relallvisible,
    CASE WHEN c.relpages = 0 THEN NULL
         ELSE round(100.0 * c.relallvisible / c.relpages, 2) END AS visible_pct,
    s.n_live_tup                                     AS live_tup,
    s.n_dead_tup                                     AS dead_tup,
    s.last_vacuum                                   AS last_vacuum,
    s.last_autovacuum                               AS last_autovacuum,
    s.autovacuum_count                              AS autovacuum_count,
    pg_size_pretty(pg_total_relation_size(c.oid))   AS total_size
FROM pg_class c
JOIN pg_inherits h  ON h.inhrelid = c.oid
LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
WHERE h.inhparent IN ('public.session_turns'::regclass,
                      'public.session_bodies'::regclass,
                      'public.sessions'::regclass)
  AND c.relkind = 'r'
  AND c.relpages > 1000                              -- 忽略小分区（可见率噪声大）
  -- ★ 核心判据：可见性地图可见率低 ⇒ Index Only Scan 不可用
  AND (c.relallvisible = 0
       OR (c.relpages > 0 AND c.relallvisible::numeric / c.relpages < 0.5))
  -- ★ 只管「冷分区」：当月分区还在写入，autovacuum 会照管，避免与在线负载抢 IO
  AND c.relname ~ '_\d{4}_\d{2}$'
  AND to_date(right(c.relname, 7), 'YYYY_MM') < date_trunc('month', CURRENT_DATE)::date
ORDER BY c.relname;

-- ═══════════════════════════════════════════════════════════════════════════
-- ② 执行（★ 默认 dry_run=true ⇒ 不执行 ★）
-- ═══════════════════════════════════════════════════════════════════════════
-- 用法：psql ... -v dry_run=false -f vacuum_session_family_vm.sql
\if :{?execute_vacuum}
\else
  \set execute_vacuum false
\endif

\echo ''
\echo '=== 执行阶段（execute_vacuum =' :execute_vacuum '）==='

-- ★★ **默认不执行**（execute_vacuum 默认 false）⇒ 干跑时只打印候选。
-- ★ 真要执行：psql ... -v execute_vacuum=true -f vacuum_session_family_vm.sql
\if :execute_vacuum
  -- VACUUM 不能在事务块内执行 ⇒ 必须由 psql 的 \gexec 在「每条语句独立提交」下逐条下发。
  -- ⚠ \gexec 会把 SELECT 的每一行结果当作一条语句执行 ⇒ 下面这个 SELECT 必须只返回 VACUUM 语句。
  SET lock_timeout = '5s';
  SET statement_timeout = '600s';
  SET vacuum_cost_delay = '20ms';
  SELECT format('VACUUM (ANALYZE, VERBOSE) public.%I;', c.relname)
  FROM pg_class c
  JOIN pg_inherits h  ON h.inhrelid = c.oid
  WHERE h.inhparent IN ('public.session_turns'::regclass,
                        'public.session_bodies'::regclass,
                        'public.sessions'::regclass)
    AND c.relkind = 'r'
    AND c.relpages > 1000
    AND (c.relallvisible = 0
         OR (c.relpages > 0 AND c.relallvisible::numeric / c.relpages < 0.5))
    AND c.relname ~ '_\d{4}_\d{2}$'
    AND to_date(right(c.relname, 7), 'YYYY_MM') < date_trunc('month', CURRENT_DATE)::date
  ORDER BY c.relname
  \gexec
\else
  \echo '（干跑：未执行任何 VACUUM。放行后用 -v execute_vacuum=true 执行）'
\endif

-- ═══════════════════════════════════════════════════════════════════════════
-- ③ 执行后核验（可见率应显著上升 ⇒ Index Only Scan 重新可用）
-- ═══════════════════════════════════════════════════════════════════════════
\echo ''
\echo '=== 执行后核验：可见率与索引可用性 ==='
SELECT c.relname AS partition,
       c.relpages, c.relallvisible,
       CASE WHEN c.relpages = 0 THEN NULL
            ELSE round(100.0 * c.relallvisible / c.relpages, 2) END AS visible_pct,
       (SELECT count(*) FROM pg_index i
         WHERE i.indrelid = c.oid AND i.indisvalid) AS valid_idx
FROM pg_class c
JOIN pg_inherits h ON h.inhrelid = c.oid
WHERE h.inhparent IN ('public.session_turns'::regclass,
                      'public.session_bodies'::regclass,
                      'public.sessions'::regclass)
  AND c.relkind = 'r'
  AND c.relname ~ '_\d{4}_\d{2}$'
ORDER BY 1;

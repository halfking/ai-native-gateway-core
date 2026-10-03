-- ============================================================================
-- 2026-10-02-db-unused-index-candidates.sql
--
-- 配合审计报告 docs/database/2026-10-02-db-audit-34pg17-and-optimization.md
-- 的 B 档（索引治理）。34 侧实测：117 个非唯一索引 idx_scan=0、合计 5.17 GB。
--
-- 设计原则：**本脚本默认不删任何索引**，只做三件事
--   1) 列出候选，并标注每个候选的「证据强度」
--   2) 提供逐条的人工放行开关（一次只放行一条，便于观察影响）
--   3) 删除时用 DROP INDEX CONCURRENTLY，避免长时间持锁
--
-- 为什么要「逐条放行」而不是一把梭
--   idx_scan=0 只说明**这个统计窗口内**没被用过。以下情况会让一个真正在用的索引
--   显示为 0：
--     * 低频路径（月度任务、季度对账、故障时的人工查询）
--     * 统计刚被重置（34 侧 stats_reset = 2026-09-29）
--     * 另一套代码/另一个服务在用同样的查询
--   所以必须先在 252 上用同样的口径核对，再在 34 上逐条放行、逐条观察。
--
-- 用法
--   -- 第 1 步：出候选清单（只读，不改任何东西）
--   psql -d llm_gateway -v ON_ERROR_STOP=1 \
--        -f sql/audit/2026-10-02-db-unused-index-candidates.sql
--
--   -- 第 2 步：逐条放行。把 index_drop_targets 里对应行的 enabled 改成 true，
--   --         并把 storage_reclaim.index_drop='on' 一起打开。
--   psql -d llm_gateway -v ON_ERROR_STOP=1 \
--        -c "SET storage_reclaim.index_drop='on'" \
--        -f sql/audit/2026-10-02-db-unused-index-candidates.sql
--
-- 回滚：DROP INDEX 无法回滚。删之前务必先记录 pg_get_indexdef（本脚本会打印）。
-- ============================================================================

\pset pager off
SET lock_timeout = '5s';
SET statement_timeout = '120s';

SET storage_reclaim.index_drop = 'off';

\echo '=== 2026-10-02 unused-index candidate scan start ==='

-- ---------------------------------------------------------------------------
-- 1. 候选总览
-- ---------------------------------------------------------------------------
\echo ''
\echo '--- [1] 零扫描非唯一索引总览 ---'

SELECT pg_size_pretty(coalesce(sum(pg_relation_size(ic.oid)),0)) AS reclaimable,
       count(*)                                             AS index_count
FROM pg_stat_user_indexes ui
JOIN pg_index i  ON i.indexrelid = ui.indexrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
WHERE ui.idx_scan = 0
  AND NOT i.indisunique
  AND NOT i.indisprimary
  AND pg_relation_size(ic.oid) > 1048576;

\echo ''
\echo '--- [2] 候选清单（含证据强度判定）---'

SELECT ic.relname AS index_name,
       tc.relname AS table_name,
       pg_size_pretty(pg_relation_size(ic.oid)) AS size,
       s.idx_scan,
       s.idx_tup_read,
       CASE
         -- 该表整体索引使用率极低 -> 整表索引族都可疑
         WHEN EXISTS (SELECT 1 FROM pg_stat_user_indexes x
                       WHERE x.relid = tc.oid AND x.idx_scan > 0)
              AND (SELECT coalesce(sum(idx_scan),0) FROM pg_stat_user_indexes x WHERE x.relid = tc.oid) < 1000
           THEN 'WEAK-整表索引几乎不用'
         WHEN tc.reltuples > 0 AND pg_relation_size(ic.oid) / greatest(tc.reltuples,1) > 800
           THEN 'WEAK-索引体积/行数比异常大'
         ELSE 'NEEDS-REVIEW'
       END AS evidence,
       left(pg_get_indexdef(ic.oid), 110) AS index_def
FROM pg_stat_user_indexes ui
JOIN pg_index i  ON i.indexrelid = ui.indexrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_class tc ON tc.oid = i.indrelid
LEFT JOIN pg_class s ON s.oid = ui.indexrelid
WHERE ui.idx_scan = 0
  AND NOT i.indisunique
  AND NOT i.indisprimary
  AND pg_relation_size(ic.oid) > 1048576
ORDER BY pg_relation_size(ic.oid) DESC;

\echo ''
\echo '--- [3] 主键索引零扫描（单独列，通常更值得怀疑）---'

SELECT ic.relname AS index_name,
       tc.relname AS table_name,
       pg_size_pretty(pg_relation_size(ic.oid)) AS size,
       ui.idx_scan
FROM pg_stat_user_indexes ui
JOIN pg_index i  ON i.indexrelid = ui.indexrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_class tc ON tc.oid = i.indrelid
WHERE ui.idx_scan = 0 AND i.indisprimary AND pg_relation_size(ic.oid) > 1048576
ORDER BY pg_relation_size(ic.oid) DESC;

\echo ''
\echo '--- [4] 每个候选表的其他索引用量（判断是否整族闲置）---'

SELECT tc.relname AS table_name,
       pg_size_pretty(pg_total_relation_size(tc.oid)) AS table_size,
       count(*)                                          AS index_count,
       sum(ui.idx_scan)                                  AS total_idx_scan,
       string_agg(ic.relname || '(' || ui.idx_scan || ')', ', ' ORDER BY pg_relation_size(ic.oid) DESC) AS indexes
FROM pg_stat_user_indexes ui
JOIN pg_index i  ON i.indexrelid = ui.indexrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_class tc ON tc.oid = i.indrelid
WHERE ui.idx_scan = 0
  AND NOT i.indisunique
  AND pg_relation_size(ic.oid) > 1048576
GROUP BY tc.relname, tc.oid
ORDER BY pg_total_relation_size(tc.oid) DESC;

-- ---------------------------------------------------------------------------
-- 2. 逐条放行的删除目标
-- ---------------------------------------------------------------------------
\echo ''
\echo '--- [5] 删除目标（enabled 全为 false，需人工逐条改为 true）---'

CREATE TEMP TABLE index_drop_targets (
  index_name text PRIMARY KEY,
  enabled    boolean NOT NULL DEFAULT false,
  rationale  text
) ON COMMIT DROP;

INSERT INTO index_drop_targets (index_name, rationale) VALUES
  ('idx_state_transitions_journey_recent',
   'request_state_transitions 的 5 个零扫描 journey 索引之一；该表 11 个索引里只有 uq_..._tenant_request_seq(93k) 与 idx_state_transitions_created(280) 被用过'),
  ('idx_state_transitions_journey_model_recent', '同上，journey 族'),
  ('idx_state_transitions_tenant_request',        '与 uq_state_transitions_tenant_request_seq 几乎重复，且未被使用'),
  ('idx_state_transitions_journey_node_recent',   '同上，journey 族'),
  ('idx_state_transitions_request',               '与 uq_state_transitions_legacy_request_seq 功能重叠，且未被使用'),
  ('idx_state_transitions_request_attempt',       'request 族，未被使用'),
  ('route_incident_events 相关的 type_created / incident_created 两个索引', '占位：见下方实际名称'),
  ('request_logs_2026_09_client_model_idx1',      'hash 索引 95MB，idx_scan=0；需与 idx3(GIN) 二选一或都删'),
  ('request_logs_2026_09_client_model_idx3',      'GIN 索引 95MB，idx_scan=0'),
  ('ursm_node_snapshot_min_canonical_name_lower_idx', 'lower() 表达式索引 127MB，idx_scan=0'),
  ('usage_facts_default 的全部索引',                '占位：0 行分区上的 534MB 索引，优先用 TRUNCATE 回收（见 2026-10-02-db-storage-reclaim.sql）')
ON CONFLICT (index_name) DO NOTHING;

-- 上面几行是「占位说明」，真正的删除对象以实际存在的索引为准。
-- 这里用一次精确匹配把占位行剔除，只保留库中真实存在的索引名。
DELETE FROM index_drop_targets d
 WHERE NOT EXISTS (SELECT 1 FROM pg_class c
                    JOIN pg_namespace n ON n.oid = c.relnamespace
                    WHERE n.nspname = 'public' AND c.relname = d.index_name);

SELECT * FROM index_drop_targets ORDER BY index_name;

\echo ''
\echo '--- [6] 统计窗口参考（idx_scan=0 的可信度取决于窗口长度）---'

SELECT 'pg_stat_user_indexes 最早可能的数据起点 ≈ 数据库启动时间 ' ||
       pg_postmaster_start_time()::text ||
       ' | pg_stat_statements stats_reset = ' ||
       (SELECT stats_reset FROM pg_stat_statements_info)
       AS window_note;

-- ---------------------------------------------------------------------------
-- 3. 执行删除（仅对 enabled=true 的行）
-- ---------------------------------------------------------------------------
DO $dropidx$
DECLARE
  v_apply text := current_setting('storage_reclaim.index_drop', true);
  r       record;
  v_done  int := 0;
  v_bytes bigint := 0;
BEGIN
  IF v_apply IS DISTINCT FROM 'on' THEN
    RAISE NOTICE '>>> DRY-RUN：storage_reclaim.index_drop <> ''on''，未删除任何索引。';
    RETURN;
  END IF;

  FOR r IN
    SELECT d.index_name, pg_relation_size(c.oid) AS bytes
    FROM index_drop_targets d
    JOIN pg_class c ON c.relname = d.index_name
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND d.enabled
  LOOP
    -- 删除前再核一次：万一窗口之后被用过了
    IF (SELECT idx_scan FROM pg_stat_user_indexes WHERE indexrelid =
          (SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname='public' AND c.relname = r.index_name)) > 0 THEN
      RAISE WARNING 'SKIP %：统计窗口内已被使用（idx_scan>0），取消删除', r.index_name;
      CONTINUE;
    END IF;

    EXECUTE format('DROP INDEX CONCURRENTLY IF EXISTS public.%I', r.index_name);
    v_done := v_done + 1;
    v_bytes := v_bytes + r.bytes;
    RAISE NOTICE 'DROP INDEX CONCURRENTLY public.%  (% 字节) —— %', r.index_name, r.bytes, r.rationale;
  END LOOP;

  RAISE NOTICE '=== 合计删除 % 个索引，释放约 % ===', v_done, pg_size_pretty(v_bytes);
  IF v_done = 0 THEN
    RAISE NOTICE '>>> 没有 enabled=true 的条目。逐条放行后重跑。';
  ELSE
    RAISE NOTICE '>>> 建议现在跑一次 ANALYZE，观察一天，再决定下一批。';
  END IF;
END
$dropidx$;

\echo ''
\echo '--- [7] 结果核验 ---'

SELECT pg_size_pretty(coalesce(sum(pg_relation_size(ic.oid)),0)) AS still_reclaimable,
       count(*) AS still_candidate
FROM pg_stat_user_indexes ui
JOIN pg_index i  ON i.indexrelid = ui.indexrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
WHERE ui.idx_scan = 0 AND NOT i.indisunique AND pg_relation_size(ic.oid) > 1048576;

\echo '=== 2026-10-02 unused-index candidate scan done ==='

-- ============================================================================
-- 删完索引后必做（删索引不会让统计失效，但长期缺索引的表统计可能本来就旧）
--
--   ANALYZE public.request_state_transitions;
--   ANALYZE public.route_incident_events;
--   ANALYZE public.ursm_node_snapshot_min;
--   ANALYZE public.request_logs_2026_09;
--   ANALYZE public.usage_facts;
--
-- 另外，34 侧有 625 张表自 stats_reset 起从未 ANALYZE，完整清单见审计报告 A6。
-- ============================================================================

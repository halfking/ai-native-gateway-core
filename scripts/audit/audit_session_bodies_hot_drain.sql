-- §143.97 会话族 hot 排水只读审计脚本
--
-- ★ 用途：把 §143.97 轮的判据固化成**可在任何环境复跑**的只读读数脚本。
-- ★ 它回答四个问题：
--   Q1  session_bodies_hot 的批次选取**有没有索引承重**？（⇒ 缺口一）
--   Q2  promote_session_bodies_hot_to_partition 的**活体形态**是哪一版？
--       （⇒ 缺口二：裸 ON CONFLICT 还是带仲裁目标？）
--   Q3  60s 超时的**时间到底花在哪一段**？（⇒ 选取侧 vs 写侧）
--   Q4  台账说已应用的迁移，**实际对象**是不是那一版？（⇒ 614 的 now() 谓词缺口）
--
-- ★ ★★ 全脚本**只读**：只有 SELECT / EXPLAIN（不带 ANALYZE 的 EXPLAIN 是纯计划）。
-- ★ ★★ EXPLAIN ANALYZE 会真的执行 SELECT（只读，但会消耗 IO）⇒ 默认**关**，
--        需要时把 \set enable_analyze on 打开（★ 在生产上打开前请自行评估 IO）。
-- ★ ★★ 只在**只读**环境跑；★ 禁 CREATE TEMP TABLE（★ 会话族脚本的公共约束）。
--
-- ★ 用法：
--     psql -X -v ON_ERROR_STOP=1 -P pager=off -f scripts/audit/audit_session_bodies_hot_drain.sql
--     psql ... -v enable_analyze=on -f ...        # ★ 打开 Q3 的真实耗时测量

\set ON_ERROR_STOP on
\pset pager off
\timing off
SET statement_timeout = '120s';
SET lock_timeout = '5s';

\if :{?enable_analyze}
\else
\set enable_analyze off
\endif

\echo ''
\echo '=============================================================================='
\echo '§143.97 会话族 hot 排水只读审计'
\echo '=============================================================================='

-- ═══════════════════════════════════════════════════════════════════════
-- Q1  session_bodies_hot 的批次选取有没有索引承重？
-- ═══════════════════════════════════════════════════════════════════════

\echo ''
\echo '--- Q1.1 hot 表的索引清单（★ 看有没有 ts 首列）---'
SELECT i.indexrelid::regclass AS idx,
       pg_get_indexdef(i.indexrelid) AS def,
       i.indisvalid,
       i.indisready
FROM pg_index i
WHERE i.indrelid = 'public.session_bodies_hot'::regclass
ORDER BY 1;

\echo ''
\echo '--- Q1.2 是否有 ts 首列索引（★ 期望 t）---'
SELECT EXISTS (
  SELECT 1
  FROM pg_index i
  JOIN pg_class c ON c.oid = i.indexrelid
  JOIN pg_class ct ON ct.oid = i.indrelid
  JOIN pg_namespace n ON n.oid = ct.relnamespace
  JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS u(k, ord) ON u.ord = 1
  JOIN pg_attribute a ON a.attrelid = ct.oid AND a.attnum = u.k
  WHERE n.nspname = 'public'
    AND ct.relname = 'session_bodies_hot'
    AND i.indisvalid
    AND a.attname = 'ts'
) AS has_ts_leading_index;

\echo ''
\echo '--- Q1.3 对照组 session_turns_hot 的 ts 首列索引（★ 样板）---'
SELECT i.indexrelid::regclass AS idx,
       pg_get_indexdef(i.indexrelid) AS def
FROM pg_index i
WHERE i.indrelid = 'public.session_turns_hot'::regclass
  AND pg_get_indexdef(i.indexrelid) ILIKE '%(ts%'
ORDER BY 1;

\echo ''
\echo '--- Q1.4 父表 session_bodies 的 ts 首列索引（★ 707b 用 LIKE INCLUDING ALL 建 hot 表 ⇒ 索引继承自父表）---'
SELECT i.indexrelid::regclass AS idx,
       pg_get_indexdef(i.indexrelid) AS def
FROM pg_index i
WHERE i.indrelid = 'public.session_bodies'::regclass
  AND pg_get_indexdef(i.indexrelid) ILIKE '%(ts%'
ORDER BY 1;

\echo ''
\echo '--- Q1.5 批次选取的计划形态（★ 不带 ANALYZE ⇒ 纯计划，不执行）---'
EXPLAIN (COSTS ON)
SELECT id, partition_date FROM public.session_bodies_hot
WHERE ts < statement_timestamp() - interval '8 hours'
ORDER BY ts LIMIT 5000;

\echo ''
\echo '--- Q1.6 对照组 session_turns_hot 的批次选取计划（★ 形态对照）---'
EXPLAIN (COSTS ON)
SELECT id, partition_date FROM public.session_turns_hot
WHERE ts < statement_timestamp() - interval '8 hours'
ORDER BY ts, id, partition_date LIMIT 5000;

-- ═══════════════════════════════════════════════════════════════════════
-- Q2  promote_session_bodies_hot_to_partition 的活体形态是哪一版？
-- ═══════════════════════════════════════════════════════════════════════

\echo ''
\echo '--- Q2.1 活体形态指纹（★ 缺口二的判据）---'
SELECT pg_get_function_identity_arguments(p.oid) AS identity_args,
       pg_get_function_result(p.oid) AS result_type,
       length(p.prosrc) AS src_len,
       (p.prosrc LIKE '%ON CONFLICT DO NOTHING%') AS has_bare_on_conflict,
       (p.prosrc LIKE '%ON CONFLICT (id, partition_date)%') AS has_targeted_on_conflict,
       (p.prosrc LIKE '%reconciled AS%') AS has_reconciled,
       (p.prosrc LIKE '%v_parent_shape%') AS has_shape_guard,
       (p.prosrc LIKE '%pg_try_advisory_xact_lock%') AS has_try_lock
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public'
  AND p.proname = 'promote_session_bodies_hot_to_partition';

\echo ''
\echo '--- Q2.2 同一函数有没有多份重载（★ 重载残留 = 装错了形态）---'
SELECT count(*) AS overload_count
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public'
  AND p.proname = 'promote_session_bodies_hot_to_partition';

\echo ''
\echo '--- Q2.3 父表与 hot 表的唯一键清单（★ 判「仲裁目标覆盖不覆盖」的依据）---'
SELECT c.relname AS obj,
       i2.relname AS idx_or_constraint,
       pg_get_indexdef(i.indexrelid) AS def
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_class i2 ON i2.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relname IN ('session_bodies', 'session_bodies_hot')
  AND i.indisunique
ORDER BY 1, 2;

-- ═══════════════════════════════════════════════════════════════════════
-- Q3  60s 超时的时间花在哪一段？
-- ═══════════════════════════════════════════════════════════════════════

\echo ''
\echo '--- Q3.1 候选规模与表体量（★ 8h 保留窗）---'
\echo '    ★ 注：分区父表自身的 pg_total_relation_size 返 0（★ 数据在分区里）'
\echo '    ★ ⇒ 父表体积要看 Q3.7 的逐分区清单。'
SELECT count(*) AS candidate_rows,
       pg_size_pretty(pg_total_relation_size('public.session_bodies_hot')) AS hot_total_size,
       pg_size_pretty(pg_size_bytes(current_setting('work_mem'))) AS work_mem
FROM public.session_bodies_hot
WHERE ts < statement_timestamp() - interval '8 hours';

\echo ''
\echo '--- Q3.2 ★ 元组内联字节（★ 比较 work_mem 时必须用这个，不是 pg_column_size(jsonb)）---'
\echo '    ★ 说明：pg_column_size(<整行 composite>) 会 detoast 整行 ⇒ ★ 在生产上对'
\echo '    ★ 数千行大 payload **会超时**（2026-10-23 实测：statement_timeout）⇒'
\echo '    ★ ⇒ 所以本项默认【关】，用 -v tuple_bytes=on 打开（★ 只在可接受 IO 时开）。'
\if :{?tuple_bytes}
SELECT count(*) AS rows_8h,
       sum(pg_column_size(h)) AS tuple_bytes_total,
       pg_size_bytes(current_setting('work_mem')) AS work_mem_bytes,
       round(sum(pg_column_size(h))::numeric
             / pg_size_bytes(current_setting('work_mem')), 2) AS ratio_vs_work_mem,
       avg(pg_column_size(h))::int AS avg_tuple_bytes,
       max(pg_column_size(h)) AS max_tuple_bytes
FROM public.session_bodies_hot h
WHERE h.ts < statement_timestamp() - interval '8 hours';
\else
\echo '    （已跳过 ⇒ 用 Q3.3 的单列口径 + Q3.3b 的 Sort 内存读数替代）'
\endif

\echo ''
\echo '--- Q3.3 各 jsonb 列的存储体积与出表情况（★ 单列口径：秒回；★ >2000B 即 TOAST 出表 ⇒ 元组里只占 18 字节指针）---'
SELECT count(*) AS n,
       round(avg(pg_column_size(request_delta))) AS avg_request_delta,
       max(pg_column_size(request_delta)) AS max_request_delta,
       count(*) FILTER (WHERE pg_column_size(request_delta) > 2000) AS request_delta_toasted,
       round(avg(pg_column_size(response_delta))) AS avg_response_delta,
       count(*) FILTER (WHERE pg_column_size(response_delta) > 2000) AS response_delta_toasted,
       round(avg(pg_column_size(outbound_body))) AS avg_outbound_body,
       count(*) FILTER (WHERE pg_column_size(outbound_body) > 2000) AS outbound_body_toasted,
       round(avg(pg_column_size(request_attachments))) AS avg_request_attachments,
       round(avg(pg_column_size(response_attachments))) AS avg_response_attachments
FROM public.session_bodies_hot
WHERE ts < statement_timestamp() - interval '8 hours';

\echo ''
\echo '--- Q3.3b ★★ 批次选取的真实耗时与 Sort 内存（★ 唯一可信的「是否溢写」判据）---'
\echo '    ★ planner 自己报的数 ⇒ ★ 不要用列体积去推算（★ §143.97 的教训）。'
\echo '    ★ 需要 -v enable_analyze=on（★ EXPLAIN ANALYZE 会真的执行 SELECT ⇒ 只读但耗 IO）。'
\if :{?enable_analyze}
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF)
SELECT h.id, h.partition_date, h.session_id, h.turn_no, h.tenant_id, h.request_id,
       h.request_delta, h.response_delta, h.outbound_body,
       h.request_attachments, h.response_attachments, h.ts, h.kind
FROM public.session_bodies_hot h
WHERE h.ts < statement_timestamp() - interval '8 hours'
ORDER BY h.ts
LIMIT 5000;
\else
\echo '    （已跳过 ⇒ 需要 -v enable_analyze=on）'
\endif

\echo ''
\echo '--- Q3.4 hot 表行数与年龄分布（★ 存量是否单调增长）---'
SELECT count(*) AS hot_rows,
       count(*) FILTER (WHERE ts < statement_timestamp() - interval '8 hours') AS over_8h,
       count(*) FILTER (WHERE ts < statement_timestamp() - interval '24 hours') AS over_24h,
       min(ts) AS oldest,
       max(ts) AS newest
FROM public.session_bodies_hot;

\echo ''
\echo '--- Q3.5 冲突集（★ 按父表的每个唯一键分别算；★ 用错键 ⇒ 阴性结论整个翻转）---'
SELECT count(*) AS candidates,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM public.session_bodies s
          WHERE s.tenant_id = h.tenant_id AND s.session_id = h.session_id
            AND s.turn_no = h.turn_no AND s.partition_date = h.partition_date))
         AS conflict_tenant_session_turn,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM public.session_bodies s
          WHERE s.tenant_id = h.tenant_id AND s.request_id = h.request_id
            AND s.partition_date = h.partition_date))
         AS conflict_tenant_request,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM public.session_bodies s
          WHERE s.tenant_id = h.tenant_id AND s.session_id = h.session_id
            AND s.partition_date = h.partition_date
            AND s.kind = 'final_full' AND h.kind = 'final_full'))
         AS conflict_final_full,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM public.session_bodies s
          WHERE s.id = h.id AND s.partition_date = h.partition_date))
         AS conflict_id_partition
FROM public.session_bodies_hot h
WHERE h.ts < statement_timestamp() - interval '8 hours';

\echo ''
\echo '--- Q3.6 reconciled CTE 会命中几行（★ = 0 ⇒ 不是它开销）---'
SELECT count(*) AS reconciled_would_update
FROM public.session_bodies_hot h
WHERE h.ts < statement_timestamp() - interval '8 hours'
  AND EXISTS (SELECT 1 FROM public.session_bodies s
      WHERE s.tenant_id = h.tenant_id AND s.request_id = h.request_id
        AND s.partition_date = h.partition_date
        AND s.id <> h.id);

\echo ''
\echo '--- Q3.7 写侧对象：父表当月分区（★ heap 还是 columnar）---'
SELECT c.relname AS partition,
       am.amname AS access_method,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size
FROM pg_inherits inh
JOIN pg_class c ON c.oid = inh.inhrelid
JOIN pg_am am ON am.oid = c.relam
WHERE inh.inhparent = 'public.session_bodies'::regclass
ORDER BY c.relname;

-- ═══════════════════════════════════════════════════════════════════════
-- Q4  台账说已应用的迁移，实际对象是不是那一版？
-- ═══════════════════════════════════════════════════════════════════════

\echo ''
\echo '--- Q4.1 双账本里 614 / 708 / 897 / 898 / 899 的记账（★ 898/899 期望 0 行）---'
SELECT 'schema_migrations' AS ledger, version, applied_at
FROM schema_migrations
WHERE version IN ('614','708','897','898','899')
UNION ALL
SELECT 'revision_sequences', sequence_name, applied_at
FROM gateway_db_revision_sequences
WHERE sequence_name IN ('614','708','897','898','899')
ORDER BY 1, 2;

\echo ''
\echo '--- Q4.2 ★ 614 的 ts 索引有没有在位（★ 期望 t 才是 614 那一版的完整形态）---'
SELECT to_regclass('public.idx_session_bodies_hot_ts') IS NOT NULL AS ts_index_exists,
       to_regclass('public.idx_session_bodies_hot_partition_date') IS NOT NULL AS partition_date_index_exists,
       to_regclass('public.idx_session_bodies_hot_request') IS NOT NULL AS request_index_exists,
       to_regclass('public.idx_session_bodies_hot_lookup') IS NOT NULL AS lookup_index_exists;

\echo ''
\echo '=============================================================================='
\echo '★ 读数解释（§143.97 轮的实测基准，★ 2026-10-23 于生产 252 共享库）'
\echo '  · Q1.2 期望 t ⇒ ts 首列索引在位（★ 缺口一已闭合）'
\echo '  · Q2.1 期望 bare=t / targeted=f / reconciled=t ⇒ 活体形态是【裸 ON CONFLICT】那一版'
\echo '    （★ 若 targeted=t ⇒ 装的是 708 的带仲裁目标形态 ⇒ ⇒ 23505 复发风险）'
\echo '  · Q3.1/Q3.2 候选规模与元组字节；★ 低于 work_mem ⇒ 批次选取**不溢写**'
\echo '  · Q3.7 期望 heap（★ 非 columnar ⇒ 父表写入不是列存慢）'
\echo '  · Q4.2 期望全部 t ⇒ 614 那一版的索引集完整落地（★ ts_index_exists=f ⇒ 命中 §143.97.6 的缺口）'
\echo '=============================================================================='

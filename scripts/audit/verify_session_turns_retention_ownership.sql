-- ═══════════════════════════════════════════════════════════════════════════
-- verify_session_turns_retention_ownership.sql
--
-- 用途：查清「session_turns 族的留存机制归属」与「session_turns_hot 由谁清、
--       何时清、能不能清干净」—— 回答 §143.94 自己标注的两个诚实边界：
--       ① PG 侧 session_turns 的行级留存（当时标「未测」）
--       ② session_turns_hot 孤儿表由谁清理、何时清理（当时标「未测」）
--
-- 背景（§143.95，2026-10-23，只读普查）：
--   · session_turns 是**月度分区父表**；session_turns_hot 是**独立 heap 热写表**
--     （按设计就不在父表下 —— 它是写路径的热表，冷行由
--      promote_session_turns_hot_to_partition() 原子搬进父表的月分区）。
--   · ★★ §143.94 把 session_turns_hot 称作「孤儿表」⇒ 本轮核实这个措辞：
--     「不在父表下」与「没人清理」是**两件事**，前者是设计、后者才是缺陷。
--     ⇒ 本轮实测：hot 表**有**设计内的排水通道（promote，1h 周期 / 8h 保留窗 /
--       5000 行批 / 60s 单批 deadline），但该通道**自 2026-10-23 08:47 起每 tick 必败**。
--
-- ★★★ 本脚本**只读**：全部语句为 SELECT / SET（会话参数），无 DDL/DML。
-- ★ 用法：
--   252（经 154 跳板）：bash scripts/audit/run_prod_sql.sh <本文件> 900 <前缀>
--   211（LAN docker）： ssh 211 'docker exec -i acc-pg17 psql -U acc_super \
--                                  -d llm_gateway -v ON_ERROR_STOP=1 -f -' < <本文件>
--
-- ── ★★ 本脚本踩过的三个量具坑（写在头里，防止下一个人重踩）──────────────
--   ① **裸子串会被注释喂饱**：pg_stat_statements.query 里若含
--      「promote_session_turns_hot_to_partition」这个词但实际是别的语句
--      （本轮就撞到：凭据自检的 SQL 注释里提到了它）⇒ 必须**先剥注释再匹配**。
--   ② **键选错 ⇒ 阴性结论也是错的**：promote 自己的去重键是
--      (tenant_id, request_id)，而父表的冲突键是
--      (tenant_id, session_id, partition_date) WHERE is_final_success
--      ⇒ 用前者查冲突恒得 0（本轮第一次就查错了）。
--   ③ **列名近似 ⇒ 读数张冠李戴**：`success`（请求成功标记）≠ `is_final_success`
--      （该会话唯一最终成功标记）⇒ 碰撞判据必须写死 is_final_success。
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '600s';

\echo '=== 0/10: 前置自检 —— 库身份 / 会话时区 / pg_stat_statements 可用性 ==='
SELECT current_database() AS db,
       current_user AS usr,
       now() AS now_local,
       current_setting('TimeZone') AS session_tz,
       (SELECT oid FROM pg_database WHERE datname = current_database()) AS db_oid,
       (SELECT count(*) FROM pg_stat_statements) AS pss_rows;

\echo ''
\echo '=== 1/10: ★★★ 族清单 —— 父表下的月分区 + 独立的 hot 热表（逐个列名点名）==='
-- ★ 「在不在父表下」用 pg_inherits 的父子列名：inhrelid / inhparent。
--   ★ 父表自己也不在 pg_inherits 里（它没有父）⇒ 判定必须逐个列名点名，
--   不能用「不在结果集里 ⇒ 是孤儿」这种一刀切。
SELECT 'parent' AS role,
       c.relname AS object,
       c.relkind::text AS relkind,
       pg_total_relation_size(c.oid) AS total_bytes,
       (SELECT count(*) FROM pg_inherits i WHERE i.inhparent = c.oid) AS n_children,
       '' AS attached_detail
FROM pg_class c
WHERE c.relname = 'session_turns' AND c.relkind = 'p'
UNION ALL
SELECT 'child', c.relname, c.relkind::text, pg_total_relation_size(c.oid), 0,
       'attached=' || EXISTS (SELECT 1 FROM pg_inherits i
         WHERE i.inhparent = 'public.session_turns'::regclass
           AND i.inhrelid = c.oid)::text
FROM pg_class c
WHERE c.relname ~ '^session_turns_[0-9]{4}_[0-9]{2}$' AND c.relkind = 'r'
UNION ALL
SELECT 'hot', c.relname, c.relkind::text, pg_total_relation_size(c.oid), 0,
       'attached=' || EXISTS (SELECT 1 FROM pg_inherits i
         WHERE i.inhparent = 'public.session_turns'::regclass
           AND i.inhrelid = c.oid)::text
FROM pg_class c
WHERE c.relname = 'session_turns_hot'
ORDER BY role, object;

\echo ''
\echo '=== 2/10: ★★★★ session_turns_hot 现状（行数 / 体积 / ts 跨度）==='
-- ★★ 用 ts（本地时刻）而不是 partition_date（UTC 日历日）判断时间跨度。
SELECT count(*) AS hot_rows,
       pg_total_relation_size('public.session_turns_hot') AS hot_total_bytes,
       pg_relation_size('public.session_turns_hot') AS heap_bytes,
       pg_total_relation_size('public.session_turns_hot')
         - pg_relation_size('public.session_turns_hot') AS toast_bytes,
       min(ts) AS min_ts,
       max(ts) AS max_ts,
       now() - min(ts) AS oldest_age
FROM public.session_turns_hot;

\echo ''
\echo '=== 3/10: ★★★★★★ 8h 保留窗实测 —— hot 里「已超保留窗却仍在」的行 ==='
-- ★★ promote 的排水判据：h.ts < statement_timestamp() - p_retention
--    而 p_retention 由 Go 侧 resolvePromoteConfig("session_turns_hot") 给出：
--    该 label 不在 switch 的任何 case 里 ⇒ 落 default 分支 ⇒
--    lifecycle.hot_retention_hours（默认 DefaultRetentionWindow = 8h）。
--   ⇒ 「hot 表里还留着超过 8h 的行」= 排水没做完（滞留）。
SELECT count(*) AS hot_rows,
       count(*) FILTER (WHERE ts <  now() - interval '8 hours') AS older_than_8h,
       count(*) FILTER (WHERE ts >= now() - interval '8 hours') AS within_8h,
       min(ts) FILTER (WHERE ts <  now() - interval '8 hours') AS oldest_stuck_ts,
       max(ts) FILTER (WHERE ts <  now() - interval '8 hours') AS newest_stuck_ts,
       now() - min(ts) FILTER (WHERE ts <  now() - interval '8 hours') AS oldest_stuck_age
FROM public.session_turns_hot;

\echo ''
\echo '=== 4/10: ★★★★★★ 两种「冲突」必须分开量（★ 键不同 ⇒ 结论方向不同）==='
-- ④a promote **自己的**去重键 (tenant_id, request_id)：它排除的冲突。
-- ④b 父表**部分唯一索引**的键 (tenant_id, session_id, partition_date) WHERE is_final_success：
--     ★ 这才是真正会让 promote 的 INSERT 报 23505 的冲突。
-- ⇒ 本轮实测：④a = 0（promote 的去重看不见），④b = 1 ⇒ ★ 结论方向整个翻转。
WITH stuck AS MATERIALIZED (
    SELECT tenant_id, request_id, ts
    FROM public.session_turns_hot
    WHERE ts < now() - interval '8 hours'
),
dedup_conflict AS MATERIALIZED (
    SELECT s.tenant_id, s.request_id
    FROM stuck s
    WHERE EXISTS (
        SELECT 1 FROM public.session_turns a
        WHERE a.request_id = s.request_id AND a.tenant_id = s.tenant_id
    )
)
SELECT (SELECT count(*) FROM stuck)            AS stuck_rows,
       (SELECT count(*) FROM dedup_conflict)   AS dedup_key_conflict_rows;

\echo ''
\echo '=== 5/10: ★★★★★★ 真正会触发 23505 的冲突集（is_final_success，键 = 父表唯一索引键）==='
-- ★★ 键必须是 (tenant_id, session_id, partition_date)，列必须是 is_final_success。
WITH hot_fs AS MATERIALIZED (
    SELECT DISTINCT tenant_id, session_id, partition_date
    FROM public.session_turns_hot
    WHERE is_final_success
)
SELECT h.tenant_id,
       h.session_id,
       h.partition_date,
       (SELECT count(*) FROM public.session_turns_hot x
         WHERE x.tenant_id = h.tenant_id AND x.session_id = h.session_id
           AND x.partition_date = h.partition_date) AS hot_rows,
       (SELECT count(*) FROM public.session_turns_hot x
         WHERE x.tenant_id = h.tenant_id AND x.session_id = h.session_id
           AND x.partition_date = h.partition_date AND x.is_final_success) AS hot_fs_rows,
       (SELECT count(*) FROM public.session_turns p
         WHERE p.tenant_id = h.tenant_id AND p.session_id = h.session_id
           AND p.partition_date = h.partition_date) AS parent_rows,
       (SELECT count(*) FROM public.session_turns p
         WHERE p.tenant_id = h.tenant_id AND p.session_id = h.session_id
           AND p.partition_date = h.partition_date AND p.is_final_success) AS parent_fs_rows
FROM hot_fs h
WHERE EXISTS (
    SELECT 1 FROM public.session_turns p
    WHERE p.tenant_id = h.tenant_id
      AND p.session_id = h.session_id
      AND p.partition_date = h.partition_date
      AND p.is_final_success IS TRUE
)
ORDER BY 1,2,3;

\echo ''
\echo '=== 6/10: ★★★★ hot 表侧 is_final_success 的形态（★ hot 自己的唯一索引是干净的）==='
SELECT CASE WHEN is_final_success IS NULL THEN 'NULL'
            WHEN is_final_success THEN 'true' ELSE 'false' END AS fs_state,
       count(*) AS rows
FROM public.session_turns_hot
GROUP BY 1 ORDER BY 2 DESC;

\echo ''
\echo '=== 7/10: ★★★★★★ 父表与 hot 的累计写入计数（★ n_tup_ins 是累计计数器 ⇒ 不受快照过期影响）==='
-- ★★★ 本段是「父表饿死」的决定性读数：
--   hot 的 n_tup_ins ≈ 父表的 n_tup_ins ⇒ **父表的唯一写入路径就是 promote**
--   ⇒ ⇒ promote 一停 ⇒ 父表彻底停止接收冷行。
--   ★ 父表 n_tup_upd = 0 ⇒ **final_success 标记只活在 hot 面**（父表面的
--     mark UPDATE 恒 0 行）⇒ ⇒ 标记随行被搬进父表。
SELECT relname, n_tup_ins, n_tup_upd, n_tup_del, n_live_tup, idx_scan, seq_scan
FROM pg_stat_user_tables
WHERE relname IN ('session_turns_hot',
                  'session_turns_2026_07', 'session_turns_2026_08',
                  'session_turns_2026_09', 'session_turns_2026_10', 'session_turns_2026_11')
ORDER BY relname;

\echo ''
\echo '=== 8/10: ★★★★★★ 唯一的部分唯一索引（★ 23505 的来源）===='
SELECT t.relname AS table_name,
       c.relname AS index_name,
       pg_get_indexdef(c.oid) AS index_def
FROM pg_class c
JOIN pg_index i ON i.indexrelid = c.oid
JOIN pg_class t ON t.oid = i.indrelid
WHERE t.relname IN ('session_turns', 'session_turns_hot')
  AND i.indisunique
  AND pg_get_indexdef(c.oid) LIKE '%is_final_success%'
ORDER BY t.relname;

\echo ''
\echo '=== 9/10: ★★★★★★ 留存机制三件套 + 行级留存的证据 ==='
-- ① 841 的策略表 + 审计表（策略表为空 ⇒ 路径 B 空转；审计表 0 行 ⇒ 从未 DROP 过月分区）
SELECT 'retention_policy' AS what, count(*) AS rows FROM public.llm_gateway_partition_retention
UNION ALL
SELECT 'drop_log', count(*) FROM public.llm_gateway_partition_drop_log
UNION ALL
SELECT 'expired_now', count(*) FROM public.llm_gateway_expired_month_partitions();

\echo '--- 9b: ★★ 穷举 settings_kv 里【所有】lifecycle.* 键（★ 不做子集过滤，避免「我认的形态全了吗」）---'
SELECT key, value
FROM public.settings_kv
WHERE key LIKE 'lifecycle.%'
ORDER BY key;

\echo '--- 9c: ★★ 父表上有没有行级留存语句（pg_stat_statements 侧证据；★ 只记成功语句 ⇒ 是下界）---'
SELECT calls, left(query, 100) AS query_head
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND (query ILIKE 'delete%session_turns%' OR query ILIKE 'truncate%session_turns%')
ORDER BY calls DESC;

\echo '--- 9d: ★★ hot 表的活读者（库侧语句证据，★ 剥注释后匹配）---'
WITH pss AS (
    SELECT calls,
           regexp_replace(query, '--[^\n]*', '', 'g') AS q
    FROM pg_stat_statements
    WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
      AND query ILIKE '%session_turns_hot%'
)
SELECT calls, left(q, 110) AS query_head
FROM pss
WHERE q ILIKE '%session_turns_hot%'
  AND q NOT ILIKE '%promote_session_turns_hot_to_partition%'
ORDER BY calls DESC
LIMIT 15;

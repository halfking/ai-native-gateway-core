-- =====================================================================
-- §143.99 只读审计脚本：会话正文族（session_bodies）读侧放大
--
-- ★ 本脚本回答一个问题：
--   §143.98 实测「读侧 3 条 SELECT 占 6,716 s DB 时间 = promote 的 3.1 倍」
--   ⇒ ⇒ 本轮把「读放大」从【一个累计数字】变成【一条可判定的算术】：
--   ⇒ ⇒ 「多少调用 × 每次多少字节 × 为什么每次都白干」。
--
-- ★ ★★ 量具纪律（本脚本自带的自检，防止「读数是量具坏了」）：
--   Q2 用三条【锚点语句】当场验证 pg_stat_statements.rows 的语义
--   （恒空投影 ⇒ rows=0；count(*) ⇒ rows=1；generate_series(1,3) ⇒ rows=3）。
--   ★ 没有这一步，Q1 的「rows=0」会被误读成「这 3 条语句返回了 0 行」——
--   ★ 而 rows 的语义如果变了，那个结论整个翻盘。
--
-- ★ 只读：本脚本【不建任何对象、不写任何行】。
--   ★ 生产护栏拒绝行首的写/DDL 关键字 ⇒ ⇒ 本脚本全部语句以 SELECT 开头。
--
-- 用法（★ 必须串行；★ 生产 252 共享库）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/audit_session_bodies_read_amplification.sql 900
-- =====================================================================
SET statement_timeout = '300s';
SET lock_timeout = '10s';

\echo '=== Q1 读侧三条 SELECT 的调用量 / 累计耗时 / 返回行数 / 每次拉取字节数 ==='
-- ★ bytes_per_call = (shared_blks_hit + shared_blks_read) * 8192 / calls
-- ★   ⇒ 【每次调用平均从缓冲池/磁盘拉了多少字节】。
-- ★ rows_per_call   = rows / calls
-- ★   ⇒ 【每次调用平均返回几行】。
SELECT queryid,
       calls,
       round(total_exec_time::numeric, 1)                             AS total_exec_ms,
       round(mean_exec_time::numeric, 2)                              AS mean_exec_ms,
       rows,
       round(rows::numeric / GREATEST(calls, 1), 3)                   AS rows_per_call,
       shared_blks_hit,
       shared_blks_read,
       round((shared_blks_hit + shared_blks_read)::numeric * 8192
             / GREATEST(calls, 1))                                     AS bytes_per_call,
       (query LIKE '%COALESCE(%')                                     AS has_coalesce,
       (query LIKE '%LATERAL%')                                       AS has_lateral,
       (query LIKE '%outbound_body <> %::jsonb%')                     AS has_jsonb_null_guard,
       left(regexp_replace(query, '\s+', ' ', 'g'), 60)                AS head
FROM pg_stat_statements
WHERE dbid = (SELECT dbid FROM pg_stat_database WHERE datname = current_database())
  AND query LIKE '%session_bodies_hot%'
  AND query LIKE '%outbound_body%'
  AND query LIKE 'SELECT %'
ORDER BY total_exec_time DESC;

\echo '=== Q2 量具自检 —— rows 语义锚点（恒空 => 0；count(*) => 1；generate_series(1,3) => 3） ==='
-- ★★ 这三条是【本脚本自己跑的锚点】：★ 跑完再到 pg_stat_statements 里查它们。
-- ★★ 若锚点读数与预期不符 ⇒ ⇒ Q1 的 rows 列不可信 ⇒ ⇒ 结论作废。
SELECT 1 AS anchor_zero_rows WHERE false;
SELECT count(*) AS anchor_one_row FROM (SELECT 1 WHERE false) x;
SELECT g AS anchor_three_rows FROM generate_series(1, 3) g;

\echo '=== Q3 读侧放大在【全库 DB 时间】里的占比 ==='
SELECT round(sum(total_exec_time)::numeric, 1)                              AS all_stmt_ms,
       round(sum(CASE WHEN query LIKE '%session_bodies_hot%'
                        AND query LIKE '%outbound_body%'
                       THEN total_exec_time ELSE 0 END)::numeric, 1)        AS read_amp_ms,
       round(sum(CASE WHEN query LIKE '%session_bodies_hot%'
                        AND query LIKE '%outbound_body%'
                       THEN total_exec_time ELSE 0 END)::numeric
             / NULLIF(sum(total_exec_time)::numeric, 0) * 100, 2)          AS read_amp_pct,
       round(sum(CASE WHEN query LIKE 'INSERT INTO public.session_bodies_hot%'
                       THEN total_exec_time ELSE 0 END)::numeric, 1)        AS promote_write_ms
FROM pg_stat_statements
WHERE dbid = (SELECT dbid FROM pg_stat_database WHERE datname = current_database());

\echo '=== Q4 长尾的度量 —— 每个 session 的轮次数（★ L3 回退按 session_id 取【全部】轮次） ==='
-- ★ 为什么这一项是本轮的核心：
--   L3 冷启动回退的 turns 腿是 `WHERE t.session_id = $1`（★ 无 LIMIT）
--   ⇒ ⇒ 一次调用的候选行数 = 该 session 的【累计轮次数】
--   ⇒ ⇒ 而实测有 session 的轮次达【四万】（探测型，见 Q9）
--   ⇒ ⇒ 这就是「平均每次只有几毫秒、但均值 182 ms」的答案：【长尾】。
SELECT 'hot' AS leg, session_id, tenant_id, count(*) AS turns
FROM public.session_turns_hot
WHERE session_id IS NOT NULL AND session_id <> '' AND tenant_id <> ''
GROUP BY session_id, tenant_id
ORDER BY turns DESC
LIMIT 5;

SELECT 'parent_2026_10' AS leg, session_id, tenant_id, count(*) AS turns
FROM public.session_turns_2026_10
WHERE session_id IS NOT NULL AND session_id <> '' AND tenant_id <> ''
GROUP BY session_id, tenant_id
ORDER BY turns DESC
LIMIT 5;

SELECT 'parent_2026_09' AS leg, session_id, tenant_id, count(*) AS turns
FROM public.session_turns_2026_09
WHERE session_id IS NOT NULL AND session_id <> '' AND tenant_id <> ''
GROUP BY session_id, tenant_id
ORDER BY turns DESC
LIMIT 5;

\echo '=== Q5 outbound_body 的体积分布（★ 每次调用 detoast 的量级） ==='
-- ★ pg_column_size 给【存储体积】（TOAST 指针形态会返回 18 字节）
-- ★ ⇒ 与 Q1 的 bytes_per_call 对照 ⇒ ⇒ 「每次调用拉的那些字节就是它」。
SELECT count(*)                                                            AS n,
       round(avg(pg_column_size(outbound_body)))                         AS avg_outbound,
       round(max(pg_column_size(outbound_body)))                          AS max_outbound,
       count(*) FILTER (WHERE pg_column_size(outbound_body) > 1000)      AS outbound_gt1k,
       count(*) FILTER (WHERE outbound_body IS NOT NULL)                  AS is_not_null,
       count(*) FILTER (WHERE outbound_body IS NOT NULL
                          AND outbound_body <> 'null'::jsonb)             AS has_body
FROM (SELECT outbound_body FROM public.session_bodies_hot LIMIT 1500) s;

\echo '=== Q6 判据口径对照 —— details 侧元数据能否替代 bodies 侧 blob 判据 ==='
-- ★ 结论（2026-10-24 实测）：★ **不能**。
-- ★   both_agree = 0 ⇒ 「details 说有出站消息」与「bodies 里确有正文」几乎零交集
-- ★   ⇒ ⇒ ⇒ **任何「用元数据列当『有正文』的代理判据」的修法都不成立**
-- ★   ⇒ ⇒ ⇒ 唯一能省掉 detoast 的方向是【把判据物化成列/索引】（迁移项）。
SELECT count(*)                                                                   AS join_rows,
       count(*) FILTER (WHERE d.outbound_msg_count IS NOT NULL)                  AS details_has_count,
       count(*) FILTER (WHERE sb.outbound_body IS NOT NULL)                       AS bodies_is_not_null,
       count(*) FILTER (WHERE sb.outbound_body IS NOT NULL
                          AND sb.outbound_body <> 'null'::jsonb)                 AS bodies_has_body,
       count(*) FILTER (WHERE d.outbound_msg_count IS NOT NULL
                          AND sb.outbound_body IS NOT NULL
                          AND sb.outbound_body <> 'null'::jsonb)                 AS both_agree,
       count(*) FILTER (WHERE d.outbound_msg_count IS NULL
                          AND sb.outbound_body IS NOT NULL
                          AND sb.outbound_body <> 'null'::jsonb)                 AS bodies_only,
       count(*) FILTER (WHERE d.outbound_msg_count IS NOT NULL
                          AND NOT (sb.outbound_body IS NOT NULL
                                   AND sb.outbound_body <> 'null'::jsonb))       AS details_only
FROM public.session_turn_details_hot d
LEFT JOIN public.session_bodies_hot sb ON sb.request_id = d.request_id;

\echo '=== Q7 索引盘点 —— turns/bodies 族上有没有「session_id 定位」与「带 jsonb 判据的部分索引」 ==='
-- ★ 两个缺口各对应一条修法：
--   ① turns 族缺 (session_id, ts DESC) 形态的定位索引 ⇒ ⇒ L3 回退的 turns 腿
--      在父表上退化成低效路径（★ 实测 4 万轮次的 session ⇒ 单次 32.7~155.2 s）。
--   ② bodies 族没有【带 jsonb 判据】的部分索引 ⇒ ⇒ 「有正文」只能在运行时
--      detoast 判定（★ 实测每次调用拉 ~218 KB ⇒ 恰好一个 outbound_body 的量级）。
SELECT c.oid::regclass::text AS rel,
       i.relname            AS idx,
       pg_get_indexdef(x.indexrelid) AS idxdef
FROM pg_index x
JOIN pg_class c ON c.oid = x.indrelid
JOIN pg_class i ON i.oid = x.indexrelid
WHERE c.relname IN ('session_turns', 'session_turns_hot',
                    'session_turns_2026_09', 'session_turns_2026_10')
  AND (i.relname LIKE '%session%' OR i.relname LIKE '%request%')
ORDER BY rel, idx;

\echo '=== Q8 ★ 长尾算术自检 —— max/mean 比（★ 纯 stats 自算，不依赖任何手工实验） ==='
-- ★ 判据：★ 若 mean_exec_time 远小于 max_exec_time ⇒ ⇒ 【均值被长尾拉高】
-- ★   ⇒ ⇒ 「这 3 条语句的 6,819 s」不是【每次都慢】，而是【绝大多数很快 + 少数极慢】。
-- ★   ⇒ ⇒ ★★ 这一条比任何「平均成本」的解读都更接近真相。
SELECT queryid,
       calls,
       round(mean_exec_time::numeric, 2)  AS mean_exec_ms,
       round(max_exec_time::numeric, 1)  AS max_exec_ms,
       round(max_exec_time::numeric / NULLIF(mean_exec_time, 0), 1) AS tail_ratio,
       round(total_exec_time::numeric, 1) AS total_exec_ms,
       round((calls * mean_exec_time)::numeric, 1) AS recomputed_total_ms
FROM pg_stat_statements
WHERE dbid = (SELECT dbid FROM pg_stat_database WHERE datname = current_database())
  AND query LIKE '%session_bodies_hot%'
  AND query LIKE '%outbound_body%'
  AND query LIKE 'SELECT %'
ORDER BY total_exec_time DESC;

\echo '=== Q9 探测型 session 的轮次累积（★ 长尾的来源） ==='
-- ★ session_id 形如 `sys:probe:<cred>:<yyyymmdd>` ⇒ ⇒ 一个探测凭据按天一个 session
-- ★   ⇒ ⇒ 轮次只增不减 ⇒ ⇒ 一天下来可以累积到【万级】。
-- ★ ★★ 而 L3 回退的 turns 腿是 `WHERE t.session_id = $1`（★ 无 LIMIT）
-- ★   ⇒ ⇒ 一次调用要把这些轮次全部过一遍 ⇒ ⇒ 单次数十秒。
-- ★ 两段聚合（★ 先 GROUP BY 落出每 session 的轮次，再汇总）：
-- ★   ★ 不用窗口函数 —— 798,278 行的月分区上做 PARTITION BY 窗口会拖垮 statement_timeout。
SELECT CASE WHEN session_id LIKE 'sys:probe:%' THEN 'sys:probe:*'
            WHEN session_id LIKE 'sys:%'        THEN 'sys:*'
            ELSE 'gw:*' END              AS session_kind,
       count(*)                          AS sessions,
       sum(c)                            AS turns,
       round(avg(c)::numeric, 1)         AS avg_turns_per_session,
       max(c)                            AS max_turns_in_session
FROM (SELECT session_id, count(*) AS c
      FROM public.session_turns_2026_09
      WHERE session_id IS NOT NULL AND session_id <> ''
      GROUP BY session_id) t
GROUP BY 1
ORDER BY turns DESC;

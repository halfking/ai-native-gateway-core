-- report_duplicate_session_success.sql
--
-- v4 T7 (2026-08-18, migration 532) 补偿脚本 —— 只读报告，不改数。
--
-- 目的：扫描历史上「同一 gw_session_id 多行 success」的数据（migration 054
-- 记录的缺陷：客户端 5 次重发曾产生 5 行全 success），以及 migration 532
-- 上线后仍未被 claim 的成功行（滚动发布窗口内写入、或 claim 降级丢失标记），
-- 输出人工修正建议（UPDATE 语句以文本形式生成，不执行）。
--
-- 数据源：request_logs_with_current_month（= request_logs_hot UNION ALL
-- request_logs 月度分区母表，migration 340/448/459/491/510/532 维护），
-- 因此同时覆盖热表与已 promote 的归档分区。
--
-- 用法：
--   psql "$DATABASE_URL" -f sql/scripts/report_duplicate_session_success.sql
--   # 或导出 CSV：psql ... -v format=csv ...
--
-- 修正原则（人工确认后执行生成的语句）：
--   * 多行 success 的会话：保留 ts 最新的一行为 is_final_success=TRUE
--     （「最终」成功语义：客户端重发链上最后一次成功最可能被客户端完整接收），
--     其余行保持 FALSE（读路径按 superseded_success 标注，不回改历史）；
--   * 仅一行 success 且无人持有标记的会话：补标该行为 TRUE；
--   * 所有 UPDATE 前先检查目标行不在并发写入窗口（建议低峰期执行）。

\echo '== [1/4] 概览：同会话多行 success 的会话数（按存储位置分布） =='
SELECT count(*)                                       AS sessions_with_duplicate_success,
       sum(success_rows)                              AS total_success_rows_in_those_sessions,
       sum(success_rows) - count(*)                   AS rows_to_leave_unmarked
FROM (
    SELECT gw_session_id, count(*) FILTER (
               WHERE success AND request_status = 'success') AS success_rows
      FROM request_logs_with_current_month
     WHERE COALESCE(gw_session_id, '') <> ''
     GROUP BY gw_session_id
    HAVING count(*) FILTER (WHERE success AND request_status = 'success') > 1
) dup;

\echo '== [2/4] 概览：无人持有标记的成功会话（单行 success 且 is_final_success=FALSE，待补标） =='
SELECT count(*) AS sessions_success_without_claim
FROM (
    SELECT gw_session_id,
           count(*) FILTER (WHERE success AND request_status = 'success') AS success_rows,
           count(*) FILTER (WHERE is_final_success)                       AS claimed_rows
      FROM request_logs_with_current_month
     WHERE COALESCE(gw_session_id, '') <> ''
     GROUP BY gw_session_id
    HAVING count(*) FILTER (WHERE success AND request_status = 'success') >= 1
       AND count(*) FILTER (WHERE is_final_success) = 0
) unclaimed;

\echo '== [3/4] 明细：重复成功会话（每会话至多前 50 行，按 ts 升序） =='
WITH dup_sessions AS (
    SELECT gw_session_id
      FROM request_logs_with_current_month
     WHERE COALESCE(gw_session_id, '') <> ''
     GROUP BY gw_session_id
    HAVING count(*) FILTER (WHERE success AND request_status = 'success') > 1
),
ranked AS (
    SELECT rl.gw_session_id, rl.request_id, rl.ts, rl.request_status,
           rl.success, rl.is_final_success, rl.client_request_id,
           rl.request_type, rl.latency_ms, rl.tenant_id,
           row_number() OVER (
               PARTITION BY rl.gw_session_id, (rl.success AND rl.request_status = 'success')
               ORDER BY rl.ts DESC) AS success_rank_desc
      FROM request_logs_with_current_month rl
      JOIN dup_sessions d USING (gw_session_id)
     ORDER BY rl.gw_session_id, rl.ts ASC
)
SELECT gw_session_id, request_id, ts, request_status, success,
       is_final_success,
       CASE WHEN success_rank_desc = 1 AND success AND request_status = 'success'
            THEN 'KEEPER(newest success — suggest is_final_success=TRUE)'
            WHEN success AND request_status = 'success'
            THEN 'superseded(leave FALSE)'
            ELSE 'non-success(context)' END AS suggestion,
       client_request_id, request_type, tenant_id
  FROM ranked
 LIMIT 5000;

\echo '== [4/4] 建议执行的修正 SQL（人工审阅后复制执行；本脚本不执行任何写操作） =='
WITH candidates AS (
    SELECT rl.gw_session_id, rl.request_id,
           row_number() OVER (
               PARTITION BY rl.gw_session_id
               ORDER BY rl.ts DESC, rl.request_id DESC) AS rn
      FROM request_logs_with_current_month rl
     WHERE COALESCE(rl.gw_session_id, '') <> ''
       AND rl.success AND rl.request_status = 'success'
       AND NOT EXISTS (
           SELECT 1
             FROM request_logs_with_current_month claimed
            WHERE claimed.gw_session_id = rl.gw_session_id
              AND claimed.is_final_success)
)
SELECT format(
    'UPDATE request_logs_hot SET is_final_success = TRUE WHERE request_id = %L AND NOT EXISTS (SELECT 1 FROM request_logs_hot h WHERE h.gw_session_id = %L AND h.is_final_success AND h.request_id <> %L); -- keeper for session %s',
    c.request_id, c.gw_session_id, c.request_id, c.gw_session_id
) AS suggested_fix_sql
  FROM candidates c
 WHERE c.rn = 1
 ORDER BY c.gw_session_id
 LIMIT 1000;

\echo '注：keeper（每会话 ts 最新成功行）优先在 request_logs_hot 上补标；'
\echo '若该行已 promote（UPDATE 影响 0 行），需在对应月度分区上执行同样的'
\echo 'UPDATE（分区侧唯一性由写入侧 NOT EXISTS 语义保证，无唯一索引兜底的'
\echo 'columnar 分区请人工核对会话内无第二行 TRUE 后再执行）。'

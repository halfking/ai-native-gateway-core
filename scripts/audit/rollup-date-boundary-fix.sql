-- rollup-date-boundary-fix.sql
-- L87 对账快照日界统一修正：回填受 UTC/上海日历日混用影响的 7 天
--
-- 受影响日期（实测证据见主档 §六 D-01）：
--   11-11, 11-12, 11-13, 11-14, 11-15, 11-16（UTC 日界，应为上海日历日）
--   11-22（UTC 日界，应为上海日历日）
--
-- 修正方案：
--   1. 删除这 7 天的所有 scope 快照行（8 scope × 7 天 = 最多 56 行）
--   2. 通过管理端 API 或 worker 重新生成（会自动用上海日历日）
--   3. 验证新快照的请求数与 usage_facts 上海日历日窗口一致
--
-- 安全措施：
--   - DRY RUN：先 SELECT 显示将被删除的行
--   - 执行前备份：pg_dump -t report_snapshots
--   - 仅在 211 生产库执行（禁止在开发/测试库执行）

\set affected_dates '(''2026-11-11'', ''2026-11-12'', ''2026-11-13'', ''2026-11-14'', ''2026-11-15'', ''2026-11-16'', ''2026-11-22'')'

-- ==================== DRY RUN（先执行这部分） ====================

\echo '========== 受影响的快照行（将被删除） =========='
SELECT
    report_date,
    scope,
    scope_key,
    request_count,
    estimated_cost_cents / 100.0 as total_cost_usd,
    created_at
FROM report_snapshots
WHERE report_date::text IN :affected_dates
ORDER BY report_date, scope, scope_key;

\echo ''
\echo '========== 按 scope 分组统计 =========='
SELECT
    report_date,
    COUNT(*) as row_count,
    SUM(request_count) as total_requests,
    SUM(estimated_cost_cents) / 100.0 as total_cost_usd
FROM report_snapshots
WHERE report_date::text IN :affected_dates
GROUP BY report_date
ORDER BY report_date;

\echo ''
\echo '========== 预期：每天应有 8 个 scope（若为完整快照） =========='
\echo 'daily_total, daily_by_provider, daily_by_model, daily_grain,'
\echo 'internal_tenant, internal_person, internal_model, internal_grain'

-- ==================== 实际删除（确认 DRY RUN 结果后执行） ====================

-- 1. 备份建议（在 shell 执行）：
--    pg_dump -h 127.0.0.1 -p 15432 -U llm_gateway -t report_snapshots \
--      -a --inserts --data-only -f /tmp/report_snapshots_backup_$(date +%Y%m%d_%H%M%S).sql

-- 2. 执行删除（确认 DRY RUN 无误后取消注释）：
-- BEGIN;
--
-- DELETE FROM report_snapshots
-- WHERE report_date::text IN :affected_dates;
--
-- \echo ''
-- \echo '========== 删除完成，受影响行数见上方 DELETE 输出 =========='
-- \echo '下一步：通过管理端 API 重新生成这 7 天的快照'
-- \echo '  POST /api/admin/report-rollup/run'
-- \echo '  Body: {"date": "2026-11-11"} ... {"date": "2026-11-22"}'
-- \echo ''
-- \echo '或等待 worker 自动追赶（每天 02:30 UTC+8 检查缺失日期）'
--
-- COMMIT;

-- ==================== 验证（回填后执行） ====================

\echo ''
\echo '========== 验证：新快照请求数与 usage_facts 上海日历日窗口对齐 =========='
\echo '（执行前需先完成回填）'

-- 验证 11-11（示例，其余日期重复执行）
-- SELECT
--     rs.report_date,
--     rs.scope,
--     rs.request_count as snapshot_count,
--     (SELECT COUNT(*)
--      FROM usage_facts
--      WHERE occurred_at >= '2026-11-11 00:00:00+08'::timestamptz
--        AND occurred_at < '2026-11-12 00:00:00+08'::timestamptz
--        AND CASE
--              WHEN rs.scope IN ('daily_total', 'daily_by_provider', 'daily_by_model', 'daily_grain')
--              THEN true  -- 外部：全流量
--              ELSE traffic_class = 'business'  -- 内部：仅 business
--            END
--     ) as facts_count,
--     rs.request_count - (SELECT COUNT(*)
--                         FROM usage_facts
--                         WHERE occurred_at >= '2026-11-11 00:00:00+08'::timestamptz
--                           AND occurred_at < '2026-11-12 00:00:00+08'::timestamptz
--                           AND CASE
--                                 WHEN rs.scope IN ('daily_total', 'daily_by_provider', 'daily_by_model', 'daily_grain')
--                                 THEN true
--                                 ELSE traffic_class = 'business'
--                               END
--                        ) as diff
-- FROM report_snapshots rs
-- WHERE rs.report_date = '2026-11-11'
--   AND rs.scope IN ('daily_total', 'internal_tenant');

\echo ''
\echo '========== 预期结果 =========='
\echo 'diff 应全为 0，证明新快照已对齐上海日历日窗口'

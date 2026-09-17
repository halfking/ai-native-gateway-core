# Lane B — admin/ SQL 性能与风险审计（R37 重跑版，2026-09-17）

## 一、发现（候选）
| # | 级别 | 发现 | 证据 |
|---|---|---|---|
| 1 | P2 | listLogs 时间窗无上限 from=1970 即全表 COUNT+SUM | admin/logs.go:489,626-627,657-673; parseQueryTime 无跨度校验 |
| 2 | P2 | data-lifecycle stats 无时间窗全表 COUNT(WHERE 1=1) | admin/data_lifecycle.go:99,102-134 |
| 3 | P2 | turns/sessions 默认无窗 + 4 路 ILIKE '%%..%%' super_admin 全租户全表扫 | admin/turns_sessions.go:232-243,464-467 |
| 4 | P3 | routing decisions since_minutes 无上限;COUNT 错误吞为 0 | admin/routing.go:3131,3155-3176 |
| 5 | P3 | routing_audit_log 无默认窗;解析失败静默丢弃 | admin/audit_log.go:59-91,103 |
| 6-10 | P3 | 旧版 sessions COUNT(DISTINCT)+ILIKE 不可索引;session-analytics countQuery 无窗;usageDashboard 宽行 CTE;cleanup preview 空间估算外推;audit_log 行扫描失败静默 continue | session_list.go:103-160 / session_analytics_handler.go:310-320 / usage.go:200-256 / data_lifecycle.go:341-343 / audit_log.go:113-117 |

未发现 P1 级注入可达点；无 WHERE 的 UPDATE/DELETE；真 N+1 未发现。

## 二、健康面
ORDER BY 闸覆盖良好（allowedSort/ValidateOrderByColumn/switch 白名单/固定字符串）；热点聚合均强制时间窗（heatmap/usage/enhanced ≤366d）；ownerScopeClause 列名可信。

## 三、未覆盖
未连库验证执行计划；routing.go(5663 行)仅审区段；query/storage/db/web 未审。

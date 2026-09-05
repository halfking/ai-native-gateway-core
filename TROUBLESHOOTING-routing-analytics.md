# 路由分析页面 500 错误诊断与修复记录（已解决）

> 2026-08-31 更新：本页初版曾把根因误判为「视图缺失」（并留下了已废弃的
> `fix-missing-view.sql`，现已删除）。实际根因是**查询超时**，修复方案为
> migration 632 物化视图预聚合。本文为修正后的最终记录。

## 问题描述

前端访问 `https://llmgo.kxpms.cn/routing-v2?tab=analytics` 时，以下接口返回 500：

```
GET /api/admin/auto-route/analytics/flow?window=7d    → 500（15s 超时）
GET /api/admin/auto-route/analytics/matrix?window=7d  → 500（15s 超时）
GET /api/admin/auto-route/audit                       → 部分 500（10s 超时）
```

错误体：`{"error":{"message":"internal error (see server logs)","type":"admin_error"}}`

服务端日志（154 服务器 `/opt/llm-gateway-go/logs/gateway.log`）：

```
{"level":"ERROR","msg":"admin auto-route internal error","error":"timeout: context deadline exceeded"}
{"level":"ERROR","msg":"http_request","path":"/api/admin/auto-route/analytics/flow","status":500,"duration_ms":15000}
```

## 根本原因（最终结论）

**不是视图缺失**——`request_logs_with_current_month(_without_customer_id)` 视图存在。
真正的问题链：

1. 所有热力图/审计接口对 `request_logs_with_current_month_without_customer_id`
   做实时聚合（`COUNT/SUM/percentile_cont + GROUP BY`）。
2. 当月 columnar 分区 314K+ 行，聚合必然全分区扫描。
3. 索引无法解决 GROUP BY 成本；15s（analytics）/ 10s（audit）的 handler
   超时被击穿 → `context deadline exceeded` → 500。

## 修复方案：migration 632 物化视图预聚合

### 数据层

| 对象 | 用途 |
|------|------|
| `routing_analytics_7d` | 按小时 × 任务 × 模型 × 供应商 × 租户 预聚合，服务 matrix / flow |
| `routing_audit_summary_7d` | 按租户汇总 7 天总量，服务 audit |

关键实现细节（2026-08-31 审计修正）：

- **本仓库迁移由 Go 驱动**（`db.applyMigrationsOnce` → `db.ensureRoutingAnalyticsMaterializedViews`），
  `sql/migrations/startup/up/632_*.sql` 只是 DBA 镜像。只加 SQL 文件不会生效。
- `is_auto_request` 在视图中 `COALESCE(..., FALSE)` 归一化：否则 GROUP BY 的
  NULL/FALSE 两桶会撞上唯一索引的同一 COALESCE 键，`CREATE UNIQUE INDEX`
  报 duplicate key，**启动失败**。
- `effective_provider_id` 固化了与基础查询一致的
  `COALESCE(provider_id, credential 回退)`，保证 L2→L3 桑基图两条路径
  的 `unknown` 供应商占比一致。
- 唯一索引支撑 `REFRESH MATERIALIZED VIEW CONCURRENTLY`。

### 刷新机制（bg/materialized_view_refresher.go）

- 每 10 分钟 `REFRESH ... CONCURRENTLY`，不阻塞读取。
- `pg_try_advisory_lock` 防止 canary/正式多实例重复刷新。
- 初始延迟 30s 且可被 `Stop()` 中断（不阻塞优雅停机）。

### 查询层回退契约（admin/analytics_materialized.go）

- 仅当 `refreshed_at` 距今 < 15 分钟才信任物化视图（一次刷新周期 + 余量）。
- 视图缺失 / 为空 / 过期 / 查询出错 → 自动回退原基础视图查询：
  - refresher 挂掉 15 分钟后接口退回「慢但正确」的旧行为，不会给出错数字；
  - 24h 窗口始终走基础视图（物化视图只预聚合 7d）。
- 租户隔离：`handleAudit` 中 tenant id 以 `tenantLogsClause` 返回的
  **string** 提取；提取失败时**禁用**物化路径（防跨租户汇总泄漏），
  走带租户过滤的基础查询。

## 验证

```bash
# 视图存在且新鲜（refreshed_at < 15min 前）
psql ... -c "SELECT MAX(refreshed_at) FROM routing_analytics_7d;"

# 接口冒烟（154 服务器）
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
  "https://llmgo.kxpms.cn/api/admin/auto-route/analytics/flow?window=7d" | head -c 300

# 目标：P95 < 500ms，0 超时
```

数据一致性抽查（物化 vs 基础视图，diff 应 ≈ 刷新延迟内的增量）：

```sql
SELECT (SELECT SUM(request_count) FROM routing_analytics_7d) AS mv_total,
       (SELECT COUNT(*) FROM request_logs_with_current_month_without_customer_id
        WHERE ts >= NOW() - INTERVAL '7 days'
          AND (is_auto_request = TRUE
               OR (is_auto_request IS NOT TRUE AND client_model <> ''))) AS base_total;
```

## 回滚

```bash
# 1) 回滚代码后（handlers 自动回退基础查询，无需动库）
# 2) 如需彻底移除视图：
psql ... -f sql/migrations/startup/down/632_routing_analytics_materialized_view.down.sql
```

## 相关代码

- `db/db.go` — `ensureRoutingAnalyticsMaterializedViews`（真正执行迁移）
- `sql/migrations/startup/up|down/632_*.sql` — DBA 镜像
- `bg/materialized_view_refresher.go` — 定时刷新 + advisory lock
- `admin/analytics_materialized.go` — 快路径查询 + 新鲜度门控 + 回退
- `admin/analytics.go` / `admin/auto_route.go` — matrix / flow / audit 接入点
- `cmd/gateway/main.go` — refresher 装配（含 defer Stop）

## 遗留事项

- p95 为小时桶加权近似（非精确分位），对热力图场景可接受；若需精确值，
  在 MV 中存 `latency_ms` 直方图或升级 TimescaleDB/ClickHouse。
- 30d 窗口未预聚合（UI 未使用）；需要时按 632 模式扩展。
- `request_logs` 持续增长仍会影响基础回退路径与刷新成本，见
  分区归档策略（保留 90 天热数据）。

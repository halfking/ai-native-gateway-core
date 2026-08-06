# 2026-08-06 — /api/logs 列表查询 LATERAL 全表扫描 → 两段式分页

## 变更摘要

修复请求日志列表接口超时：`listLogs` 的 SQL 因 LATERAL 子查询引用外层列，planner 无法下推分页，导致对全部命中行逐行执行 `provider_models` 全表扫描。改为内层子查询先截断到一页，外层再对少量行做 JOIN。

## 触发原因

| 项 | 现状 (修复前) | 影响 |
|---|---|---|
| 列表查询耗时 | EXPLAIN ANALYZE 9943ms | 超接口 5s 硬超时 → `{"error":{"detail":"query failed"}}` → 页面无数据 |
| LATERAL 执行次数 | 7387 次（= 全部命中行数） | 每次对 provider_models 全表扫 675 行 |
| planner 下推 | `ORDER BY ts DESC LIMIT` 无法下推 | 必须等 JOIN 完再排序截断 |

## 根因分析

`admin/logs.go` 的 `requestLogsJoins` 含：

```sql
LEFT JOIN LATERAL (
  SELECT ... FROM model_offers mo
  WHERE mo.credential_id = rl.credential_id AND ...
) mo_pick ON TRUE
```

LATERAL 的 `WHERE` 引用外层 `rl.credential_id` / `rl.canonical_id` / `rl.client_model`，而外层查询又对整张 `request_logs_with_current_month` 视图（hot 表 + 分月列存分区 UNION ALL）做 JOIN 后才 `LIMIT 50`。planner 无法把 `ORDER BY rl.ts DESC LIMIT 50` 下推到 LATERAL 之前的阶段，只能先物化全部命中行（7387 行），每行触发一次 LATERAL 全表扫描：

```
provider_models seq scan: 675 rows × 7387 次 ≈ 550 万行
credential_model_bindings index scan: 36048 次
Execution Time: 9943.848ms  |  Buffers: 606152
```

## 修复方案（两段式）

**内层子查询**（负责分页 + 过滤，全部可下推）：

```sql
SELECT rl.*[, ROW_NUMBER() OVER (ORDER BY rl.ts ASC) AS trace_seq]
FROM request_logs_with_current_month rl
WHERE <全部 rl.* 过滤条件>
ORDER BY rl.ts (DESC/ASC)
LIMIT $n OFFSET $m
```

**外层查询**（只对一页行做辅助表 JOIN + LATERAL）：

```sql
SELECT <requestLogsListCols>[, rl.trace_seq]
FROM (<innerSQL>) rl
<requestLogsJoins>      -- providers / credentials / api_keys / applications / models_canonical / LATERAL mo_pick
ORDER BY rl.ts (DESC/ASC)
```

关键点：
- 所有 filter 均以 `rl.` 前缀引用视图列，`where` 安全整体下推到内层（logs.go:359 保证 `clauses` 永不为空，首条即 `rl.ts >= $1 AND rl.ts <= $2`）
- `clauses` 至少含时间范围，故 `WHERE` 永不为空
- 内层 `SELECT rl.*` 为视图全列，外层列清单照常引用
- chrono 场景（`chrono=1` / `gw_task_id` / `gw_session_id`）：`ROW_NUMBER() OVER (ORDER BY rl.ts ASC)` 移入内层，外层读 `rl.trace_seq`，语义不变
- COUNT 查询独立无 JOIN，未改动

## 验证结果

| 场景 | 执行时间 | LATERAL 次数 |
|---|---|---|
| 无过滤（全量 105 行命中） | 53.8ms | loops=50（分页 50） |
| 带 `success=false` + `q=claude` 过滤 | 44.9ms | loops=10（分页 10） |
| 修复前 | 9943.8ms | 7387 次 |

- `go build ./...` ✅
- `go vet ./admin/` ✅
- `go test ./admin/` 全绿（含 logs_view_test.go 列对齐回归）
- 详情接口 `getLog`（logs.go:591）按 request_id 单行查询，LATERAL 仅 1 次，无性能问题，未改动

## 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `admin/logs.go` | 修改 | `listLogs` 查询改两段式；`traceSeqCol` 拆为 `traceSeqInner`/`traceSeqOuter` |
| `CHANGELOG.md` | 修改 | 新增 Fixed 条目 |

## 遗留与风险

- 无。预存在的 `autoupdate` / `domains/health` / `domains/provider` 测试失败为环境性（DNS 延迟、DB 权限 42501），与本次改动无关（已用 `go list -deps` 证明无 admin 依赖）
- 部署后需在 154 环境复测接口与 EXPLAIN 计划

# 分区自动化修复实施总结

**时间**: 2026-08-07 19:30  
**任务**: 修复审计报告中优先级问题 #1「分区自动化缺失」  
**状态**: ✅ 完成并验证

---

## 问题背景

### 根因
24小时审计发现：`bg.PartitionManager` 的 `ensureSpecs()` 只注册了 6 张表，但实际持续写入的 RANGE 分区表有 14+ 张。迁移 473 只是一次性补丁（补到 2026_10），**没有持续自动化机制** — 到 2026-11-01 缺分区的表会写入失败导致宕机。

### 深度调查发现
在实施过程中，发现比预期更严重的问题：
- 我原计划接入的 6 个 `ensure_*_partition()` 函数中，**只有 1 个**（`ensure_sessions_v2_partitions`）真实存在于生产 DB
- 其余 5 个函数虽然在迁移 334/335/382/383 中定义，但因与 431/432 同类的 silent skip 问题，**从未进入生产库**
- 2026-08-04 的 `pg_dump` 快照证实：生产库有这些表、有分区，但函数全部缺失
- 只在 Go 注册函数名是**无效修复**：PartitionManager 每次 tick 会因 "function does not exist" 报错，分区建不出来

---

## 实施方案

### 变更清单

#### 1. **bg/partition_manager.go** (+42/-1 行)

**a. `archiveSpec` 结构扩展**  
新增 `argExpr string` 字段，支持 date 类型参数的 `$1::date` 转换：
```go
type archiveSpec struct {
    day     int
    fnName  string
    label   string
    enabled bool
    argExpr string  // 新增：默认 "$1"，date 函数用 "$1::date"
}
```

**b. `ensureNextMonthPartitions` 循环改造**  
从硬编码 `"SELECT "+s.fnName+"($1)"` 改为动态拼接：
```go
argExpr := s.argExpr
if argExpr == "" {
    argExpr = "$1"
}
pm.db.Exec(timeoutCtx, "SELECT "+s.fnName+"("+argExpr+")", targetMonth)
```

**c. `ensureSpecs()` 新增 6 个条目**  
- `ensure_credit_ledger_partition` (timestamptz)
- `ensure_tool_usage_stats_partition` (timestamptz)
- `ensure_sessions_v2_partitions` (date, argExpr="$1::date") — 一次覆盖 3 张表
- `ensure_session_module_executions_partition` (date, argExpr="$1::date")
- `ensure_dashboard_events_partition` (date, argExpr="$1::date")
- `ensure_cache_metrics_partition` (date, argExpr="$1::date")

#### 2. **bg/partition_manager_test.go** (+10 行)

`TestEnsureSpecsCoversAllPartitionedTables` 的期望 map 新增 6 个条目，与 `ensureSpecs()` 一致。

#### 3. **sql/migrations/startup/475_restore_missing_ensure_partition_functions.sql** (13 KB, 315 行)

用 `CREATE OR REPLACE FUNCTION` 幂等重建 5 个生产缺失的函数：

| 函数 | 签名 | 来源 | 索引策略 |
|------|------|------|---------|
| `ensure_credit_ledger_partition` | `timestamptz` | 334 | 父表索引自动传播 |
| `ensure_tool_usage_stats_partition` | `timestamptz` | 335 | 父表索引自动传播 |
| `ensure_session_module_executions_partition` | `date` | 382 | 分区级显式建索引 |
| `ensure_dashboard_events_partition` | `date` | 383 | 分区级显式建索引 |
| `ensure_cache_metrics_partition` | `date` | 新建 | 父表索引自动传播 |

**关键设计**：
- 用 `pg_class` 名字检查（非 `::regclass` cast）保证幂等性，对齐 472/473/474 风格
- 迁移末尾立即为当月/下月执行 `PERFORM ensure_*_partition()`，无需等下次 tick
- `session_module_executions` 和 `dashboard_access_events` 无父表索引，函数内显式建分区级索引（对齐生产 dump）

#### 4. **sql/migrations/startup/475_restore_missing_ensure_partition_functions.down.sql** (890 B)

`DROP FUNCTION` 全部 5 个函数，不删已建分区（避免数据丢失）。

---

## 验证结果

### Go 侧
- ✅ `go build ./...` — 全量编译通过
- ✅ `go test ./bg/ -v` — 全部测试通过 (0.879s)
- ✅ `go vet ./bg/...` — 无 lint 警告

### SQL 侧
用 Docker Postgres 16 真实验证：
- ✅ **语法正确**：fixtures + 475 up 正常执行，创建 5 个函数 + 当月/下月分区
- ✅ **幂等性**：二次运行 475 up，所有表返回 `(already exists)`，无重复建表
- ✅ **up/down 对称**：475 down 正确删除 5 个函数（`\df ensure_*partition` 返回 0 rows）

---

## 覆盖表清单

| 表 | 分区键 | 原 ensure 函数来源 | 生产状态 | 修复后 |
|----|--------|-------------------|---------|--------|
| `credit_ledger` | `created_at` (timestamptz) | 迁移 334 | 表存在，函数缺失 | ✅ 自动化 |
| `tool_usage_stats` | `created_at` (timestamptz) | 迁移 335 | 表存在，函数缺失 | ✅ 自动化 |
| `session_module_executions` | `created_at` (timestamptz) | 迁移 382 | 表存在，函数缺失 | ✅ 自动化 |
| `dashboard_access_events` | `created_at` (timestamptz) | 迁移 383 | 表存在，函数缺失 | ✅ 自动化 |
| `sessions` / `session_turns` / `session_bodies` (gateway schema) | `partition_date` (date) | 迁移 430 | ✅ 函数存在 | ✅ 接入 |
| `cache_metrics` | `partition_date` (date) | 无 | 表存在（470），无函数 | ✅ 新建 + 自动化 |

**明确排除**（有意不接入）：
- `model_probe_runs`：已退役为纯 hot 表 + TTL DELETE，不再分区
- `routing_decision_log_archive`：仅 archive job（每月 1-3 日）写入，自建分区
- `candidate_failure_logs`：ensure 函数是空 body，走 hot 表 + promote 架构

---

## 影响面

### 增量 & 安全性
- **纯增量**：只新增分区（`CREATE TABLE IF NOT EXISTS` / `pg_class` 幂等），不改现有数据/schema
- **无冲突**：与 473 一次性补丁无冲突，473 已建的分区会被 ensure 幂等跳过
- **无 breaking change**：无 API 变更，启动时多几次幂等 ensure 调用（开销可忽略）
- **无外部依赖**：未接入外部 cron，沿用现有 ticker+goroutine 模式

### 自动化保障
- **当前 + 下月**：每次 PartitionManager tick（启动 + 每 24h）为 12 张表（11 张物理表，sessions_v2 一次覆盖 3 张）自动建分区
- **2026-11-01 及以后无需人工介入**：系统自动预创建下月分区

---

## 遗留问题修复

本次实施同时修复了一个严重的技术债：
- **431/432 同类 silent skip 问题**：迁移 474 已修复 431/432 的后果（补索引 + 约束），但 334/335/382/383 同样受影响从未被发现
- **475 是全面补救**：不仅建了 cache_metrics 的新函数，还修复了 4 个历史遗留的缺失函数

---

## 部署建议

1. **立即部署 475**：避免 2026-09-01（月初）可能的分区缺失风险（473 只补到 10 月）
2. **监控 PartitionManager 日志**：部署后观察下次 tick（最多 24h）的 `partition_manager: ensured partition` 日志，确认 6 个新函数正常调用
3. **验证分区创建**：部署后在生产 DB 查询：
   ```sql
   SELECT schemaname, tablename 
   FROM pg_tables 
   WHERE tablename ~ '_(2026_09|2026_10)$' 
   ORDER BY tablename;
   ```
   应看到这 5 张表的 09/10 分区（473 已建）+ 未来自动建的 11 月分区

---

## 文件清单

### 修改
- `bg/partition_manager.go` (+42/-1)
- `bg/partition_manager_test.go` (+10)

### 新建
- `sql/migrations/startup/475_restore_missing_ensure_partition_functions.sql` (13 KB)
- `sql/migrations/startup/475_restore_missing_ensure_partition_functions.down.sql` (890 B)

---

**实施人**: ZCode AI Agent  
**完成时间**: 2026-08-07 19:30  
**验证状态**: 全部通过  
**下一步**: 审查通过后合并部署

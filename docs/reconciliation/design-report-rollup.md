# 对账报表设计（Report Rollup）

> 范围：**MVP 切片**——日报（每日 02:00 定时、可配） + 两个 sheet（用量 + 模型质量与错误分析） + Excel 导出。
> 不在本切片：周/月/自定义区间聚合 API、租户/人员独立视角切换、内部价模型（vs 供应商价）。这两块留后续轮。

## 1. 与现有 reconciliation 的关系

| 现有 | 本切片 |
|---|---|
| `domains/stats/reconciliation.go`（usage_facts vs stats_usage_daily 比对 + 调整入库） | 不动 |
| `domains/stats/daily_monthly_rollup.go`（天/月投影） | 不动（仅作数据源参考） |
| `bg/cost_reconciliation_worker.go`（provider 月级成本比对） | 不动 |
| `bg/feedback_analyzer.go`（02:00 反馈分析） | 不动 |
| `usage_facts`（真相源） | **直接读，作为日报聚合来源** |
| `stats_usage_daily` | 不直接用，**日报独立聚合**避免耦合 |

`report_snapshots` 是**独立快照表**，与 stats_reconciliation 审计轨不冲突——前者是"已聚合可下载报表"，后者是"对账审计轨迹"。

## 2. 数据模型

### 2.1 `report_snapshots`（报表快照元数据，迁移 745）

| 列 | 类型 | 说明 |
|---|---|---|
| id | bigserial PK | |
| scope | text NOT NULL | `provider` / `tenant` / `user`——按 MVP 阶段固定 provider |
| scope_key | text NOT NULL | 对 scope 的具体 id：`provider:<id>` 等 |
| report_date | date NOT NULL | 报表对应日期（UTC） |
| generated_at | timestamptz NOT NULL DEFAULT now() | 生成时间 |
| generation_status | text NOT NULL | `pending` / `completed` / `failed` |
| error | text | 失败原因 |
| sheet_usage | jsonb NOT NULL | sheet1（用量）汇总 JSON：{ provider, model, day, request_count, prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cost_amount, cost_currency } |
| sheet_quality | jsonb NOT NULL | sheet2（模型质量+错误分析）JSON：{ provider, model, day, success_count, failure_count, p50_latency_ms, p95_latency_ms, error_kind_breakdown: {kind: count} } |
| pricing_version | text | 用于解释成本列所用价格版本 |

UNIQUE(scope, scope_key, report_date)，便于 ON CONFLICT 幂等回填。

### 2.2 `report_snapshots_hot` + 5 分区

按 report_date 月度分区，仿 `request_logs_hot`。含 hot 行（最近 30 天）+ 默认分区。
- 用 `report_date` 分区（数据按月增长慢，分区主要为了保留清理 hook）
- 不做索引层覆盖太多：MVP 阶段靠 `idx (scope, report_date DESC)` 一条索引即可

## 3. Worker

`bg/report_rollup_worker.go`：
- 启动时立即跑一次"昨天"的报表（参考 `bg/cost_reconciliation_worker.go` 的"开启即跑当月"语义）
- 之后按 cron（默认 `0 2 * * *` UTC，本地 02:00 跟随 docker 容器时区）触发
- cron 表达式从平台 setting `reports.daily_rollup.cron` 读取；空 / 不合法时 fallback 到默认
- 每次只聚合"昨天的 UTC 日"——边界 = `[report_date 00:00 UTC, report_date+1 00:00 UTC)`
- 写入采用 `INSERT ... ON CONFLICT (scope, scope_key, report_date) DO UPDATE`，可重入

数据查询：
```sql
-- sheet_usage: 按 provider + canonical_id + raw_model_name 聚合
SELECT provider_id, canonical_id, raw_model_name,
       occurred_at::date AS day,
       COUNT(*) AS request_count,
       SUM(prompt_tokens) AS prompt_tokens,
       SUM(completion_tokens) AS completion_tokens,
       SUM(cache_read_tokens) AS cache_read_tokens,
       SUM(cache_write_tokens) AS cache_write_tokens,
       SUM(cost_amount) AS cost_amount,
       MAX(cost_currency) AS cost_currency
FROM usage_facts
WHERE occurred_at >= $1 AND occurred_at < $2
GROUP BY 1,2,3,4;
-- 仅成功请求（cost > 0）— 失败请求单独走 sheet_quality
```

失败原因分类：依赖 `usage_facts.error_kind` 与 `error_class` 字段已经存在；worker 仅 `GROUP BY error_kind` 形成透视列。

## 4. API

`admin/report_rollup.go`：

| Method | Path | 用途 |
|---|---|---|
| GET | `/api/admin/report-rollup/snapshots?date=YYYY-MM-DD&scope=provider` | 列出该日所有快照 |
| GET | `/api/admin/report-rollup/snapshots/{id}` | 读单个快照详情（带 sheet JSON 完整 payload） |
| GET | `/api/admin/report-rollup/snapshots/{id}/export.xlsx` | 下载 Excel 双 sheet 文件 |

`scope=provider` 是 MVP 唯一值；切租户/人员视角留 follow-up。

权限：`wrap(superAdminMiddleware)`。

## 5. Excel 导出（excelize）

`domains/reportrollup/xlsx.go`：
- 输入：`(snapshot ReportSnapshot)`、`(writer io.Writer)`
- 流程：
  1. `f := excelize.NewFile()`
  2. sheet1 "用量"：表头 `provider_id | provider_name | canonical_id | raw_model_name | day | request_count | prompt_tokens | completion_tokens | cache_read_tokens | cache_write_tokens | cost_amount | cost_currency`，逐行写
  3. sheet2 "模型质量与错误分析"：表头 `provider_id | canonical_id | raw_model_name | day | success_count | failure_count | p50_latency_ms | p95_latency_ms | rate_limit | context_length | upstream_5xx | credential_broken | client_error | other`，按 `error_kind` 透视
  4. `f.Write(w)` 流式写

excelize 已在 `go.sum`（间接依赖），需在 `go.mod` 提升为直接依赖（`go get github.com/xuri/excelize/v2`）。

## 6. Platform Setting

`settings/spec_lifecycle.go` 加：
```go
{Key: "reports.daily_rollup.cron", Default: "0 2 * * *", Description: "..."}
```

## 7. 失败分类字段假设

`usage_facts.error_kind` 是文本字段，预期值取自 `errorsx/classify.go` 的 `ErrorKind` 枚举（rate_limited/context_length/credential_fatal/client_bug/server_error/...）。worker 不强校验，按字符串自由分组；后续若发现脏数据可在 sheet2 加 "unknown" 桶。

## 8. 不在 MVP 范围（留 follow-up）

- 周 / 自定义区间聚合（任何区间都重读 `usage_facts` 走 GROUP BY 可行，但 API 与权限需另设计）
- 租户/人员独立视角报表（同源双视图方向，先 vendor 视角落地，租户视角 = 多 scope 列）
- 内部价表（`internal_pricing` / `tenant_pricing`）——目前 sheet_usage 的 cost 全部按供应商成本读出，与用户目标里"按内部价重算"的需求脱节。该项需要新增表 + 价格版本管理 + 双价重算
- 与 `stats_reconciliation_runs` 串联（自动用 snapshot 数据作为 source value 比对 daily_rollup）
- Webhook 通知（日报失败告警）

## 9. 验证

- `go build ./...` 必须过
- `go vet ./...` 必须过
- `go test ./domains/reportrollup/...` 必须过（含 xlsx 落盘回读单元测试）
- `go test ./bg/... -run TestReportRollup` 必须过（worker 路径用 sqlmock 或 testfixtures）
- 集成测试：迁745 落地后，灌 10 条 usage_facts → 触发 worker → 验证 snapshot 存在 → GET Excel → 校验两个 sheet 行数
- **迁移号三重查重断言**：实施前 `git log sql/migrations/startup/74{3,4,5}_*.sql` 必须只命中 743/744（既有），命中 0 条 745 文件；`go test ./...` 通过 ≥492（含 installer 自检）；共享 252 PG 账本 `SELECT filename FROM schema_migrations WHERE filename LIKE '74%'` 不含 745（建号前查一次）

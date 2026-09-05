# 工作包 6：P3 清理包 + installer embed 补注册（659/660）

执行分支：main（未 commit，全部改动留在工作区）。
日期：2026-09-05。

## 一、installer embed 补注册（审计 D-2#3 同族缺口）

背景：659/660 已合入 main（`sql/migrations/startup/`）但 installer 从未注册，新装实例拿不到新迁移体。对照 656/657/658 既有写法补齐。

### 注册清单

| 位置 | 改动 |
|---|---|
| `installer/cmd/llm-gw-installer/embeddata/startup/659_legacy_promote_atomic_cte.sql` | 新增（从 canonical 拷贝，仅 up；惯例 down 不入 installer 资产） |
| `installer/cmd/llm-gw-installer/embeddata/startup/660_credential_model_weekly_peak_unique.sql` | 新增（同上） |
| `installer/cmd/llm-gw-installer/main.go`（go:embed 变量区，原 :256-257 后） | 新增 `legacyPromoteAtomicCTEMigration659`、`credentialModelWeeklyPeakUniqueMigration660` 两个 `//go:embed` 变量 |
| `installer/cmd/llm-gw-installer/main.go` copySQLBackup map（db/init 备份路径） | 新增 `startup/659_...`、`startup/660_...` 两条目 |
| `installer/cmd/llm-gw-installer/main.go` setupSQLDir map（应用路径） | 新增同两条目 |
| `installer/cmd/llm-gw-installer/stats_migrations_test.go` expected map | 新增 659/660 条目（同时补 658，见下） |

### 超出 659/660 的同族缺口修复（均属 D-2#3 同一缺陷家族，在 installer/** 权限内）

1. **658 未入 `dbinit.Runner.StartupFiles`**：658 此前只在两个 embed map 里，runner（fresh install 唯一迁移应用路径，`InitSchema` 按 `StartupFiles` 顺序 psql 应用）从未引用它 → 新装实例永远不应用 658。已在 `installer/internal/dbinit/runner.go` StartupFiles 补 `658_auto_route_structured_features.sql`（657 之后、bootstrap 之前）。
2. **563 embed 落后 canonical（HEAD 上即红）**：`TestStatsStartupMigrationsMatchCanonicalSources` 在未改动前即 FAIL——canonical 563 已由 2e178ca5d（22003 溢出根因修复，DECIMAL(10,6)→numeric）更新，installer embeddata 未同步，新装实例拿到的是会回滚 request_logs 事务的旧触发器体。已将 canonical 拷贝覆盖 `embeddata/startup/563_session_summary_trigger_on_hot.sql`。
3. 测试 expected map 同步补 `658_auto_route_structured_features.sql`（canonical 字节比对覆盖）。

660 幂等性核验：`CREATE UNIQUE INDEX IF NOT EXISTS` + DO 块校验；659 为 CREATE OR REPLACE FUNCTION 体（656 单 CTE 模板）；二者在 01-schema 基线之后可安全用于新装。

## 二、P3 死代码逐项处置表

| # | 项（审计出处） | 验证方式 | 动作 | 备注 |
|---|---|---|---|---|
| B1a | `internal/ir/serialize_anthropic.go` `mapEffortToBudget`（Deprecated 零调用，axis-A 清理清单） | 全仓 grep 仅定义处 + 一处历史注释 | 删函数 | :108 历史注释按原样保留（仅作变更背景说明） |
| B1b | `internal/ir/parse_responses.go` :410-427 第二个 `function_call_output` 分支（A-#11） | 逐路径确认 ：323 早退分支对同一 `item["type"]` 必然先返回，后置分支不可达 | 删分支（含内部 `if _, ok := item["output"]` no-op），留注释指向早退分支 | 测试语义不变（早退分支产出完全相同结构） |
| B1c | `msg["source"]` no-op 特例（`_ = source`，axis-A 清理清单标 parse_openai.go:295-299） | grep 定位实际在 `parse_anthropic.go:295-299`（审计文件归属偏差，内容完全吻合） | 删 | 纯 no-op，行为零变化 |
| B1d | `internal/ir/serialize_openai.go` `requestIDFromIR` 恒返回 "unknown"（axis-A 清理清单） | 确认 ~40 个调用点全部是 `ReportProtocolLoss` 首参；包内他处本就直传 `"unknown"`/`""` 字面量 | 4 个 serialize_* 文件 sed 替换为字面量 `"unknown"` + 删函数 | IR 尚无显式 RequestID 字段；如未来要真实现应在 types.go 加字段，不属于死代码清理范畴 |
| B1e | `serialize_anthropic.go` serializeAnthropicMessages 文档注释整块重复（axis-A 清理清单） | 目检 ：359-364 | 删重复块 | |
| B2 | E-#7：`domains/routing/candidate_failure_logger.go` 重复 CandidateFailureWriter（无双写 supplier_errors_hot、无 SanitizeErrorText） | 全仓 grep：`NewCandidateFailureWriter` 生产零引用（生产 wiring 在 cmd/gateway/main.go:1933 用 executors 版）；文件内 helper（unwrapErr/recoveryContext/marshalContext）包内无他处使用 | 删 `candidate_failure_logger.go` + `candidate_failure_logger_test.go`；`go build/vet/test ./domains/routing/` 通过 | |
| B3 | C-#20：`sortPriorityClusters` 恒空转排序 + `CredentialRef.PriorityCluster` 死字段（axis-C 清理清单；实际文件在 `domains/dispatch/`，非审计所写 executors/ 路径） | 全仓 grep（含 *.sql/*.json）：candidateToRef（executor_dispatch.go:207-234）不填该字段、无任何赋值点、无 json tag/DB 列/序列化依赖；排序在恒零值上 O(n log n) | 删 dispatcher.go `selectAndEnqueue` 内排序调用 + `sortPriorityClusters` 函数（含 `sort` import）+ 字段 + `TestPriorityClusterSortPreservesOrderWithinCluster` | 无序列化/DB 依赖，故按全删处理（未走 deprecated 标记分支） |
| B4 | `domains/streaming/executors/router.go` `planLegacy` deprecated 占位（axis-C 清理清单；实际在 :986-1007） | 全仓 grep 零调用 | 删函数，留删除记录注释 | |
| B5 | `bg/auto_route_affinity_worker.go` prevAvgReward 扫出未使用（axis-H 五） | 目检 ：258-263：仅 prevEMA 被 `UpdateEMA` 消费 | SELECT 裁掉 `COALESCE(avg_reward,0)` 列，Scan 同步改单目标 | |
| B6 | `domains/streaming/handler_autocombo.go` emptySet 构建后仅 `_ = emptySet`（axis-H 五） | 目检 :207/:211/:235：无任何读路径 | 删 map + 写入 + `_ =` 行；`continue` 前留注释说明外层 len() 兜底 | |
| B7 | `autoroute/decision.go` :687 buildReason `nolint:unused` 遗留（axis-H 五；实际函数在 `autoroute/classifier.go:731`，decision.go 无此函数） | 全仓 grep：调用方全部走 `buildReasonEx`（classifier.go:609/:732），`buildReason` 零引用含测试 | 删死包装函数（非仅摘标记） | |
| B8 | `cmd/autoclass-bench/testdata/samples.seed.jsonl` 含 "planning" 标签样本（axis-H 五 + H-6） | 核对生产 LLM fallback prompt（classifier_llm.go buildClassificationPrompt 固定 10 类）不含 planning → bench prompt 枚举该标签会使基准偏离生产 fallback 行为。注：启发式层 `TaskPlanning`/`IsPlanningRequest` 仍存在于 AllTaskTypes，未动 | 删 5 行 planning 样本（45→40 条）；labels 由样本派生（deriveLabels），main.go 无行数断言；同步更新 testdata/README.md（40 条/8 类 + 移除原因说明）；`go test ./cmd/autoclass-bench/` 通过 | |

## 三、验证输出

```text
# 主模块
go build ./...                      → OK (exit 0)
go vet ./internal/ir/... ./bg/... ./domains/... ./autoroute/... ./cmd/autoclass-bench/...
                                    → OK（含 domains/streaming/executors 单独 vet OK）
go test ./internal/ir/... ./domains/streaming/executors/... ./bg/... ./domains/routing/...
        ./domains/streaming/... ./autoroute/... ./cmd/autoclass-bench/... -count=1
                                    → 全部 ok（含 ir / executors / bg / routing / streaming
                                      72s / autoroute / autoclass-bench）

# installer 模块（独立 go.mod，需在 installer/ 下）
go build ./...                      → OK
go test ./... -count=1              → 13 个包全部 ok（含 cmd/llm-gw-installer、internal/dbinit）
```

### bg 包两个失败项归属说明（非本包改动）

执行期间另一会话并发修改了禁改清单内的 `bg/provider_error_aggregator.go`、
`bg/supplier_error_stats_aggregator.go` 及其测试（E-#1/E-#3/E-#4/E-#6 修复包）。
`TestProviderErrorAggregatorAggKeyMigrationDropsMessageFromFingerprint` 与
`TestSupplierErrorStatsRollupSQLContract` 在其半成品工作区上失败（断言 662 索引 /
STAGE_COUNTS rollup 文本）；stash 掉工作区改动后两测试即 PASS，证明失败纯由该会话
in-flight 编辑引入，与本工作包无关（本包在 bg/ 仅改 auto_route_affinity_worker.go 3 行）。
跳过这两个测试后 bg 包其余全部 ok。

## 四、改动文件清单

installer/**：
- `installer/cmd/llm-gw-installer/main.go`（embed 变量 ×2 + 两 map 各 ×2 条目）
- `installer/internal/dbinit/runner.go`（StartupFiles +658/659/660）
- `installer/cmd/llm-gw-installer/stats_migrations_test.go`（expected map +658/659/660）
- `installer/cmd/llm-gw-installer/embeddata/startup/{659_legacy_promote_atomic_cte,660_credential_model_weekly_peak_unique}.sql`（新增）
- `installer/cmd/llm-gw-installer/embeddata/startup/563_session_summary_trigger_on_hot.sql`（同步 canonical 22003 修复）

主模块：
- `internal/ir/serialize_anthropic.go`（mapEffortToBudget 删、注释去重）
- `internal/ir/serialize_openai.go`（requestIDFromIR 删 + 调用点直连）
- `internal/ir/serialize_gemini.go` / `serialize_responses.go`（调用点直连）
- `internal/ir/parse_responses.go`（死分支删）
- `internal/ir/parse_anthropic.go`（source no-op 删）
- `domains/routing/candidate_failure_logger.go` + `_test.go`（删）
- `domains/dispatch/{dispatcher,queued_request,priority_affinity}.go` + `priority_affinity_test.go`（C-#20）
- `domains/streaming/executors/router.go`（planLegacy 删）
- `domains/streaming/handler_autocombo.go`（emptySet 删）
- `bg/auto_route_affinity_worker.go`（prevAvgReward 列裁剪）
- `autoroute/classifier.go`（buildReason 死包装删）
- `cmd/autoclass-bench/testdata/samples.seed.jsonl`（-5 planning 行）
- `cmd/autoclass-bench/testdata/README.md`（40 条/8 类 + planning 移除原因）

未触碰禁改清单（cmd/gateway/**、bg 两个聚合器、web/**、deploy/** 等）。未执行 git add/commit。

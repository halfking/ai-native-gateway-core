# 2026-09-05 八项审计闭环实施报告

> 基线：origin/main = `9abce0caa`（实施前已 fetch 确认本地 HEAD 与远端一致）
> 关联审计：`docs/audit/2026-09-05-ir-storage-provider-audit.md`「未闭环项」全部八条

## 结论摘要

八项优先闭环全部完成，每项配套回归测试与运行态证据：

| # | 闭环项 | 状态 | 核心证据 |
|---|--------|------|----------|
| 1 | supplier_errors 事实源 | ✅ | V371 迁移 + 统一写入 + 趋势 API + 读端切换；真实 PG 集成矩阵通过 |
| 2 | 候选错误诊断字段+脱敏 | ✅ | 契约/脱敏测试（pgxmock + 捕获 fake）；dispatch Attempts 恒空缺口修复 |
| 3 | durable DecisionHistory | ✅ | 迁移 657 + fenced store 方法 + worker 迁移 WithHistory；跨重启续写测试 |
| 4 | lite 跨介质一致性 | ✅ | Reconcile/Repair 补偿原语 + 重启恢复/孤儿检出/清理测试 |
| 5 | hot+columnar 真实 PG | ✅ | kx-citus-pg17 实测×3：迁移幂等、分区不可变（真实报错）、8h 保留、批量迁移、RLS |
| 6 | 真实网络故障/泄漏 | ✅ | refused/reset/EOF/DNS/TLS/slow-read 六类注入 + SSE 取消（10.27s→0.25s）+ 轮次增长泄漏门禁 |
| 7 | P99 性能门禁 | ✅ | 热路径池化：p99 175µs~2.3ms(破线)→稳定 ~134µs；新增分配门禁；1ms 阈值未动 |
| 8 | 前端统一/远程菜单校验 | ✅ | credentialDisplayName 统一、结构化错误徽标、plugin-nav schema 校验（fail closed） |

## 闭环1：supplier_errors_hot / supplier_error_stats 唯一事实源

**写入**（`domains/streaming/executors/supplier_error_logger.go`）：
`CandidateFailureWriter.logFailure` 是唯一写入函数——同一行数据双写
`candidate_failure_logs_hot`（旧读端，迁移期保留）与 `supplier_errors_hot`
（V371 事实源）。低基数维度（supplier=provider catalog code、
error_type=ErrorKind、stage=失败阶段枚举）有防自由文本污染守卫；
error_message 与 request_metadata 字符串值经 `errorsx.SanitizeErrorText`
脱敏。dispatch 三个调用点注入 `extra["supplier"] = cand.CatalogCode`。

**聚合**（`bg/supplier_error_stats_aggregator.go`）：每 5 分钟对
supplier_errors_hot 按分钟桶 UPSERT 到 `supplier_error_stats`
（V371 唯一键防重复聚合），重算最近两个窗口覆盖迟到行。已接入
PartitionManager 生命周期。

**查询**：
- 新增 `GET /api/errors/trend`（`admin/errors_trend.go`）：stats 主路径 +
  unified 明细兜底（聚合器刚部署时），响应含 time_series/summary/
  top_error_types/top_suppliers；
- 凭据错误详情（`admin/vendor_credential_error_handlers.go`）读端切换到
  `supplier_errors_unified`（hot ∪ historical，security_invoker），
  响应新增 supplier/error_code/retryable/stage 结构化字段；
- lifecycle `hotPromoteTableMap` 注册 `supplier_errors_hot` →
  `promote_supplier_errors_hot_to_partition`（默认 8h + 5000 批次）。

**前端**：`vendor-credential-error.ts` 类型同步；ErrorDetailTab 新增
阶段/重试/耗时列（结构化徽标）。

## 闭环2：候选错误诊断字段补齐

**字段**：`CandidateOutcome` 补 RequestID/AttemptSeq/Supplier/HTTPStatus/
Retryable/LatencyMs/Stage；`AttemptRecord`、`RoutingAttempt`（JSONB 契约，
omitempty 向后兼容）补 Supplier/HTTPStatus/LatencyMs/Stage 与
ErrorKind/Stage/Retryable。

**缺口修复**（`execute_attempt.go`）：全仓 grep 证明 `ExecuteError.Attempts`
在生产代码从未被填充——dispatch_v2 路径真实多候选失败在聚合器眼里是
`no_candidate_outcomes` → fail-closed。`foldCandidateOutcomes` 现在从
`RoutingAttemptsTracker.FailedAttempts()`（排除 pending 占位）恢复真实
失败候选并补齐全部诊断维度；无 tracker 时保持原合成行为。

**脱敏测试**：`supplier_error_logger_test.go` 恶意上游（错误消息/响应体
回显 Bearer/sk-*/api_key=/query token）不得进入 error_message 与
request_metadata；写入失败 best-effort 不影响旧表双写。

## 闭环3：durable task DecisionHistory 持久化契约

- startup 迁移 657：`durable_llm_tasks` 增加 `decision_history JSONB`；
- `durable.Store` 新增 `LoadDecisionHistory`/`SaveDecisionHistory`
  （(lease_owner, fencing_token) fencing 条件，0 行影响返回 ErrLeaseLost）；
- `DurableRecoveryWorker` 从 `AggregateTaskOutcome` 迁移到
  `AggregateTaskOutcomeWithHistory`：接管时读回历史（失败/损坏退化为空
  历史，等价旧行为——审计要求先落地持久化载体再做非伪迁移），attempt
  结束后 `appendSurvivalHistory`（128 条有界）追加并写回；
- 回归：attempt 后历史落库（kind/provider/attempt_no/seq 单调）、跨进程
  重启续写（LastSeq 前进、条目累加）、保存失败不改变本判定。

## 闭环4：lite 跨介质一致性与重启恢复

- `storage/consistency.go`：`ReconcileTurnArtifacts`（对比 SQLite turn
  meta 与文件 body 两侧 turn 集合，输出结构化报告）与
  `RepairTurnArtifacts`（孤儿 body 可删；缺失 body 只报告绝不伪造）；
- `FileBodiesStore` 补 `ListTurns`/`DeleteTurnFile`；
- `storage/consistency_lite_test.go`：重启（关闭重开）后 session 行/
  turn meta/body 内容无损往返；「body 落盘后 meta 提交前崩溃」孤儿被
  检出且 Repair 清理；「meta 已提交 body 丢失」被检出且任何策略不伪造；
  gzip 中文大内容无损。

## 闭环5：hot+columnar 真实 PostgreSQL 集成验证

`deploy/sql/verify/supplier_errors_pg_test.go`（DSN 门控，离线 skip），
在真实 PostgreSQL 17 + citus_columnar 13.3（本仓库 kx-citus-pg17
offline-arm64 镜像）上全部通过（×3 轮）：

- V371 迁移真实执行（含迁移内建验证 DO 块）且重放幂等；
- hot 表 heap 语义（INSERT/UPDATE/DELETE）；
- **历史分区不可变**：columnar 子分区 UPDATE/DELETE 被拒，真实错误：
  `ERROR: UPDATE and CTID scans not supported for ColumnarScan (SQLSTATE 0A000)`
  ——repair 工具不得修改 columnar 的运行态依据；
- `promote('8 hours', 2)`：只迁移超窗口行、批次上限生效、fresh 行留在
  hot（8h 保留语义）、迁移后 unified 视图跨 hot+historical 可读；
- RLS 以非 superuser 角色探测（superuser 绕过 RLS，FORCE 仅约束属主），
  `app.tenant_id` 隔离生效；
- `supplier_error_stats` 唯一键 UPSERT 幂等；'' 维度去重（PG UNIQUE
  对 NULL 不去重，故维度列 NOT NULL DEFAULT ''）。

**由真实验证发现并修正的迁移缺陷**：
1. 父表 id 从 GENERATED ALWAYS 改为普通 bigint（V359 模板模式）——
   GENERATED ALWAYS 拒绝 promote 的显式 id 插入，EXCEPTION 吞掉后在
   copy-delete-insert 顺序下造成批次行丢失；
2. 去掉 `WITH (compression=zstd, ...)` reloption（columnar 11.2+ 报
   `unrecognized parameter`，参数走 columnar.* GUC），对齐 V359。

## 闭环6：真实网络故障注入 / SSE 可取消 / goroutine 泄漏

**传输层**（`upstream/client_fault_test.go`，裸 net.Listen 控制故障）：

| 故障 | 手法 | 实际分类 | 可重试 |
|------|------|----------|--------|
| refused | listen 后关闭 | KindNetwork | ✅ |
| reset | accept 后 SO_LINGER=0 关闭（RST） | KindNetwork | ✅ |
| EOF | 接受连接后无响应干净关闭 | StreamTimeout/Network（实测） | ✅ |
| DNS | `.invalid` 保留 TLD | KindNetwork | ✅ |
| TLS | httptest 自签名 → x509 | KindTransient（基线证据，现会烧重试预算） | ⚠️ 现状 |
| slow-read | chunked 滴流 + ctx 取消传播 | Read 及时返回 ctx.Canceled | — |

- goroutine 泄漏门禁：轮次增长比较（2 轮稳态采样 vs 3 轮后采样，±5
  容差）——对同机 CPU 负载自校准，不会因并行构建误报。

**桥接层修复**（审计缺口本体）：SSE reader（bufio 包装 resp.Body）阻塞
在 Read 上时，ctx 取消无法唤醒——上游不断流时读循环挂死到上游断开
（实测 10.27s）。新增 `ctxCancellableBody`（`cancellable_body.go`）：
watcher 监听 ctx.Done() 强制 Close 底层 body（Close 语义保证解除 Read
阻塞），接入 anthropic_bridge 两处与 responses_bridge 读循环。修复后
取消 0.25s 返回，中断原因正确（client_cancel）；重复取消场景轮次增长
泄漏门禁通过。

## 闭环7：TestSelectP99Under1ms 性能门禁修复（不放宽阈值）

**根因**：每次 Select 在 10k 宇宙下分配 ~190KB（prefilter 幸存切片 +
scorer 输出 + Alternatives），GC 压力与调度抖动使 p99 在并行负载下
冲到 2.3ms（破线）。

**修复**（`domains/nodestatecache/selector.go`）：
- 幸存集与（自分配的）评分输出经 sync.Pool 复用；Scorer 返回的切片归
  实现方所有，不入池（防实现方内部复用缓冲被双重持有）；
- 小 Used（≤8）线性比较替代 map，0 Used 零分配；

**效果**（-count=3 稳定）：p50 60µs→54µs，p99 175-624µs（带负载最高
2.32ms）→ **~134µs 全程低于门限**，max 15.3ms→~200µs；并行 go build
负载下重跑通过。阈值 1ms 未动；另新增 `TestSelectAllocationsBounded`
（≤6 allocs/op）防止 GC 压力根因回潮；测试侧加 512 次预热 +
`runtime.GC()` 清构造期垃圾（稳态测量口径，非放宽）。

## 闭环8：前端统一与远程菜单校验

- **credentialDisplayName 统一**：RoutingAttemptsTimeline 的凭据行从
  裸数字 ID 改为 `useCredentialLabels.credentialDisplayName`（label
  缺失回退 "凭据 #id"，绝不回显 raw）；
- **routing/error 结构化展示**：时间线新增 error_kind/stage/retryable
  结构化徽标（低基数词表 + 中文标签），error_message 降为补充文本；
  `web/src/api/logs.ts` 与 `vendor-credential-error.ts` 类型同步；
  ErrorDetailTab 失败表新增 阶段/重试/耗时 列；
- **远程菜单 schema/权限裁剪**（`plugin-runtime/nav_validate.go`）：
  manifest.pages 之前零校验直接进入 `/api/v1/plugin-nav`。现校验：
  page path 相对路径白名单字符集（拒绝绝对路径/../反斜杠/协议片段）、
  去重、页数上限 64；nav.group 低基数词表（≤64B）；label_key 必须为
  点分 i18n key（允许 camelCase，≤128B）；order ±10000；
  tenant_only+platform_ops 矛盾位拒绝。校验失败 = manifest 无效 =
  不注册菜单（fail closed）。租户维度：viewer 的 PlatformOps 仅
  default 租户（`wirePluginAuthExtractor`），TenantOnly 与 PlatformOps
  互斥校验消除前端裁剪歧义。

## 验证记录

```
go vet ./admin ./bg ./durable ./storage/... ./plugin-runtime \
        ./domains/streaming/... ./domains/nodestatecache ./upstream ./deploy/sql/verify
→ exit 0

go test ./upstream ./domains/streaming/... -count=1        → ok（含故障注入/SSE 取消/泄漏门禁）
go test ./admin ./bg ./durable ./storage/... ./plugin-runtime \
        ./domains/nodestatecache ./upstream -count=1        → ok
go test ./deploy/sql/verify -count=3（真实 PG+citus_columnar）→ ok
web: npx vue-tsc --noEmit → exit 0
web: vitest useCredentialLabels + i18n/parity → 28 passed
```

## 遗留与后续建议

- TLS x509 现分类为 transient（可重试、烧重试预算）——已用测试钉死
  基线，改为不可重试属产品决策，需单独评估；
- `AggregateTaskOutcome` 生产调用方已迁移，保留为兼容 API 与决策矩阵
  测试基线；后续可随测试一起迁移后删除；
- candidate_failure_logs_hot → supplier_errors_hot 双写为迁移期过渡，
  读端已统一切换；旧表下线需等 promote 历史窗口（8h+）过后按
  V359 治理模式清理。

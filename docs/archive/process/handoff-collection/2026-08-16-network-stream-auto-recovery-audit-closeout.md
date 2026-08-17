# 2026-08-16 — llm-gateway-go 网络流式自动恢复审计收口与 durable 核心加固交接

> 状态：审计收口（无代码改动）；下一会话 durable 核心加固 handoff
> 上一轮交接：`docs/handoff/2026-08-16-durable-nonchat-survival-hardening.md`
> 当前 HEAD：`cc1498d6a`（main，与 `origin/main` 同步）
> 工作树：clean（无需 commit / push）

## 1. 本会话已完成

1. **分支合并确认 + 清理**
   - 核验 `fix/network-stream-auto-recovery` 顶端 `c94ba8c59` 与 `origin/main` 中 `860a8412a` 字节一致。
   - 删除本地与远端 `fix/network-stream-auto-recovery` 分支，无丢失风险。
   - 工作树恢复与 `origin/main` 同步态。

2. **已合入 main 的网络流式自动恢复 / durable survival / settlement 代码审计**
   - 范围：`domains/streaming/{durable_recovery_worker,durable_stream,stream_errors,executors/executor}.go`、`durable/settlement_outbox.go`、`errorsx/classify.go` 等。
   - 重点核验：
     - **网络中断恢复**：`StreamOutcome{Interrupted, Resumable, ChunkCount, Kind, Reason}` 驱动候选 failover；`isResumable := Resumable && ChunkCount < StreamRetryThreshold` 作为网关；网络类错误（含 `io.ErrUnexpectedEOF`、`errorsx.KindNetwork`）走可恢复分支；client cancel / `KindCanceled` 走非恢复分支。
     - **durable survival**：`SurvivalCoordinator` 接管前台恢复；`durable_llm_tasks.attempt_count` 作为唯一执行账本，初次执行后最多 100 次重试（第 101 次失败转终态），2s 指数退避封顶 120s，上游 `RetryAfter` 为权威调度。
     - **settlement outbox**：`durable_task_settlement_intents` 把 terminal DB 事务的补偿与执行重试解耦；`PersistSettlementIntent` 带 fencing（`WHERE ... AND fencing_token=... AND status NOT IN terminal`），`FinalizeSettlement` 用原 task lease/fencing 原子提交并删除 intent，不存在二次结算或重放路径。

3. **构建与测试回归**
   - `go build ./...` 通过。
   - `go test ./...` 全绿。
   - `go test -race ./domains/streaming/... ./durable/...` 通过。
   - `go vet` 通过。

4. **报告陈旧引用复核**
   - `docs/修订0811/32-M4-S30-S35故障注入统一验证报告-2026-08-15.md` 第 54 行原"返回 501"措辞已在上一轮更新为 `5a62fb89` 已接入 `SurvivalCoordinator` 的描述，本轮无需再改。

## 2. 审计结论

- 未发现 critical / high 缺陷。生产 fix 文件与 `origin/main` 字节一致；settlement 防双重结算（fencing + 终态排除）；retry / backoff 边界正确（100 次封顶、120s 指数退避上限、上游 `RetryAfter` 权威）。
- 唯一关注点（非缺陷，已内部核实）：`KindUpstreamContextLoss` 故意排除于 `IsRetryable` 等判定，候选循环中走通用失败分支（记录 circuit failure 后落到下一候选），与其"必须计入降级"的注释语义一致。
- 因此本轮**无代码改动、无 commit / push**——遵循用户"不丢弃他人改动"指示，且无缺陷需修复，不创建空 commit。

## 3. 当前状态

| 项 | 值 |
| --- | --- |
| 项目根 | `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2` |
| 当前分支 | `main` |
| HEAD | `cc1498d6a` |
| 与 `origin/main` | 同步（NO_AHEAD） |
| 工作树 | clean |
| 版本 | `v2.5.0-865dc5ee-20260815-1558` / `build_seq 1558` |
| 正在编辑 | 无 |

## 4. 下一会话 durable 核心加固（Next Steps）

> 落地建议：使用 `tdd` skill 红绿迭代；开改前用 `comprehensive-code-audit` 或 `review` skill 再审一遍 store contract。

### 4.1 durable settlement store 写失败的可靠补偿 / 重试

- 范围：`settleDurableStream` 中 `Complete` / `ReleaseToWorker` / `FailTerminal` 的 store 写入失败分支。
- 现行为：失败仅记日志后停续租，依赖 safety reaper 兜底。
- 目标：设计可靠重试 / 补偿（带 fencing / 幂等键），但**必须保持**：
  - `post-content disconnect` 必须进入 `resume_safety_blocked`；
  - 绝不重放已落地的 terminal 结果。
- 约束：不要触碰 `durable_task_settlement_intents` 的现有 fencing 语义；补偿路径仍走 outbox。

### 4.2 端点级故障注入测试（不只 component 级）

- 范围：Messages / Responses 的 handler 级故障注入。
- 覆盖：
  1. checkpoint failure（`durable_stream_checkpoint` 写失败）；
  2. lease loss（worker 持有期间 lease 被剥夺）；
  3. deadline 超时；
  4. client disconnect（中途断流）；
  5. native terminal SSE **恰好一次**且不追加 JSON body（与 `4b7bd6244` 的 SSE 一次性约束保持一致）。
- 现状：`4b7bd6244` 已新增 `durable_nonchat_endpoint_test.go` 起步，需在真实认证 + 上游 mock 下验证并扩展。

### 4.3 报告回写

- 完成 4.2 后，更新 `docs/修订0811/32-M4-S30-S35故障注入统一验证报告-2026-08-15.md` §5 的状态与边界。

## 5. 阻塞 / 风险

- 真实 PostgreSQL / RLS 的 settlement migration 部署态注入。
- Messages / Responses 经真实认证与上游的 handler 级故障注入。
- 客户端 SDK SSE 兼容矩阵。

以上属灰度前置项，未在本轮宣称完成（见报告 §5 剩余项）。

## 6. 关键提交 / 文件索引

| 提交 | 用途 |
| --- | --- |
| `860a8412a` | 网络流式自动恢复主干 |
| `5a62fb89` | 接入 `SurvivalCoordinator` |
| `6c62bec9f` | durable survival 接入 |
| `5b87bcee0` | terminal settlement intent 恢复 |
| `4b7bd6244` | durable settlement 并发加固 + `durable_nonchat_endpoint_test.go` 起步 |
| `e98a0b7cd` | settlement fencing 原子化 |
| `f19ca26aa` | merge: harden durable settlement fencing |

关键文件：
- `domains/streaming/durable_recovery_worker.go`
- `domains/streaming/durable_stream.go`
- `domains/streaming/stream_errors.go`
- `domains/streaming/executors/executor.go`
- `durable/settlement_outbox.go`
- `errorsx/classify.go`

## 7. 下一会话提示词（Prompt Stub）

> 继续 `llm-gateway-go` 的 durable 核心加固。先 `tdd` 红绿迭代 `settleDurableStream` 中 `Complete` / `ReleaseToWorker` / `FailTerminal` 的 store 写失败可靠重试 / 补偿（保持 `post-content disconnect → resume_safety_blocked`、不重放 terminal）。完成后扩展 `durable_nonchat_endpoint_test.go` 至 Messages / Responses 的 checkpoint failure / lease loss / deadline / client disconnect / native terminal SSE 一次性校验。最后回写 `docs/修订0811/32-M4-S30-S35故障注入统一验证报告-2026-08-15.md` §5。参考本 handoff 与 `2026-08-16-durable-nonchat-survival-hardening.md`。
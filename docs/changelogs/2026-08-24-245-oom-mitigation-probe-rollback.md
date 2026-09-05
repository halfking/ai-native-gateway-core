# 245 memcg OOM 缓解 + probe rollback 打标接通

## Background

2026-08-24 245 预发环境调查结论（handoff `2026-08-24-160155-quota-failover-merge` §4.2/§4.3）：

1. **memcg OOM 活跃风险（P0）**：dmesg 53 次 OOM-kill 全部 `CONSTRAINT_MEMCG / task=gateway`；单次解剖 anon 1.95G 撑满 `MemoryMax=2G`，failcnt 378 万；触发模式为长上下文流量（max prompt_tokens=920179），Go 堆 20-90 分钟涨满 2G。
2. **probe rollback 打标入口休眠（P1）**：`bg/probe_rollback.go` 的 rollback worker 已在 245 运行（`CHECKPOINT: probeRollback started`），但 `MarkTentativeRestore` 全代码库零调用方 —— 永远没有 tentative 行可观察；机制无 Prometheus metric，只有 slog（8/24 14:33-16:27 有 8 次 `revert scan failed (context deadline exceeded)` 无信号）。

本次改动覆盖三条修复线：入口 prompt 预算（根因）、GOMEMLIMIT GC 背压、打标接通 + 可观测。

## Changes

### 1. 请求入口 prompt 预算拒绝（根因缓解）

- **`domains/streaming/request_meta.go`**：新增 `promptBudgetLimit()`（解析 `LLM_GATEWAY_MAX_PROMPT_TOKENS`，0/未设置/非法/负值 = fail-open 关闭）与 `promptBudgetExceeded()`（复用 `auto_route.estimateTokens`，±30% 启发式，含 JSON 结构开销、偏保守方向）。
- **三个协议入口**在 body-size 检查之后、JSON 解析之前接入（此落点防大请求在解析/转发/审计路径繁殖进程内副本）：
  - `domains/streaming/handler.go`（`/v1/chat/completions`）：413 + `code=prompt_too_large`，走 `logCtx.SetError/EmitFailure/MarkLogged`（防 safety-net 双写）。
  - `domains/streaming/messages.go`（`/v1/messages`）：413 + `writeAnthropicError`。
  - `domains/streaming/responses.go`（`/v1/responses`）：413 + `writeResponsesError`。
- **部署值**：245 设 `LLM_GATEWAY_MAX_PROMPT_TOKENS=262144`（256k）；154 等大内存环境默认 0（不限制），1M-context 模型流量不受影响。

### 2. GOMEMLIMIT GC 背压（deploy/llmgo-245.service）

- unit 注入 `Environment=GOMEMLIMIT=1900MiB`（低于服务端 drop-in `MemoryMax`），让 Go GC 在 memcg 击杀之前先自我回收。
- 与服务端 `memory-override.conf`（MemoryHigh/MemoryMax 上限）、入口 prompt 预算构成三层防线。

### 3. smart-fallback 暂定恢复接通 + rollback 可观测

- **`bg/node_probe.go` `ProbeSync` 成功分支**：恢复可用性（`updateBindingAvailability(true)`）之后、gateway 轮之前调用 `MarkTentativeRestore` 打 `probe_revert_at = now()+T`；窗口 `LLM_GATEWAY_PROBE_TENTATIVE_REVERT_AFTER`（Go duration 或裸秒数，默认 15m，`0/off/false/disabled` 关闭；负值/非法回落默认 fail-safe）。
- **确认链自然存在**：`emitSyncAudit` 刻意不写 `node_probe_state`（next_retry_at 不变），tick/统一队列仍会复探该 (cred,model)；确认探测成功经 `updateBindingAvailability` 成功分支清 timer 转正；窗口内未确认由 rollback worker 回退（one-shot WHERE guard 关闭与并发确认的竞态）。打标用 `context.WithoutCancel + 3s` 超时，不受请求 ctx 取消影响。
- **`bg/probe_rollback.go` metrics**：`llmgw_probe_rollback_tentative_marked_total`（打标，含 RowsAffected>0 判定）/ `reverted_total`（实际回退行数）/ `scan_failures_total`（扫描失败，盯共享 PG 超时频率）/ `scan_duration_seconds`（直方图）/ `pending`（gauge，COUNT 走 migration 351 部分索引，best-effort）。

## Tests

- `bg/probe_rollback_tentative_test.go`：env 解析 11 用例（默认/时长/秒数/四种关闭字面量/大小写/负值/垃圾回落）+ 契约测试（ProbeSync 打标必须位于 availability 恢复之后、gateway 轮之前，且被窗口 env 守卫）。
- `domains/streaming/prompt_budget_test.go`：env 解析（含 fail-open）、超/欠预算、空 body、413 响应体、三入口接线断言。
- 全量：`go vet ./...` PASS；`go test ./cmd/gateway ./domains/... ./bg ./admin ./errorsx ./upstream -count=1` 111 包全过；`scripts/pre-commit-check.sh` PASS=4 FAIL=0。

## Deployment Notes

- 245 部署走 `scripts/deploy-seamless.sh deploy 245 --seq <N>`（≥2471c4b82，含 quota failover）。
- `.env` 或 unit 追加：`LLM_GATEWAY_MAX_PROMPT_TOKENS=262144`（245 先行验证；确认误伤率后评估是否推 154）。
- 服务端 drop-in `memory-override.conf` 建议同步调整为 `MemoryHigh=1800M / MemoryMax=2355M`（3.5G 机，天花板 2.7G），并清理 legacy `MemoryLimit` 双真源 —— 服务端操作，带备份与回滚一行。
- 观察项：`llmgw_probe_rollback_pending` 应在打标后短暂 >0 并随确认探测回落；`scan_failures_total` 增速反映共享 PG 竞争（8 次/2h 基线）；确认窗口内 `reverted_total` 与真实失败率对齐。

## Rollback

- 打标入口：设 `LLM_GATEWAY_PROBE_TENTATIVE_REVERT_AFTER=off`（关闭打标，rollback worker 空转无副作用）。
- prompt 预算：设 `LLM_GATEWAY_MAX_PROMPT_TOKENS=0`（fail-open 恢复不限制）。
- GOMEMLIMIT：删除 unit `Environment=GOMEMLIMIT` 行 + `systemctl daemon-reload && systemctl restart llmgo-245`。
- 代码级：单 commit revert，无 schema 变更（`probe_revert_at` 列已存在，migration 351/486）。

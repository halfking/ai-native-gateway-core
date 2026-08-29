# 2026-08-29 §4.2 outcome 决策证据 — 业务方评审用

## 背景

`.handoff/2026-08-29-section5-recheck.md` §3.3 / §4.2 要求在
`StreamAnthropicSSEToOpenAIWithDiagnostics` 的 outcome 分类方向上做出
不可逆决策。本会话只完成**证据整理与方向选项列比**，不修改代码、
不动测试断言。修改要等业务方在 PR description 上勾选方向后再合入。

## 当前代码语义（HEAD `cd84808cb`，已 merge `cd84808cb`）

### defer 优先级链路

`domains/streaming/anthropic_bridge.go:800-803`：

```go
clientWriter := newClientStreamWriter(w, flusher)
defer func() {
    applyClientDisconnectOutcome(&outcome, clientWriter, !outcome.Interrupted)
}()
```

`anthropic_bridge.go:78-90`：

```go
func applyClientDisconnectOutcome(outcome *StreamOutcome, clientWriter *clientStreamWriter, upstreamCompleted bool) {
    if outcome == nil || clientWriter == nil || !clientWriter.clientDisconnected {
        return
    }
    outcome.Interrupted = true
    outcome.Kind = errorsx.KindCanceled
    outcome.Resumable = false
    if upstreamCompleted {
        outcome.Reason = "client_disconnected"
    } else {
        outcome.Reason = "client_write_failed"
    }
}
```

`!outcome.Interrupted` 在 defer 调用时等价于「上游是否完整走完」：
- 上游已完（走到 `case ir.ChunkTypeDone` 走完 `return StreamOutcome{ChunkCount: chunkCount}`，`Interrupted=false`）→ `upstreamCompleted=true`
- 上游未完（任何提前 `return StreamOutcome{Interrupted: true, ...}` 路径）→ `upstreamCompleted=false`

### 三条断开路径的当前 outcome

| 路径 | 触发条件 | `outcome.Interrupted` 入 defer | `clientWriter.clientDisconnected` | `outcome.Reason` 出 defer |
|---|---|---|---|---|
| **header write 失败** | line 204 `safeFlush` 在 WriteHeader 后立刻失败 | `true`（line 214 提前 return） | `true` | `client_write_failed` |
| **中途 SSE flush 失败** | line 308 / 413 任何 `clientWriter.write/flush` 返回 false | `true`（line 308/413 提前 return） | `true` | `client_write_failed` |
| **上游完成但客户端已死** | 走完 SSE 流，最后 `case ir.ChunkTypeDone` → `return StreamOutcome{ChunkCount: chunkCount}` | `false` | `true`（SSE 某帧 write 失败触发 latch） | `client_disconnected` |

### pending/disconnect 闭环验证

`TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer`
（`pending_disconnect_extra_test.go:99`）当前 PASS：

```
$ go test ./domains/streaming/ -run TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer -count=1 -v
=== RUN   TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer
--- PASS: TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer (0.00s)
PASS
```

该 fixture 走第三条路径——`message_stop` 已收到 → 走完 done 分支 → defer
把 Reason 改为 `client_disconnected`，`pc.Snapshot()` 返回 completed
状态、`[DONE]` 已写到 capturer。

完整 streaming 包回归（`-race -count=1`）：

```
$ go test ./domains/streaming/ -count=1 -race -timeout 180s
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming	68.393s
$ go test ./domains/streaming/executors/... -count=1 -race -timeout 180s
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming/executors	24.119s
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming/executors/webcookie	2.174s
```

`client_write_failed` 与 `client_disconnected` 在 4 个 streaming 路径
里的语义被消费：

- `domains/streaming/billing.go:24`: 把 `client_cancel` / `client_disconnected`
  当作 fail-billing 信号，不收钱。
- `domains/streaming/handler.go:7361`: `client_cancel` / `client_disconnected`
  走 `isClientCancel=true` 分支，不算 hard-failure。
- `domains/streaming/durable_stream.go:327`: `client_disconnected_pre_content`
  触发 worker 释放（仅在 releaseToWorker 路径上有特殊语义）。
- `client_write_failed`: handler 视为真连接失效，不重试（`Resumable=false`）。

## 两个方向的 failover 边界

### 方向 A — 接受当前行为（不修改代码）

**改动**：无。`Reason` 维持「中途断 = `client_write_failed`、走完上游
= `client_disconnected`」的语义。

**failover 边界**：
- 客户端断开发生在中途（任何 chunk write/flush 失败）：
  - `Reason=client_write_failed`、`Resumable=false`、`Kind=KindCanceled`
  - handler: 不重试，**billing 视为「不收钱」**（与 disconnect 等价处理）
  - 重连客户端必须发新 request，旧 ctx 不会 retry
- 客户端断开发生在上游已完成之后（走完 SSE 才 flush 失败）：
  - `Reason=client_disconnected`、`Resumable=false`、`Kind=KindCanceled`
  - pc capturer 已 completed，可走 pending/durable 路径发回客户端
  - billing: `client_disconnected` 同样走 cancel 路径

**优点**：
- 区分「中途真失败」与「走完后客户端提前退出」——前者暗示网络/缓冲
  问题，后者暗示客户端正常 timeout 后上游浪费 tokens
- 与 `client_write_failed` `Resumable=false` + `client_disconnected` 也
  `Resumable=false` 的现有契约一致
- 测试已全绿，无须改测试

**缺点**：
- 业务方 dashboard 必须分别聚合两个 Reason label 才能算「客户端
  提前退出」总数（`client_write_failed + client_disconnected`）
- 在错误聚合场景下会把「真网络失败」误算到「客户端 cancel」中

### 方向 B — 恢复旧行为（统一为 `client_disconnected`）

**改动**：把 `applyClientDisconnectOutcome` 的 `if upstreamCompleted`
分支去掉，统一写 `Reason="client_disconnected"`。同时把测试断言
`assert.Equal(t, "client_write_failed", ...)` 全部改为
`assert.Equal(t, "client_disconnected", ...)`。

**failover 边界**：
- 客户端断开（任何时机）：
  - `Reason=client_disconnected`、`Resumable=false`、`Kind=KindCanceled`
  - 真网络失败被吞进 cancel 桶
  - pc capturer 行为不变

**优点**：
- dashboard / alert 只需聚合一个 Reason
- 业务语义统一：「客户端就是断了」

**缺点**：
- 丢掉了「中途 header write 失败」的可观测性 —— 这是真网络抖动信号
- 涉及修改的测试断言较多（pending、passthrough、Q3 / Q4 共 4+ 处）
- 与 `domains/streaming/handler.go:7361` 的 `isClientCancel` 区分
  弱化：原来 cancel 类原因有 2 个 (`client_cancel` / `client_disconnected`)，
  方向 B 之后 cancel 类只剩 1 个真 client-action 信号
- billing 路径无变化（两个 reason 都走 cancel 路径）

## 推荐方向

**A**（保持当前行为），理由：

1. 当前测试已全绿，未观察到生产回归
2. `client_write_failed` 是 handler 端的核心 sentinel —— 把它消解成
   `client_disconnected` 会失去「同一连接不可重试」的可观测信号
3. 真正的客户端 cancel 监控可以走 `pc.Snapshot()` 已 completed 但
   Reason=cancel 这条边界的 metric，不需要合并 reason 字符串

业务方在 PR review 时可勾选 A 或 B；本会话不擅自落地。

## 引用

- `domains/streaming/anthropic_bridge.go:78` `applyClientDisconnectOutcome`
- `domains/streaming/anthropic_bridge.go:800-803` `StreamAnthropicSSEToOpenAIWithDiagnostics` defer
- `domains/streaming/anthropic_bridge.go:1185-1215` `case ir.ChunkTypeDone` empty-response / done 分支
- `domains/streaming/billing.go:24` client_cancel / client_disconnected billing
- `domains/streaming/handler.go:7361-7420` isClientCancel & Resumable 消费
- 测试：`pending_disconnect_extra_test.go:99` / `:144` / `:188`
- 上游 handoff：`.handoff/2026-08-29-followup-streaming-recheck.md` §3.2
- 当前 handoff：`.handoff/2026-08-29-section5-recheck.md` §3.3 / §4.2

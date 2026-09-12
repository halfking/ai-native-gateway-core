# 2026-09-13 — /v1/responses 终止事件补齐 + Q2 桥空流 failover（2089）

## 背景

llmgo.kxpms.cn（245，build 2086 实测）两个网关侧流式缺陷（证据已复现留档，方案 36 §7）：

1. **/v1/responses 缺 `response.completed` 终止事件** → codex ≥0.80 断流，被迫锁定 0.45.0 + chat wire。
2. **/v1/messages 流式病态**：纯 curl 复刻全套 claude 请求特征可复现「HTTP 200 后只收 ~56 字节 keep-alive 直至超时」（56B = 4×14B `: keep-alive` 注释，15s 间隔 ≈ 60s 窗口）；claude code 间歇 529（E2E 约 1/5 成功率）。同窗口 OpenAI chat 层 9/10 正常。

## 根因

### 缺口 0（245 实测定位的主根因）：survival holdback 窗口吞掉终止渲染

245 部署 2090 后 curl 实测 `/v1/responses`（gpt-6-astra）仍缺 `response.completed`，journals 取证：

```
survival_attempt_start:  holdback_window_ms=5000, holdback_max_chunks=20
request_survival_finished: "succeed":true, "committed":false, "commit_state":"metadata"
```

survival 协调器给每个流式 attempt 的 gate 挂 **L1 holdback 窗口（5s / 20 chunks）**：语义帧被 hold 而**不推进 gate 提交状态**（`attempt_commit_gate.go holdbackTryHoldLocked`）。短流（<5s 且 <20 chunk，正是绝大多数 Agent 请求）整个流结束在窗口内 → 桥的 `finishAttempt`/Phase-4 tail 决策看到 `MayWriteTerminal()==false` → **跳过终止渲染** → 协调器收尾 `GateWriter.Finish` 把 held 帧冲给客户端，但无人再补终止事件。影响面：

- `/v1/responses`：deltas 后流无 `response.completed` 即关闭（codex ≥0.80 断流）；
- `/v1/messages`：客户端收到 `message_start` 但无 `message_delta/message_stop`（"message_start but no content blocks" → 非流式回退 → 503/529）。

修复：两个 responses 桥与 Q2 桥在**成功路径**调用现成的 `gate.FlushHoldback()`（强制关闭 holdback 窗口并提交 held 帧，`attempt_commit_gate.go FlushHoldback`），终止渲染随即正常执行；中断路径不动 holdback，保持透明 failover 语义。回归测试 `stream_holdback_regression_test.go`（三协议）。

### 缺口 1：responses_bridge.go benign-EOF 分支不发终止事件

`StreamAnthropicSSEToResponsesWithDiagnostics` 的 EOF 分支中，当上游（anthropic-messages 协议，minimax 风格中继常见）发出 finish_reason 后不发 `message_stop` 直接关流时，代码判定 benign（成功）却**不调用 `scaffold.finishAttempt()`** 直接 return —— 网关认为成功，客户端永远收不到 `response.output_text.done / response.output_item.done / response.completed`。对照同函数 clean-EOF 路径（有 finishAttempt）确认为遗漏。

### 缺口 2：Q2 桥（OpenAI 上游 → Anthropic 客户端）无空流检测

245 日志取证：claude-sonnet-5/opus-5 路由至 provider 13092（u.syapi.cn，OpenAI 协议）。`StreamOpenAIToAnthropicSSE` 对「200 + 零语义 delta（role/空 choices + usage + [DONE]）」的上游流照样写 `message_delta + message_stop` 并返回**非中断成功** → executor 不 failover → 客户端只见 message_start 无内容 / 纯 keep-alive → 重试耗尽 → 503 overloaded（claude code 显示 529）。

chat 层（`runEmptyStreamGate` + early_empty）、Q3/Q4（`IsAnthropicStreamEmpty`）均有空流防护，**唯独 Q2 缺失** —— 与「同窗口 OpenAI chat 9/10 正常」完全吻合。

## 修复

| 文件 | 变更 |
|---|---|
| `domains/streaming/responses_bridge.go` | benign-EOF 分支 return 前补 `scaffold.finishAttempt(...)`；`MayWriteTerminal` 守卫保持未提交 attempt 静默（不破坏透明 failover） |
| `domains/streaming/anthropic_stream.go` | 主循环结束后、写 tail 前：`!outcome.Interrupted && chunkCount == 0` → 返回 `KindEmptyResponse + Resumable=true`（`anthropic_empty_response`），不写 `message_delta/message_stop`，executor 同请求内切换候选 |

## 回归测试

- `TestStreamAnthropicSSEToResponses_BenignEOFEmitCompleted`：上游 finish_reason 后无 message_stop 即 EOF → 断言 body 含 `event: response.completed` 且 outcome 非中断。
- `TestStreamOpenAIToAnthropicSSE_EmptyStreamIsResumableFailover`：空 choices + usage + [DONE] → 断言 `Interrupted=true / KindEmptyResponse / Resumable=true`，body 无 `message_stop`。

验证：`go test ./domains/streaming/... ./domains/streaming/executors/ ./domains/transformation/anthropic/ ./internal/ir/` 全绿。

## 预期效果

- /v1/responses：所有成功流必带终止事件，codex ≥0.80 可解除 0.45.0 锁定（待 245 验证后由 owner 决定）。
- /v1/messages：空流上游在单请求内被透明 failover 跳过，不再把「空成功」返回给 claude code；529/keep-alive 挂死窗口收敛。

## 关联

- 方案 36 §7（问题留档）、VM `~/backup-ai-gateway-20260912-051238/`（变更前配置备份）。
- codex 0.45.0 + chat wire 锁定本版不变。

## 245 部署与验证记录（2026-09-13）

部署：`scripts/deploy-245.sh --no-frontend`，最终 **build_seq 2092 / commit 696bc69d**（含修复提交 b514ec374 + 4b7fb6bfd），healthz ready=true。

**配置调优（代码之外）**：245 `.env` 追加 `LLM_GATEWAY_REQUEST_SURVIVAL_RETRY_BASE_SECONDS=4` / `RETRY_MAX_SECONDS=16`（原默认 30s——上游流中打嗝时客户端只能跨 30s+ backoff 干等 keep-alive 直至超时，即"56 字节 keep-alive"体验的直接来源）。变更前 `.env` 已备份至 VM `.env.bak-survival-backoff-*`。

**E2E 复现验证（公网 llmgo.kxpms.cn）**：

| 用例 | 结果 |
|---|---|
| `/v1/responses` 流式（gpt-6-astra）×4 | 4/4 完整终止序列：`response.created → deltas → output_text.done → output_item.done → response.completed`（修复前 0/2，deltas 后裸断流） |
| `/v1/messages` 全套 claude 特征（stream+tools+cache_control+metadata+beta 头）×5 | 5/5 完整信封：`message_start → ping → content_block(tool_use) → message_delta → message_stop`，2.5-2.8s，无 keep-alive 挂死（修复前间歇 75s 超时只剩 keep-alive） |
| `/v1/chat/completions` 回归冒烟 ×3 | 3/3 200 + content |

期间观测到 apiclaude.cc（provider 587）间歇性流中 `event: error`（resumable，透明重试）——上游侧抖动，网关按设计 failover；配合 4s backoff 客户端不再感知。

## 遗留观察项

- native Responses 透传（`StreamNativeResponsesSSE`）不合成终止事件——上游截断时客户端无 `response.completed`，保持透传语义另行观察。
- codex 0.45.0 + chat wire 锁定不变；解除锁定由 owner 在 codex ≥0.80 上复验后决定。

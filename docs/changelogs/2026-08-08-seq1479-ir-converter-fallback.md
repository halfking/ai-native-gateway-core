# 2026-08-08 — seq 1479: IR converter circuit-open 软降级到 legacy

## 背景

seq 1477 / 1478 修复了 credential circuit 在 sole-candidate 场景下导致 GPT/Claude 503 的问题。但 154 部署后日志中仍残留 14+ 个 `all 1 candidates failed: ... ir parse openai: transport: converter circuit open` 失败（Claude-sonnet-5 居多）。

根因：`domains/transformation/ir_converter.go` 维护一个**进程内 IR 流式/解析熔断器**（`StreamCircuitBreaker`，3 次错误 / 1 分钟触发 OPEN，1 分钟冷却）。熔断后 `ParseOpenAI / ParseAnthropic / SerializeOpenAI / SerializeAnthropic` 返回 `ErrConverterCircuitOpen`。

`domains/transformation/factory.go:147` 的 `pickDecision()` **只在 IsStream=true 时**检查熔断并 fallback 到 Legacy。当请求是 Anthropic→Anthropic / OpenAI→Anthropic 的非流式协议转换（Q3 / Q4 路径），它走 `executor_anthropic.go:443` / `executor_chat.go:1374` 直接调 IR converter；IR circuit 一旦 OPEN → 直接 return error → 503。

对于 sole-candidate 模型（apiclaude/apigpt 各自只有一个可用 credential），意味着 IR circuit OPEN 会让该模型所有请求 503，跟之前的 credential circuit 问题同形态。

## 修复

`domains/streaming/executors/executor_anthropic.go` 和 `executor_chat.go` 的两处直接 IR converter 调用增加 `errors.Is(err, transformation.ErrConverterCircuitOpen)` 检测 → fallback 到 legacy 转换器（`ChatToAnthropic` / `AnthropicToOpenAI`），由 `cmd/gateway/main.go` 在 wiring 时注入。

把 legacy 路径代码抽成可复用辅助函数：
- `executor_anthropic.go` 新增 `legacyAnthropicBody(params, cand, sourceBody)`
- `executor_chat.go` 新增 `legacyChatToOpenAIBody(params, cand, sourceBody)` 与 `applyOpenAITailTransforms(params, cand, bodyBytes)`

### 改动文件

| 文件 | 修改 |
|---|---|
| `domains/streaming/executors/executor_anthropic.go` | `ParseOpenAI` / `SerializeAnthropic` 在 `ErrConverterCircuitOpen` 时 fallback 到 `legacyAnthropicBody`；新增 helper；import `errors` |
| `domains/streaming/executors/executor_chat.go` | `ParseAnthropic` / `SerializeOpenAI` 在 `ErrConverterCircuitOpen` 时 fallback 到 `legacyChatToOpenAIBody`；新增 2 个 helper；import `errors`；重构 `finalizeOpenAIUpstreamBody` 尾部处理调用 `applyOpenAITailTransforms` |

### 设计原则

- **不改变 IR 解析失败时的行为**：原本返回的 `ir parse anthropic: ...` / `ir serialize openai: ...` 错误继续保留（不是熔断而是真解析错误）
- **仅对熔断状态做 fallback**：用 `errors.Is(err, transformation.ErrConverterCircuitOpen)` 严格匹配
- **fallback 路径与原 legacy 路径行为完全一致**：复用现有 `ChatToAnthropic` / `AnthropicToOpenAI` callback，不引入新转换逻辑
- **可观测**：每次 fallback 都 warn log，包含 `request_id / provider_id / credential_id / raw_model / client_model / stage`，运维可统计触发率

## 验证

- `go build ./cmd/gateway` OK
- `go test ./domains/streaming/executors/...` 全过（6.6s）
- `go test ./...` 全过（modelquality `TestExecCommand_StartsRealProcess` 在并行跑时偶发冲突，单跑通过，与本次无关）

## 部署影响

- 与 seq 1477 同发：sync 到 main → 部署到 154 → /metrics 上 fallback warn log 出现率应 < 0.1%（IR circuit 偶发 OPEN 但 legacy 路径能 cover）
- 不影响 IR 流式路径（`pickDecision()` 那条已在 seq 1477 之前 fallback）
- 不影响非 IR 模式用户（`e.IR == nil` 直接走 legacy，零路径分支差异）
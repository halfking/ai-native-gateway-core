# 请求层 token 估算 + 多层会话压缩阈值 Handoff

> 读者：接手 admin observability 与 session compression 的工程师，或任何审计本任务的 reviewer
> 状态：代码已合并到 `main` (`47482be4f`)，全量 `go test ./...` 通过；本文件记录关键设计点、边界条件与剩余事项
> 时间：2026-08-19

---

## 1. 目标与决策摘要

用户的核心语义：会话已经被多层压缩过的话，原始输入再大也不应该再触发压缩；判断对象必须是**实际发给 LLM 的 outbound token**，而不是"原始请求体大小"。

实现策略：

| 模块 | 改动 |
|---|---|
| `domains/streaming/token_estimate.go` | 统一 token 估算入口（`EstimateInputTokens` → `tokenest.FromChars(len(body))`） |
| `RequestLogContext` | 新增 `PromptTokensEstimate *int`，在 `EnsureCaptured`/`CapturePartialBody`/`buildEntry` 三处幂等写入 |
| `domains/streaming/handler.go` | success / initial / failure 三类 telemetry entry 都复用 receipt estimate；真实 `prompt_tokens` 会覆盖并将 `usage_source='llm'` |
| `domains/hooks/compression/window.go` | 新增 `OutboundTokenBand`（below/preliminary/forced）+ token band 分类；TOKEN trigger 加 absolute floor（不依赖 ctx_window） |
| `domains/hooks/compression/session_compressor.go` | `PrepareResult` 新增 `TokenBand`/`PriorLayerTokens`/`CompressionReason`；`>400k` 时将 legacy 模式临时提升为 smart，并把 `taskType` 改为 `document_summary` 让摘要走专用队列 |
| `config/summary_config.go` | 新增 `SummaryDimensionDocumentSummary`；`Valid()` 接受它，但 `AllSummaryDimensions()`（批量 6 维）保持不变 |
| `domains/hooks/compression/summary/summarizer.go` | 4 个 switch（BuildPrompt / SystemPromptForDimension / MaxTokensForDimension / TemperatureForDimension / DimensionForTaskType）都新增 `document_summary` case |
| `domains/hooks/compression/compaction.go` | `buildUserSummaryInstruction` 与 `compactionPromptForTaskType` 增加 `document_summary` 分支 |
| `settings/spec_compression.go` | 新增三个热重载 Spec：`compression.token_threshold_consider`（默认 200k）、`compression.token_threshold_force`（默认 400k）、`summary_models.document_summary`（默认 `auto`） |

---

## 2. 多层会话判断的具体语义

### 2.1 `ShouldTriggerWindow` 的 token 计算

调用前：

- `outboundBody` 已经是 session delta-append + 工具缓存 + thinking block strip 之后的最终 body
- `state` 是从 SessionCache 取出的上一轮已压缩 outbound 的 `TokenEstimate`

调用后：

```go
tokensEst := estimateBodyTokens(outboundBody)  // 已压缩历史 + 本轮 delta
res.TokenBand = classifyOutboundTokenBand(tokensEst)
res.PriorLayerTokens = state.TokenEstimate
```

- `>400k` → `OutboundTokenBandForced`，`Reason = "sliding_window_token_absolute"`，**不依赖 contextWindow**
- `200k–400k` → `OutboundTokenBandPreliminary`，仅标记，不触发
- `<200k` → 走原有字节长度相对阈值（ctxWindow × 0.85 × 3.5），相对阈值未触发则 `ShouldTrigger=false`

### 2.2 absolute force 不被 mutual-exclusion 抑制

`window.go:224-237` mutual-exclusion guard 在 absolute forced 情形下直接放行：

```go
if elapsed < RecentCompressedGuardSecs {
    if res.TokenBand != OutboundTokenBandForced {
        res.Degraded = true
        return res
    }
}
```

`TestWindow_AbsoluteForceIgnoresRecentCompressionGuard` 锁住这条边界。

### 2.3 临时模式提升

`SessionCompressor.Prepare` 在 Phase 4：

```go
case OutboundTokenBandForced:
    res.CompressionReason = "token_threshold_forced_absolute"
    if mode != ModeOff && mode != ModeDeltaOnly {
        mode = ModeSmart   // 强制摘要/机械裁剪回退路径
    }
```

显式 `off` / `delta_only` 仍受尊重；legacy `auto_threshold` / `on_4xx` 在 >400k 时被一次性升级为 smart。thinking strip 之后，`ShouldTriggerWindow` 再次评估（line 363）。如果 strip 把 outbound 拉到阈值以下，Phase 5 的 switch 会把 `CompressionReason` 重置为空，避免误报。

### 2.4 `prepareResult.CompressionReason` 重置

Phase 4 设了 reason，但 Phase 5 重新评估后必须清零：

```go
res.CompressionReason = ""  // line 366
switch winResult.TokenBand {
case OutboundTokenBandForced:    res.CompressionReason = "token_threshold_forced_absolute"
case OutboundTokenBandPreliminary: res.CompressionReason = "token_threshold_preliminary"
}
```

---

## 3. Receipt-time estimate 与真实 usage 的优先级

### 3.1 三类 entry 都先写 receipt estimate

- `recordInitialRequestLog`（in_progress 行）— `promptTokensEstimateFromContext` 返回非 nil 时写入 `PromptTokens`+`UsageSource=estimated`
- `emitTelemetry`（success 行）— 同上，作为 success row 的初始值
- `BuildFailureEntry`（failure 行）— `buildEntry` 第一行调用 `recordReceiptTokenEstimate()` 幂等覆盖

### 3.2 success 路径覆盖逻辑

```go
PromptTokens = promptTokensEstimateFromContext(...)  // 写入 receipt estimate
// ... capture.SummaryAsMap() 给真值:
if v, ok := m["prompt_tokens"].(int); ok && v > 0 {
    reqLog.PromptTokens = &v
    promptTokensFromLLM = true
}
// ... result.ResponseBody 给真值:
if pt > 0 && !promptTokensFromLLM {
    reqLog.PromptTokens = &pt
    promptTokensFromLLM = true
}
// 终态:
if promptTokensFromLLM { reqLog.UsageSource = LLM }
else if reqLog.UsageSource == nil && (PromptTokens || CompletionTokens != nil) { reqLog.UsageSource = estimated }
```

含义：real LLM usage 总是覆盖 receipt estimate，并把 source 切到 `llm`；只有当 upstream 真的不返回 usage 时（minimax / volcengine 路径）才保留 `estimated`。

---

## 4. 数据库列与 telemetry 字段

未新增列。审计可观测性通过复用 `compression_reason` + `compression_meta` JSONB 实现：

- `compression_reason`：填 `token_threshold_forced_absolute` / `token_threshold_preliminary` / 既有值
- `compression_meta`：新键 `token_band` / `outbound_tokens` / `prior_layer_tokens`（仅在 band 触发时写入，保留原 v7 字段）

`OutboundStrategy == "" && OutboundTokenBand == ""` 早返回避免无意义写入（`request_log_pipeline.go:1109`）。

---

## 5. 关闭 / 调优入口

| 场景 | 操作 |
|---|---|
| 完全关闭 absolute force | `compression.token_threshold_force = 0`（`OutboundTokenBandForced` 永不命中） |
| 调低门槛 | `compression.token_threshold_force = 250000` 等 |
| 关闭考虑档 | `compression.token_threshold_consider = 0` |
| 文档压缩专用模型 | `summary_models.document_summary = "minimax-text-01,gemini-2.5-flash"` |
| 整个压缩关 | `compression.mode = off`（之前已存在） |
| 退回通用压缩队列 | `summary_models.document_summary = ""`（fallback 链自动接住） |

---

## 6. 已验证

- `go build ./...`
- `go vet ./domains/streaming ./domains/hooks/compression/... ./config/... ./settings/...`
- `go test ./... -count=1`（exit code 0）
- 关键回归测试：`TestRequestLogContext_BuildFailureEntry_UsesReceiptTokenEstimate`、`TestWindow_AbsoluteForceUsesActualOutboundTokens`、`TestWindow_AbsoluteForceIgnoresRecentCompressionGuard`、`TestWindow_PreviouslyCompressedSessionBelowAbsoluteThreshold`、`TestWindow_PreliminaryBandDoesNotForceCompression`、`TestResolveDocumentSummaryModelConfig`、`TestDocumentSummaryDimensionIsValidButNotInDefaultBatch`

---

## 7. 剩余事项 / 后续建议

1. **观测仪表盘尚未制作**。`compression_reason` / `compression_meta.token_band` 现在已经能 SQL 检索，但 admin UI 还没消费这些字段。建议下一步：给 `request_logs_hot` 增加一个 `token_band` 列（从 JSONB 提取）或在 admin/compression/stats 增加按 band 的强制/预备次数计数器。
2. **Tiktoken 替换尚未做**。当前 `tokenest.FromChars(len(body))` 是个保守 4 字符 ≈ 1 token 估算。对于 CJK/代码混合请求仍然偏低。建议下一步：把 `EstimateInputTokens` 切到 tiktoken-go，并发场景下用单飞（singleflight）避免每个请求都重新加载模型。
3. **document-summary 维度的覆盖度待观察**。`ResolveModelConfig` 已沿既有 fallback 链接住；但 `summary_models.document_summary` 默认 `auto`，运营商首次未设置时实际使用 `compression.llm_model`。如果希望默认就是 `gemini-2.5-flash` 这类廉价模型，应在 settings default 处直接写明而非 `auto`。
4. **streaming 流中间的截断行为**。当 `streamStarted=true` 时 `SkipStream=true`，不进入压缩分支。`OutboundTokenBand` 此时已经算好但不会触发 —— 如果运维想看到流式过程中"我差点要压"的提示，需要另外加 metric。当前没做。
5. **跨协议 compatibility**。document-summary 路径只在 `taskType="document_summary"` 时生效（仅强制 band 进入），其他维度走既有路径；`compactionPromptForTaskType` 也只是 fallback 链的辅助，目前没看到 Anthropic-only / OpenAI-only 兼容性破坏。
6. **大 batch 测试**。`go test ./domains/streaming` 单跑 22s，`./tests/session_cache` 跑 37s；CI 上要确保不被 parallel 限速拖垮。

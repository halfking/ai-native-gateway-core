# 02 — 压缩（Compression）

> 主题：压缩管线的编排、触发、引擎、门禁、token 计数、可观测。本仓库是**单一无损/有损管线**，omniroute 是**多引擎可堆叠管线 + 多重门禁**。

## 1. 现状对比

| 维度 | llm-gateway-go | omniroute |
|---|---|---|
| **架构** | 单编排器 `SessionCompressor.Prepare`（`session_compressor.go:144`）：delta-append → thinking strip → 窗口触发 → 工具轮 strip → LLM 摘要 / 机械 trim | 多引擎 + 可堆叠 stacked pipeline（`compression/strategySelector.ts:206,853`）：~12 引擎（lite/caveman/aggressive/ultra/rtk/codex-responses/omniglyph/llmlingua/headroom/session-dedup/ccr/relevance） |
| **触发** | 三 OR 触发：token(0.85×win×3.5 chars) / count(50) / idle(300s+10 msg)（`window.go:101`） | mode 选择即启用 + autoTrigger(token) + 缓存感知调整（`strategySelector.ts:83,234`） |
| **摘要** | **真 LLM**（`tryLLMSummary` → `summarymodel`，`session_compressor.go:429`）+ 失败回退 v7 无损 compaction | **规则** `RuleBasedSummarizer`（`summarizer.ts:197`，抽 intents/files/errors）；LLM-ready 接口未接 |
| **工具裁剪** | `StripToolInfo` keepLast=2 + `toolChainIntact` 完整性守卫（`strip.go:69-180`）；2026-08-06 修复 keep-last-N 混用完整/不完整轮 | `fixToolPairs` orphan 修复（`contextManager.ts:608`，跨格式）—— 在上下文管理而非压缩内 |
| **门禁** | NeverWorse 上送前比较（`handler.go:2561`）；`Lossiness` 分类 | **引擎熔断**（`pipelineEngineBreaker.ts`）+ **保真度门禁**（`fidelityGate.ts`）+ **风险门禁**（`riskGate/`，压缩前 mask secret）+ **硬预算后处理**（`hardBudget.ts`）+ **膨胀守卫**（`strategySelector.ts:804`） |
| **缓存** | 无压缩结果 memo | 结果 memo（`resultMemo.ts`，需显式 principalId 防跨主体泄漏） |
| **可观测** | `Lossiness` + compression meta（`SessionState`） | `CompressionStats`：original/compressed/savings/engineBreakdown[]/riskGate/liveZone/rtkRawOutputPointers（`types.ts:284`） |
| **配置** | 模式 + env + settings_kv 少数键 | **~25 嵌套配置对象**（`types.ts:174`）—— 复杂度极高 |
| **可预览** | 无 | `POST /api/compression/preview`（返回 diff/preservedBlocks/validation，`src/app/api/compression/preview/route.ts`） |
| **token 计数** | `chars/3.5`（`diff.go:330`） | tiktoken + Anthropic PNG IHDR 数学（`stats.ts:122,49`） |

## 2. omniroute 的优点（可吸收）

1. **引擎注册表 + 熔断**（`engines/registry.ts` + `pipelineEngineBreaker.ts`）：单引擎抛错/连续失败不杀请求，自动 cooldown + half-open 探测。本仓库单管线无引擎级熔断——LLM 摘要失败靠回退 compaction，但没有“摘要在 N 次失败后自动停用一段时间”的机制。**强烈建议吸收**。
2. **保真度门禁 + 膨胀守卫**：`fidelityGate` 阻止语义损坏的压缩通过；`applyStackedInflationGuard`（`:804`）拒绝在“什么都没压”时虚报节省。本仓库 NeverWorse 是总量比较，没有 per-stage 语义保真度检查。**建议吸收保真度门禁**（膨胀守卫与现有 NeverWorse 部分重叠）。
3. **风险门禁（压缩前 mask secret）**（`riskGate/`）：把 API key / 密钥在送压缩前掩码，压完恢复。本仓库压缩会把完整 body 送进 LLM 摘要（`tryLLMSummary`）——**安全收益明显，建议吸收**。
4. **结果 memo（gated on principalId）**：确定模式下复用压缩结果。本仓库每轮重算。**可吸收**（需显式 tenant+session 维度，防跨主体泄漏）。
5. **缓存感知压缩**（`cachingAware.ts` + `preserveSystemPromptMode`）：压缩时尊重 provider prompt-cache 前缀，不动可缓存段。与 01 的 M4 联动。**建议吸收**。
6. **可预览 REST**：运维/调试价值高。**建议吸收**（复用现有 compression-bench harness 包成 admin endpoint）。
7. **Anthropic PNG 图片 token 数学**（`stats.ts:49`）：从 base64 前 64 字符读 IHDR 算图片 token，不解码图像。本仓库 `chars/3.5` 对多模态会话严重低估。**建议吸收**到统一 token 估算（见下）。

## 3. 本仓库的优点（保留）

1. **真 LLM 摘要**：质量上限远高于 omniroute 的规则摘要。保留，并加熔断保护。
2. **完整性守卫 `toolChainIntact` + fail-open**：比 omniroute `fixToolPairs`（事后修补 orphan）更前置——本仓库是“剥离前检查会不会产生 orphan，会则放弃剥离”。**保留并优于对方**。
3. **增量 delta-append（LCS）+ summary marker 免索引**：omniroute 假设客户端全量重发，没有 delta 概念。**保留**。
4. **`Lossiness` 分类**：none/tail/whole，给运维可恢复性信号。保留。
5. **v7 无损 compaction 回退**：LLM 失败时的安全网。保留。

## 4. 缺口 / 债务

| 编号 | 债务 | 证据 | 影响 |
|---|---|---|---|
| C1 | **无引擎级熔断** | 单管线 + 回退 | LLM 摘要连续失败会持续打满摘要模型配额 |
| C2 | **LLM 摘要送完整 body，无 secret mask** | `tryLLMSummary`→`extractConversationText` | 敏感信息泄漏到摘要模型 |
| C3 | **无结果 memo** | 每轮重算 | 重复对话重复算钱 |
| C4 | **token 估算粗糙且常数不一** | 压缩 `chars/3.5`、V2 builder `chars/4` | 多模态低估、跨模块误触发 |
| C5 | **`_to-be-deprecated/compressor/` 旧副本带 pre-fix `filterMessages` bug** | `_to-be-deprecated/compressor/strip.go:166` | 误接回退修复 |
| C6 | **`Estimator.NeedsCompression` 死代码**（`estimator.go:43-46,122`） | 两套阈值机制 | 混淆 |
| C7 | **无压缩预览/调试 endpoint** | 无 | 排障靠日志 |
| C8 | **无缓存感知** | marker 注入第一条 assistant | 破坏 prompt-cache 前缀 |

## 5. 优化建议（带优先级）

### C5（P0）删除旧压缩副本 `TARGET-BOUNDARY`
`grep -r "_to-be-deprecated/compressor"` 确认无生产引用后整目录删除。**与所有其他项解耦，先行**。

### C2（P0）LLM 摘要前 secret mask `NEW-DESIGN`（吸收 omniroute riskGate）
在 `extractConversationText` 之前加一个 `maskSecrets(body) → (masked, restoreFn)`：正则识别常见密钥形态（sk-/AKIA/Bearer/`"api_key"` 等），掩码后送摘要，恢复后再注入 marker。复用 `safety/` 现有 PII 能力若有。
- 验收：含假 key 的 body 摘要后，marker 文本不含原 key。

### C1（P1）引擎/摘要熔断 `NEW-DESIGN`（吸收 omniroute）
为本仓库的“可失败子步骤”（LLM 摘要、compaction）加一个轻量熔断器（`domains/hooks/compression/breaker.go`）：连续 N 次失败 → cooldown → half-open 探测。熔断期间直接走机械 trim，不打摘要模型。
- 配置：`compression.summary_breaker.{threshold,cooldown_ms}`，settings_kv 热加载。
- 验收：摘要模型不可用时，QPS 不降，错误计数达阈值后摘要调用归零。

### C4（P1）统一 token 估算 `NEW-DESIGN`
新建 `domains/tokencount/`（或扩展 `internal/ir`）：默认 `chars/4`（与 V2 builder 对齐），多模态走 Anthropic PNG 数学（吸收 omniroute `stats.ts:49`）；可选接真 tiktoken（成本权衡）。压缩、窗口触发、V2 builder 统一调用同一函数。删除 `Estimator.NeedsCompression`（C6）。

### C3（P2）压缩结果 memo `NEW-DESIGN`（吸收 omniroute）
key = hash(tenantID, sessionID, outboundHash, mode)；TTL 短（5–10 min）；命中直接复用 `OutboundResult`。**必须显式带 tenant+session 维度**（吸收 omniroute principalId 教训）。

### C7（P2）压缩预览 endpoint `NEW-DESIGN`（吸收 omniroute）
`POST /admin/v1/compression/preview`：输入 messages + 当前配置，返回 before/after/token saving/Lossiness/被剥离的工具轮。复用 `cmd/compression-bench` 的回放逻辑。仅 admin。

### C8（P1）缓存感知压缩 + cache-safe marker 注入 `NEW-DESIGN`
与 01-M4 合并实现：marker 注入位置随 provider cache 前缀自适应；压缩跳过可缓存前缀段。需 04 的 prompt-cache 前缀分析先行或并行。

## 6. 不做（明确非目标）

- **不引入 omniroute 的 ~12 引擎全量**（caveman/omniglyph/llmlingua/...）：本仓库单管线 + LLM 摘要已覆盖主场景；引入全套会带来 omniroute 那样的 ~25 嵌套配置负担。仅吸收**编排/门禁/熔断**的工程模式，不吸收具体引擎。
- **不引入 stacked 多引擎串行**：本仓库语义是“压缩 = 摘要 or 机械裁剪”，不是“多引擎逐级压”。串行引擎会让早期有损引擎污染后续输入（omniroute 自身也有此风险）。
- **不做 LLMLingua SLM**：需额外 ONNX 模型部署，收益不明确。

## 7. 与其他主题的依赖

- C4（token 统一）→ 被 03（窗口触发）、04（缓存预算）依赖。
- C8（缓存感知）→ 依赖 04 的 prompt-cache 前缀分析。
- C5（删旧包）→ 与 01-M6 同项，先行。

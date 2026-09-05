# 审计报告：流式处理 + 上下文压缩 + 缓存分层

**审计时间**: 2026-09-01
**审计范围**: 流式转发、>1048576 token 上下文压缩、Provider 4xx 重试压缩策略、原始/脱敏/压缩三层缓存 + 偏移记录
**审计方法**: 静态代码审计 + 关键路径验证
**结论**: 四项需求均已部分实现；流式化成熟、上下文压缩路径完整、60% 激进压缩已就绪；存在多个具体缺陷需修复，详见下文。

---

## 一、整体评估矩阵

| 需求 | 实现度 | 关键风险 |
|---|---|---|
| 所有请求尽可能流式 | 🟢 完整 | `maxBodySize=32 MiB` 强制全量缓存；流式前需要 `bufferRequestBody` 一次性缓冲 |
| 网关支持 2M 上下文，超 1M 时主动压缩至 60% | 🟡 部分 | 1M 预压缩已就绪（`promptBudgetExceeded`）；**2M 上限 + 主动触发 60% 预压缩缺失** |
| Provider 上下文超长 → 触发压缩到接近供应商端的 60% | 🟢 完整 | 60% `CompressMessagesAggressively` 已实现；4xx 后 3 层状态机（mechanical → memora → LLM summary）已实现 |
| 多策略并行压缩 | 🔴 **缺失** | 实际是**顺序串行**执行（`strategy.Runner.RunWithBody` 是顺序 for-loop），无 errgroup/parallel 实现 |
| 三层缓存（原始/脱敏/压缩） | 🟡 部分 | 测试 mock 实现 vs 生产 `SessionState` 单层混合；**脱敏层与压缩层没有独立 record offset 的对齐结构** |
| 记录压缩起止位置 + 后续续接 | 🟢 完整 | `CutMarker` 包含 `CutIndex`/`SystemMsgCount`/`SummaryText`，`IncrementalBuild` 重建 outbound |

---

## 二、流式处理（Streaming）

### 2.1 现状

流式路径覆盖完整，主要入口与桥接：

| 入口 | 文件:行号 |
|---|---|
| OpenAI Chat → Upstream | `domains/streaming/stream.go:518-590` (`StreamChat*`) |
| Anthropic 透传 | `domains/streaming/anthropic_bridge.go:93` (`StreamAnthropicPassthrough`) |
| OpenAI → Anthropic SSE | `domains/streaming/anthropic_stream.go:46-62` |
| Anthropic → OpenAI SSE | `domains/streaming/anthropic_to_openai_stream.go:263-593` |
| Anthropic → Responses SSE | `domains/streaming/responses_bridge.go:53-857` |
| 早期 keepalive | `domains/streaming/handler.go:187-251` (`preStreamKeepalive`) |
| Chat handler 入站分支 | `domains/streaming/handler.go:1619-1627`, `isStream := reqBody.Stream (line 2769)` |

**结论**: 流式从客户端 → 网关 → upstream → 客户端全链路贯通，SSE keepalive、断流兜底 (`pendingCapturer`)、协议转换均有覆盖。

### 2.2 缺陷

**P1 — 入站 body 必须全量缓存才能进入流式分支**  
`domains/streaming/handler.go:1619-1630` → `serveHTTPInner` → `bufferRequestBody`（`request_meta.go:145`）必须读完整 body 才能：
1. 提取 `model` 字段（line 219：`extractModelFromBody`）
2. `promptBudgetExceeded` 估算 tokens（line 2181）
3. `sanitizeInputMiddleware` 替换占位符（line 1623）
4. `sessionCompressor.Prepare` 准备压缩（line 3440）

对于超大 body（接近 1M token / 4MB），一次性 `io.ReadAll` 与后续多份复制是 memcg OOM 的根因（`request_meta.go:26-37` 注释明确记载 245 memcg OOM 53 次）。当前有 `LLM_GATEWAY_MAX_PROMPT_TOKENS=1048576` 兜底，但**没有 streaming body 解析路径**——比如边读边替换占位符、边读边算 token。

**P2 — `maxBodySize = 32 MiB` 硬编码**  
`handler.go:2165` 直接 413 拒绝超出 32 MiB 的 body，**长上下文请求**（如 Cursor 把整个 repo 灌进 system prompt）很容易超过。这是用户需求中 "2M 上下文" 的硬天花板。

**P0 — `isStream` 决定发生在 body 解析后**  
`isStream := reqBody.Stream` 在 `handler.go:2769`，意味着如果客户端发了非 stream body（含 stream: true 标志但非 SSE 兼容），网关会按非流处理。需要在 auth/body 解析前就检查 `Accept: text/event-stream`。

---

## 三、>1048576 上下文压缩

### 3.1 1M token 入口级守卫（🟢 已实现）

| 组件 | 位置 |
|---|---|
| 设置项定义 | `settings/spec_gateway.go:7-22` (`gateway.max_prompt_tokens`，Default=1048576) |
| 环境变量 | `LLM_GATEWAY_MAX_PROMPT_TOKENS`，`request_meta.go:52` |
| 热加载 | `promptBudgetLimit()`，`request_meta.go:44-65` |
| 拒绝触发 | `handler.go:2181` (`promptBudgetExceeded` → 413 prompt_too_large) |
| OpenAI 分支拒绝 | `domains/streaming/responses.go:266-272` |
| Anthropic 分支拒绝 | `domains/streaming/messages.go:269-273` |

行为：超过 1M 默认直接 **413 拒绝**，不进入压缩流程。

### 3.2 主动压缩触发（🟡 部分实现）

| 触发点 | 文件:行 | 目标比例 | 备注 |
|---|---|---|---|
| Pre-request (`applyOptionalOpenAIStrategies`) | `executor_chat.go:1913-1925` | **85%** | `CompressMessagesIfNeeded` 默认 soft limit 是 `window × 0.85 × 3.5 = window × 2.975`（`ctx_compress.go:46-52, 463-471`） |
| Pre-request Anthropic | `executor_anthropic.go:595-601` | **85%** | 同上 |
| `Compress()`（mode=auto_threshold） | `compressor.go:498-617` | 85% → fallback mechanical | Lite/Caveman/ToolFocused stages → mechanical |
| `runCompressionStrategies` | `compression_strategy.go:24-50` | 由策略链决定 | 顺序执行 |
| 4xx 之后 `handleContextLengthRecovery` | `context_summarize.go:991-1287` | **60%** | `CompressMessagesAggressively` |

### 3.3 缺陷

**P0 — 用户需求 "超 1M 时压缩到 60%" 没有真正落实**  

用户期望：会话超 1M token → 主动压缩到 60%（不依赖供应商 4xx 触发）。

代码实际：
- 1M 是**硬上限**（`promptBudgetExceeded`），超过直接拒绝（413）
- 60% 压缩**只在供应商端 4xx 之后**触发（`context_summarize.go:1183-1192` 注释明确："We are here because the upstream already rejected this body"）

**预期实现**：在 `promptBudgetExceeded` 之前，或 `sessionCompressor.Prepare` 入口处增加一层 `if estimatedTokens > 1048576 → CompressMessagesAggressively` 主动预压缩。

**P0 — 没有 2M 上限的定义**  
用户提到 "网关可以接收达到 2M 的上下文"，但代码中只找到：
- `promptBudgetDefaultTokens = 1048576`（1M）
- `settings/handoff_specs.go:92` 的 `handoff.summary_max_tokens Max=2000000`（仅 handoff 摘要上限，非上下文窗口）

**预期实现**：在 `promptBudgetLimit` 增加 `LLM_GATEWAY_MAX_PROMPT_TOKENS_MAX=2097152` 软上限（高于 1M），超 1M 但 ≤ 2M 时主动预压缩 60%。

**P1 — "压缩后下次不用反复压缩" 没有真正闭环**  

`CutMarker` 与 `IncrementalBuild` 完整支持增量构建（`cut_marker.go:173-228`），但是 `IncrementalBuild` 在 `recovery_coordinator.go:133-149` **只在 4xx 之后才被检查**。Pre-request 路径不读 `CutMarker`，每次新请求都从完整 body 重新评估 `NeedsCompression`。

**预期实现**：在 `applyOptionalOpenAIStrategies` / `legacyAnthropicBody` 入口处先检查 `SessionCache.GetOrLoad` 拿 `CutMarker`，若未过期且 body 长度超过阈值则直接 `IncrementalBuild`。

---

## 四、Provider 上下文超长 → 自适应压缩

### 4.1 错误分类（🟢 已实现）

`errorsx/classify.go:56` 定义 `KindContextLength = "context_length_exceeded"`，多家 provider 的关键字匹配覆盖到位（Anthropic/OpenAI/MiniMax）。

### 4.2 上下文限制动态发现（🟢 已实现）

`context_summarize.go:1009-1067` 解析 supplier error body 提取真实 limit，写入 `targetCand.ContextWindow`，并异步持久化到 `credential_model_bindings.context_window_override`（`internal/dbx/context_limit_updater.go`）。指标 `llmgw_context_limit_discovery_total` 埋点。

### 4.3 60% 激进压缩（🟢 已实现）

`ctx_compress.go:507-528`:
- `CompressMessagesAggressively`：OpenAI chat → 压到 `window × 0.60`
- `CompressAnthropicMessagesAggressively`：Anthropic → 压到 `window × 0.60`

`context_summarize.go:1183-1192` 在 mechanical trim 阶段使用激进版（注释："We are here because the upstream already rejected this body... trimming to just under the limit leaves no room for the response"）。

### 4.4 三层恢复状态机（🟢 已实现）

`context_summarize.go:970-1287` + `recovery_coordinator.go:102-255`:
1. **Phase 0**: SessionCache 增量复用（`IncrementalBuild`）
2. **Phase 1**: Smart window cut point（`FindOptimalCutPoint`，`window.go:75-259`）
3. **Phase 2a**: LLM summary（`tryLLMContextCompaction`，`context_summarize.go:1085-1153`）
4. **Phase 2b**: mechanical fallback（`extractSummaryText`）
5. **Phase 3**: 持久化 `CutMarker` 到 `SessionCache` + DB

### 4.5 多策略"并行"压缩（🔴 **与需求不符**）

用户期望："根据当前上下文与目标的差异，需要启动不同的压缩方式，或者用多种并行压缩，找到信息量最大的"。

代码实际：
- `strategy.Runner.RunWithBody` 是**严格顺序串行**：`runner.go:115-169` 中 `for _, s := range chosen { s.Apply(...) }`，每段套 NeverWorse 守卫
- Lite → Caveman → ToolFocused → mechanical 是**链式**，每段输入是上一段输出
- **没有任何 errgroup/goroutine 并行尝试多个策略**
- 没有 "信息量最大" 的多策略比较逻辑

**预期实现**：
1. 引入并行 fan-out：mechanical、LLM summary、caveman 三策略并发执行
2. 用信息量估计（如非空 token 数 / embedding cosine similarity）排序
3. 选最优结果写入 `CutMarker`

### 4.6 自适应选择器（🟡 部分实现）

`strategy/selector_adaptive.go` 实现 **AdaptiveSelector**：按 `CostTier` 升序排序 strategy，累加预估压缩率直到满足预算。**但这还是顺序执行**，不是真正的并行尝试。

### 4.7 缺陷汇总

| 优先级 | 缺陷 | 影响 |
|---|---|---|
| P0 | 没有真正的多策略并行压缩 | 长 body 时压缩慢、信息量损失不可控 |
| P1 | 60% 激进压缩仅在 4xx 后触发 | 需主动预压缩 |
| P1 | 415 status 处理不明确（仅在 `shouldHeuristicCompact` 中检测） | 部分 provider 用 415 表达超长，未走 recovery |

---

## 五、三层缓存 + 偏移记录

### 5.1 测试中的三层缓存（🟡 仅测试 mock）

`tests/session_cache/three_tier_cache_test.go:26-339` 定义了 `RawSessionCache` / `CompressedSessionCache` / `AuditedSessionCache` 三层 mock 实现，含 `AlignmentInfo` 位置对齐结构（`helpers.go`）。**这是测试夹具，不是生产代码**。

### 5.2 生产代码的"三层"语义（🟡 混合到单层）

生产代码把三层信息合并到 `domains/hooks/compression/session_cache.go:148-225` 的 `SessionState`：

```go
// v8 三层语义（注释）
// L1 fields (raw session, true values):
RawTokenEstimate int   // token count before sanitization/compression
RawMsgCount      int   // message count before compression

// L2 fields (compressed session, placeholders):
CompressedTokens     int
CompressedMsgs       int
CompressedPrefixHash string
CompressionQuality   CompressionQualityScore

// L3 fields (audited session, sanitize map):
SanitizeMapRef string
SanitizeStats  SanitizeStats
```

存储层：
- L1: 进程内 LRU（`ll *list.List + l1 map[string]*l1Entry`）
- L2: Redis（`redisKey = "session:sc:" + tenantID + ":" + gwSessionID + ":v1"`）
- L3: PostgreSQL `request_logs`（`LastOutboundForSession`）

### 5.3 V2 独立缓存（🟡 与 V1 并存）

`domains/session/v2/cache_v2.go:64-89` 定义 `SessionStateV2` 与 `CompressionMeta`（含 `LastCompressedAt`、`SummaryMarker`、`CompressedPrefixHash`、`Strategy`）。**V2 与 V1 并存但未统一**。

### 5.4 脱敏偏移（🟢 已实现但与压缩层耦合弱）

`security/sanitize/smart_sani_guard.go:240-334`：
- 占位符→原文映射写入 Redis `session:{tenantHash}:{sid}:sanitize`
- **每类已用最大编号**写入 `session:{tenantHash}:{sid}:sanitize:offsets`（Redis Hash）
- 这样下一次请求不会重号（phone、email、id_card 等分类计数）

`domains/hooks/compression/sanitize_info.go:114-134` 镜像 Redis key 结构。

### 5.5 CutMarker（🟢 完整）

`domains/hooks/compression/cut_marker.go:32-66`:
```go
type CutMarker struct {
    Version        int    // schema 版本
    CreatedAt      int64  // unix ts
    SourceMsgCount int    // 压缩前消息数
    SystemMsgCount int    // 保留的 system 数
    CutIndex       int    // 在非 system 部分内的切点
    SummaryMarker  string // smm_v1 hash
    Strategy       string // smart_window/mechanical_trim/llm_summary/memora_l1
    BytesBefore    int
    BytesAfter     int
    SummaryText    string // 仅 L1 in-process
}
```

`GlobalCutIndex()`（line 99）返回绝对索引 = `SystemMsgCount + CutIndex`，下次请求据此接续。

### 5.6 缺陷

**P0 — 三层缓存没有真正的 "原始 / 脱敏 / 压缩" 物理分层**  
当前 `SessionState` 把三层信息**塞进一个结构体**。当 sanitization 与 compression 同时发生时：
- L1 RawTokenEstimate 与 L2 CompressedTokens **冗余存储**
- 没有独立 Redis key 跟踪 "压缩前后 message offset 映射"
- 当同一会话多次压缩（增量），原始消息 → 压缩消息 → 脱敏消息 三层索引没有持久化

用户需求："需要记录压缩在原文的起止位置，但于回复的时候及下次压缩时，知道该从哪里开始，并且能顺利接接上全文内容"。

**当前实际**：
- 压缩起止位置：`CutMarker` 记录 `CutIndex` / `SourceMsgCount`，**完整覆盖**
- 全文接续：`IncrementalBuild`（`cut_marker.go:173-228`）从 `marker.GlobalCutIndex()` 取 tail 拼接 system + summary + tail
- 脱敏层 offset：`sanitize:offsets` Redis Hash 记录每类已用最大编号（仅占位符编号，不是消息 offset）
- **缺口**：原始层 → 压缩层 → 脱敏层的 **message index 映射** 没有持久化结构

`SessionState.AlignmentMap []AlignmentInfo`（`session_cache.go:206`）已经定义但只是可选 `omitempty`，缺少实际填充路径。

**P0 — 脱敏层与压缩层的更新时序没有原子保证**

`smart_sani_guard.go:240-244` 写 sanitize map 是 fail-open 的；compression 写 `CutMarker` 是另一个事务。如果 sanitize 成功 + compression 失败，重试时**同一段原文可能被用不同的 placeholder 编号重新脱敏**，导致回复时映射错位。

**预期实现**：
1. 增加 `sanitize_offset_to_compressed_offset` 的 record（Redis Hash 或 DB JSONB）
2. 在 `IncrementalBuild` 重建 outbound 时检查 alignment map，校验脱敏偏移量与压缩偏移量一致
3. `CutMarker` 增加 `PreSanitizeMsgRangeStart/End` 字段，记录压缩前对应的原文区间

**P1 — `tests/session_cache/` 不在生产 build 路径**  
`three_tier_cache_test.go` 是测试包，不会编译进二进制。需要把三层抽象提到 `domains/hooks/compression/` 作为生产 API。

**P1 — Sanitize Map 的 TTL 与 SessionCache 的 TTL 不一致**  
`smart_sani_guard.go` 的 `m.ttl` 与 `SessionCache` 的 `sessionCacheRedisTTL()` 是独立设置。如果不一致，可能出现 sanitize map 存在但 session cache 已驱逐导致恢复失败。

---

## 六、关键修复建议优先级

| 优先级 | 修复 | 涉及文件 |
|---|---|---|
| **P0** | 实现 1M ≤ tokens ≤ 2M 区间内的主动 60% 预压缩 | `handler.go:2181` 之前 / `sessionCompressor.Prepare` 入口 |
| **P0** | 实现真正的多策略并行压缩（errgroup fan-out + 信息量比较） | `domains/hooks/compression/strategy/runner.go` |
| **P0** | 增加 2M 软上限设置项 `gateway.max_prompt_tokens_soft_max` | `settings/spec_gateway.go` |
| **P0** | 把测试 `three_tier_cache_test.go` 抽象升级到生产 `domains/hooks/compression/threetier/` | 新建包 |
| **P0** | `CutMarker` 增加 `PreSanitizeOffsetRange` 字段并写入 SessionState，串起 原始→压缩→脱敏 三层 | `cut_marker.go:32`, `session_cache.go:206` |
| **P1** | 入站 body streaming 解析（边读边脱敏/边算 token），减少 memcg OOM | `handler.go` + `sanitize/smart_sani_guard.go` |
| **P1** | Pre-request 路径读 `CutMarker` 走 `IncrementalBuild`，避免每次从全量 body 评估 | `executor_chat.go:1913`, `executor_anthropic.go:595` |
| **P1** | `maxBodySize = 32 MiB` 改为可配置 | `handler.go:2165` |
| **P1** | Sanitize TTL 与 SessionCache TTL 对齐策略 | `smart_sani_guard.go` + `session_cache.go` |
| **P2** | 把 415 status 也归入 context_length recovery | `shouldHeuristicCompact` (`context_summarize.go:71`) |

---

## 七、验证清单

- [x] 流式路径全链路贯通
- [x] 1M token 入口级拒绝（`promptBudgetExceeded`）
- [x] 4xx context_length 后 60% 激进压缩（`CompressMessagesAggressively`）
- [x] `CutMarker` 记录压缩起止位置 + `IncrementalBuild` 续接
- [x] 脱敏占位符按类型独立计数（`sanitize:offsets`）
- [ ] **缺失**：1M-2M 区间主动预压缩
- [ ] **缺失**：2M 软上限配置项
- [ ] **缺失**：真正的多策略并行压缩
- [ ] **缺失**：三层缓存的生产代码实现（当前为测试 mock）
- [ ] **缺失**：压缩前后 → 脱敏的 offset 对齐结构

---

## 八、相关代码索引

### 流式
- `domains/streaming/handler.go:1619-1630, 2769, 3394-3412` — 入口 + isStream 决策 + pre-stream
- `domains/streaming/stream.go:518-590` — StreamChat 主路径
- `domains/streaming/anthropic_stream.go:46-62` — OpenAI→Anthropic SSE
- `domains/streaming/responses_bridge.go:53-857` — Anthropic→Responses

### 上下文压缩
- `settings/spec_gateway.go:7-22` — `gateway.max_prompt_tokens`
- `domains/streaming/request_meta.go:39-77` — `promptBudget*`
- `domains/transformation/ctx_compress.go:44-77, 463-528` — `CompressMessagesIfNeeded`/`Aggressively`/`NeedsCompression`
- `domains/hooks/compression/compressor.go:276-617` — `ShouldCompressPreRequest` + `Compress`
- `domains/streaming/executors/context_summarize.go:991-1287` — `handleContextLengthRecovery` 3 层状态机
- `domains/hooks/compression/recovery_coordinator.go:102-255` — `RecoveryCoordinator.Recover`
- `domains/hooks/compression/strategy/selector_adaptive.go` — 自适应升级选择器
- `domains/hooks/compression/strategy/runner.go:115-169` — 顺序串行 Runner

### 三层缓存
- `domains/hooks/compression/session_cache.go:148-340` — `SessionState` + `SessionCache` (L1/L2/L3)
- `domains/session/v2/cache_v2.go:64-89` — V2 `SessionStateV2` + `CompressionMeta`
- `domains/hooks/compression/cut_marker.go:32-228` — `CutMarker` + `IncrementalBuild`
- `security/sanitize/smart_sani_guard.go:240-334` — sanitize map + offset 持久化
- `domains/hooks/compression/sanitize_info.go:114-134` — 镜像 Redis key
- `tests/session_cache/three_tier_cache_test.go:26-339` — 测试 mock 三层缓存

### Provider 错误分类
- `errorsx/classify.go:56, 220, 751, 920, 1257, 1306` — `KindContextLength` + 多家匹配
- `errorsx/context_limit_parser.go` — 错误体解析 limit
- `internal/dbx/context_limit_updater.go` — discovered → DB 持久化

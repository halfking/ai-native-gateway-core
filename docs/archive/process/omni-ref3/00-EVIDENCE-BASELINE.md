# 00 — 证据基线（双仓 file:line 核对清单）

> 本文件列出 omni-ref3 所有结论背后的**源码证据**，标注证据等级。评审者应优先核对 `SOURCE-VERIFIED` 条目；任何被标 `NEW-DESIGN` 的建议在落地前必须重新核对当前 checkout（迁移号、函数签名会随提交变化）。
>
> 核对时间：2026-08-06。本仓库基线 commit `2d1d57bd`。omniroute checkout `package.json` 3.8.49。

## 0. 证据等级图例

| 标签 | 含义 |
|---|---|
| `SOURCE-VERIFIED` | 已读源码并引用 file:line |
| `TARGET-BOUNDARY` | 现有架构边界（不需新证据） |
| `NEW-DESIGN` | 目标不存在，需评审 |
| `BLOCKED` | 依赖未满足 |

---

## 1. 本仓库（llm-gateway-go）证据

### 1.1 会话存储三轨并存 `SOURCE-VERIFIED`

| 事实 | 证据 |
|---|---|
| V1 live：Redis Hash `session:{id}`，含 `Title/Tags/Annotation/TaskID` | `domains/session/session.go:35-71`（struct）、`:373-375`（从 hash 反序列化 tags/title）、`session_state.go:19-44`（hash 字段表） |
| V1 持久化：`request_logs` + `request_logs_bodies`（每轮全量 JSONB） | `domains/sessionsummary/summarizer.go:376-377,467-468`（LEFT JOIN 读取） |
| V2：`gateway.sessions/session_turns/session_bodies/session_turn_logs`，增量 delta | `sql/migrations/startup/430_sessions_v2_schema.sql`；`domains/session/v2/bodies_writer.go:45-51`（Message 结构）、`:69-86`（BodiesRecord delta） |
| V2 是 shadow-write，读路径关闭 | `domains/hooks/compression/session_compressor.go:675-691`（`shouldUseV2` 硬编码 `return false`）、`internal/sessionv2mirror/`（shadow hook） |

### 1.2 归纳能力存在但未接线 `SOURCE-VERIFIED`

| 能力 | 证据 | 是否接 live 元数据 |
|---|---|---|
| LLM 会话摘要 / 标题 / 关键话题 / 用户意图 | `domains/sessionsummary/summarizer.go:54-72`（`SessionSummary`）、`:96-145`（GenerateSummary）、`:148-202`（GenerateTitle）、`:286-329`（GenerateRollingSummary）；落表 `internal/summarystore/store.go` | 写入 `session_summaries`（迁移 310），**未**写回 live `Session.Title/Tags` |
| 规则意图分类 + 漂移检测 | `domains/intentconfig/classifier.go:20-21,244-245`（`ClassifyWithCandidates`/`GetPrimaryIntent`）、`domains/intentconfig/drift.go`（KL 散度漂移）；意图种类 `domains/intentconfig/types.go:25-32`（chat/code/reasoning/summary/translation/extraction/tool_use） | 经 `domains/hooks/intentanalysis/hook.go:119-125` 写入 pipeline metadata，**未**落 `sessions.task_type` |
| 向量聚类（项目 / 主题归纳） | `domains/analysis/clusterer.go:34-66`（`SessionClusterer`）、`:114-130`（coarseKey = intent\|model\|topic）、`:136`（vectorCluster）、`:272-296`（写 `session_clusters` + `session_cluster_members`）、`:309-311`（`session_embeddings`） | 离线 / 定时任务，**未**回填 live session 的 project/tag |
| 逐轮意图演化轨迹 | `sql/migrations/startup/359_session_intent_evolution.sql`（`session_intent_evolution`，turn_number + intent_drift_score） | 表存在，由 intentanalysis hook 写入（验证待补） |
| V2 元数据写回 API | `domains/session/v2/session_aggregator.go:295-298`（`SetSessionMetadata`，写 `task_type/topic/intent`） | **零生产调用方**（`grep '\.SetSessionMetadata'` 仅 test 命中） |

### 1.3 压缩管线 `SOURCE-VERIFIED`

| 事实 | 证据 |
|---|---|
| 单一编排器 `SessionCompressor.Prepare` | `domains/hooks/compression/session_compressor.go:144-385` |
| 增量 delta-append（LCS） | `domains/hooks/compression/diff.go:68`（`BuildOutboundMessages`）、`:227-243`（SHA256/512B 指纹）、`:292-316`（summary marker 免索引） |
| 三触发窗口（token/count/idle） | `domains/hooks/compression/window.go:101`（`ShouldTriggerWindow`）、`:14-30`（默认值） |
| 工具轮剥离 + 完整性守卫 | `domains/hooks/compression/strip.go:69-111`（`StripToolInfo`，keepLast=2）、`:158-180`（`toolChainIntact` fail-open）、`:258-307`（`filterMessages` keep-last-N 修复）、`:208-245`（`detectToolRounds`） |
| thinking 块剥离（无损） | `domains/hooks/compression/strip.go:118-153`（`StripThinkingBlocksOnly`） |
| LLM 摘要 + marker 注入 | `domains/hooks/compression/session_compressor.go:429-454`（`tryLLMSummary`）、`:536-584`（`injectSummaryMarker` `[smm_v1:<sha>]`） |
| NeverWorse 守卫 | `domains/streaming/handler.go:2561`（上送前比较） |
| 旧全量包仍在 `_to-be-deprecated/compressor/`（带 pre-fix bug） | `_to-be-deprecated/compressor/strip.go:166`（旧 `filterMessages(msgs, remove map[int]bool, result)` 签名） |

### 1.4 多级缓存 `SOURCE-VERIFIED`

| 事实 | 证据 |
|---|---|
| 会话状态三级缓存（L1 LRU / L2 Redis / L3 PG） | `domains/hooks/compression/session_cache.go:40-60`（结构）、`:104-156`（`SessionState`）、L1 上限 `l1MaxSessions=1024` |
| V2 元数据缓存（仅 metadata，无 body） | `domains/session/v2/cache_v2.go:45-56`、`:137-196`（`CompressionMetaCache`，2026-08-06 race-fix） |
| 三级 sticky（session/client+model/client） | `domains/routing/sticky.go:25-40`（level 定义 + TTL）、`:133-226`（`GetMultiLevel` 级联）、`:272-352`（`RecordSuccessMultiLevel` 三级写）、`:496-532`（`buildStickyKeys`） |
| 旧版单字段 sticky 仍在 | `domains/session/sticky_router.go`（仅读写 `LastCredentialID`） |
| 语义 / 前缀 / delta / kv cache 各自独立 | `cache/semantic/`、`cache/prefix/`、`cache/delta/`、`cache/kv/`；hook `domains/hooks/cache/hook.go`（PreRouting priority 50） |

### 1.5 IR 与净化 `SOURCE-VERIFIED`

| 事实 | 证据 |
|---|---|
| IR 超集 hub-and-spoke | `internal/ir/types.go:4-17`（设计注释）、`:37-179`（`InternalRequest`）、`:208-218`（`Message`）、`:222-266`（`ContentBlock` 判别联合）、`:384-401`（`ToolDefinition.Raw` 透传）、`:162-169`（`Extensions` 透传未知字段） |
| 净化顺序修复 | `internal/ir/validate_and_fix.go:25-44`（`ValidateAndFixRequest`）、`:47-81`（5 步）、`:52-67`（removeEmptyMessages 先于 SanitizeToolMessages 的注释）、`:89-182`（`removeEmptyMessages`） |
| 工具消息净化（orphan 删除） | `internal/ir/sanitize_tool_messages.go:28-109`（`SanitizeToolMessages`，scope 规则） |
| tool_call_id 生成器（仅 10 个、易碰撞） | `internal/ir/validate_and_fix.go:318-320`（`string([]byte{...})` 逐字符、`index%10`） |
| 三套消息模型不一致 | `ir.Message`（`[]ContentBlock`，强类型）vs `v2.Message`（`Content string`、`ToolCalls []map[string]any`，弱类型，`bodies_writer.go:45-51`）vs `session.Session` 隐式；转换 `internal/sessionv2mirror/hook.go`（`toMessage`，有损） |

---

## 2. omniroute 证据

### 2.1 记忆 / 会话 `SOURCE-VERIFIED`

| 事实 | 证据 |
|---|---|
| 记忆是 keyed CRUD（FTS5 + sqlite-vec + Qdrant） | `src/lib/memory/store.ts:162`（UPSERT by key）、`:122`（scheduleVectorUpsert）、`retrieval.ts:231`（tiered retrieve）；schema `src/lib/db/migrations/{015,022,083,111}` |
| fact 抽取是**正则**，非 LLM | `src/lib/memory/extraction.ts:16-38`（preference/decision/pattern 正则）、`:161`（extractFacts fire-and-forget） |
| 摘要是**首 3 句**启发式，非 LLM | `src/lib/memory/summarization.ts:111`（`generateSummary`） |
| **无**标题 / tag / 项目 / 任务归纳 | 记忆域仅 keyed CRUD；task-aware router（`open-sse/services/taskAwareRouter.ts:311`）只分类**路由 intent**，不是记忆项目 |
| cache-safe 注入（插在最后 user 之前） | `src/lib/memory/injection.ts:105,158`（`injectMemory`，PROVIDERS_WITHOUT_SYSTEM_MESSAGE/:38、PROVIDERS_SYSTEM_MUST_BE_FIRST/:70） |
| 类型化衰减 + 访问免疫 | `src/lib/memory/typedDecay.ts:40`（仅 EPISODIC 30 天）、`DEFAULT_ACCESS_IMMUNITY_THRESHOLD=3` |

### 2.2 压缩 `SOURCE-VERIFIED`

| 事实 | 证据 |
|---|---|
| 多引擎 + 可堆叠管线 | `open-sse/services/compression/types.ts:160`（`CompressionPipelineStep`）、`:389`（默认 stacked `[rtk,caveman]`）；`strategySelector.ts:206`（selectCompressionPlan）、`:853`（applyStackedCompression）、`:959`（async） |
| 引擎注册表 + 熔断 | `engines/registry.ts`（register/get/setEnabled）；`pipelineEngineBreaker.ts`（per-engine breaker，`failureThreshold=3`、`canRunEngine:90`） |
| 保真度 / 风险 / 硬预算门禁 | `fidelityGate.ts`、`riskGate/`（压缩前 mask secret）、`hardBudget.ts`（#17 后处理） |
| 膨胀守卫（诚实统计） | `strategySelector.ts:804`（`applyStackedInflationGuard`）、`:835`（commitStepResult） |
| 缓存感知（不动可缓存前缀） | `cachingAware.ts`、`types.ts:172`（`preserveSystemPromptMode`） |
| 结果 memo（需显式 principalId） | `resultMemo.ts`、`strategySelector.ts:294`（gate） |
| 摘要器是规则（非 LLM），LLM-ready 接口未接 | `summarizer.ts:197`（`RuleBasedSummarizer`） |
| 唯一 LLM：跨账户 handoff | `open-sse/services/contextHandoff.ts:369`（`generateHandoffAsync`，`max_tokens:800`、`temp:0.1`）、`:436`（`maybeGenerateHandoff`，阈值 0.85–0.95） |
| 可预览 REST | `src/app/api/compression/preview/route.ts`（返回 original/compressed/tokensSaved/savingsPct/engineBreakdown/diff/validation） |
| 配置面巨大（~25 嵌套对象） | `types.ts:174`（`CompressionConfig`） |

### 2.3 多轮 / 上下文管理 `SOURCE-VERIFIED`

| 事实 | 证据 |
|---|---|
| 上下文窗口自校正（discovery vs catalog） | `src/lib/contextWindowResolver.ts:47`（`reconcileContextWindows`）、`:125`（24h 定时） |
| 3 层预适配裁剪 | `open-sse/services/contextManager.ts:401`（`compressContext`）、`:496`（trimToolMessages maxChars=2000）、`:148`（pruneOlderInlineImages，keep=2）、`:526`（compressThinking）、`:555`（purifyHistory 二分搜索） |
| **多格式 orphan 工具对修复** | `contextManager.ts:608`（`fixToolPairs`，跨 OpenAI tool_calls/role:tool 与 Claude tool_use/tool_result）、多次重跑 |
| 多格式内联图片识别（4 种 shape） | `contextManager.ts:121`（`isInlineBase64ImageBlock`）、`:130`（`replaceImageBlockWithPlaceholder`） |
| 会话指纹（sticky）进程内 Map | `open-sse/services/sessionManager.ts:103`（`generateSessionId` SHA256）、`:46`（Map，MAX=200，TTL 15min）—— **多实例不共享** |

### 2.4 缓存 / 转发 `SOURCE-VERIFIED`

| 事实 | 证据 |
|---|---|
| 通用 LRU | `src/lib/cacheLayer.ts:27`（`class LRUCache`，count+byte+ttl）、`:54`（SHA256 key） |
| 两级语义缓存（mem LRU + SQLite），仅 temp===0 | `src/lib/semanticCache.ts:119`（signature）、`:176`（getCachedResponse）、`:357/371`（cacheable gate）、apiKeyId 明文前缀隔离（`:140`） |
| 前缀分析（识别可缓存前缀，token=length/4） | `src/lib/promptCache/prefixAnalyzer.ts:25,75,22` |
| 转发 = 账号 fallback + combo，非单体 relay | `open-sse/services/accountFallback.ts`、`combo.ts`、`provider.ts`、`proxyAutoSelector.ts` |

### 2.5 数据结构变换 `SOURCE-VERIFIED`

| 事实 | 证据 |
|---|---|
| translator 注册表（from:to） | `open-sse/translator/registry.ts:20`（register）、`bootstrap.ts` |
| 角色归一化（GLM 版本感知） | `open-sse/services/roleNormalizer.ts:269`（`normalizeRoles`）、`:65`（`isGlmWithoutSystemRole`，5.1+ 保留 system）、`:40`（developer 保留 allowlist） |
| Responses 输入净化（input_image/output_text 角色不对称） | `open-sse/services/responsesInputSanitizer.ts:152`、`:42/64/83/99` |
| 声明式系统变换 DSL（TransformOp） | `open-sse/services/ccBridgeTransforms.ts:33`（判别联合：drop_paragraph/replace_text/inject_billing_header/...）、`:87-108`（CCH algo）；`systemTransforms.ts:54`（`obfuscate_words`）、`:69`（per-provider registry） |

---

## 3. 需要复核 / 待补证据（`NEW-DESIGN` / 待核）

| 条目 | 状态 | 复核动作 |
|---|---|---|
| `session_intent_evolution` 是否由 intentanalysis hook 实际写入 | 待核 | grep 写入点 + 看是否有 cron |
| `intentconfig` 规则是否多语言（中文） | 待核 | 读 `types.go:222-244` KeywordsConfig/PatternsConfig 全量 |
| 语义缓存 `cache/semantic/` 是否已上线 / 命中策略 | 待核 | 读实现 + 看是否在 handler 接线 |
| omniroute `resultMemo` / `fidelityGate` 的实际阈值 | `SOURCE-VERIFIED` 类型层 | 实现时按本仓库语义重定阈值 |
| 最大迁移号 | 待核 | `ls sql/migrations/startup/ | sort -n | tail`，新增前重检 |

## 4. 已确认的矛盾 / 易错点（评审重点关注）

1. **`shouldUseV2` 文档与实现不符**：注释说“由 feature flag 控制”，但第 691 行硬编码 `return false`，flag 检查被注释。任何“V2 已上线”的说法都是错的。`SOURCE-VERIFIED`。
2. **`SetSessionMetadata` 零生产调用方**：V2 `sessions.task_type/topic/intent` 列在生产中恒空。`SOURCE-VERIFIED`。
3. **`Tags` 是自由字符串**：不是结构化 tag；客户端可设任意值，无归纳。`SOURCE-VERIFIED`。
4. **两套压缩包并存**：`_to-be-deprecated/compressor/` 是 `domains/hooks/compression/` 的旧副本，带 `filterMessages` pre-fix bug。误接会回退修复。`SOURCE-VERIFIED`。
5. **两套消息指纹**：`diff.go` SHA256(512B) vs `session_writer_v2.go:506-513` role+首100字符。V2 切读后需统一，否则去重不一致。`SOURCE-VERIFIED`。
6. **token 估算常数不一**：压缩 `chars/3.5`、V2 builder `chars/4`、omniroute tiktoken + Anthropic PNG 数学。混用会误触发窗口。`SOURCE-VERIFIED`。

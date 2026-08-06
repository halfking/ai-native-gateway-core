# 01 — 会话元数据：总结 / 标题 / tag / 项目 / 任务归纳

> 主题：会话级的“管理功能”——总结（summary）、标题（title）、tag、项目归纳（project）、任务归纳（task）。对比双方如何**生成、存储、检索、消费**这些元数据。

## 1. 现状对比

> ⚠️ **本节经实现前复核修正（2026-08-06）**：初版误判 tag/summary/title"无归纳/未接线"。复核确认本仓库**已有完整的准实时分析管线** `AnalysisHook`（PostResponse，异步，`domains/hooks/sessionanalysis/hook.go`），在 `main_pipeline.go:542` 注册、`:1272-1279` 装配 `Engines{RequestSummarizer, Tagger}`，每会话触发 `SummarizeSession` + `TagSession` + 首请求 `GenerateTitle`。详见 §1.1。

| 维度 | llm-gateway-go（本仓库） | omniroute |
|---|---|---|
| **会话总结** | LLM `Summarizer`（`{title,summary,key_topics,user_intent}`，增量 rolling，`sessionsummary/summarizer.go:96-329`，落 `session_summaries` 迁移 310）**+** 准实时 `RequestSummarizer`（每会话，`AnalysisHook` 内，`sessionanalysis/hook.go:99`） | 无会话级总结；记忆域仅**首 3 句启发式**（`memory/summarization.ts:111`），非 LLM |
| **标题** | LLM `GenerateTitle`（maxTokens 30，首消息兜底）；**`AnalysisHook` 在首请求触发**（`hook.go:116-126`，受 `TitleOnFirstRequest` 配置控制）；会话关闭时 worker 精炼。**但 `main_pipeline.go:1272` 的 `Engines.TitleGenerator` 未注入（nil）→ 管线内首请求标题当前实际跳过** `SOURCE-VERIFIED` | 无标题生成 |
| **tag（结构化）** | **已有结构化自动打标** `SessionTagger`：从 `session_summaries` 派生多维标签（task/client/llm/topic/intent/provider/quality），落 `session_tags`（`tag_source='auto'`，迁移 351），幂等；**已接线**（`main_pipeline.go:1275` → `hook.go:115` `TagSession`）。另有 `SessionStateProjector` 把 v6 审计态（security/compliance/pii/approval）投影到同一表（`approval_integration.go:137`）。架构见 `docs/2026-07-09-session-tagging-redaction-architecture.md` | 无 tag 概念（记忆有 `type: factual/episodic/...`，非会话 tag） |
| **tag（live 字段）** | `Session.Tags`（Redis hash）是**自由逗号字符串**（`session.go:68`），客户端可设，与结构化 `session_tags` 表**两套并行** | N/A |
| **项目归纳** | `SessionClusterer`（coarse intent\|model\|topic + 向量聚类，落 `session_clusters`/`session_cluster_members`/`session_embeddings`，`analysis/clusterer.go`）+ `MultiSessionCluster`/`TopicSummarizer`（`domain/analysis/summary.go`、`topic.go`）；`ClusterRunner` 在 `main_pipeline.go:1280` 装配，admin/cron 触发（`admin/session_clusters_handler.go:212`） | **无** |
| **任务归纳** | 规则意图分类 `EnhancedClassifier`（7 类）+ KL 散度漂移（`intentconfig/classifier.go:20`、`drift.go`），**逐轮写 `session_intent_evolution`**（迁移 359，`intentconfig/store.go:60`） `SOURCE-VERIFIED` | task-aware router 只分类**路由 intent**（`taskAwareRouter.ts:311`），非会话任务归纳 |
| **V2 元数据写回** | `SetSessionMetadata`（写 `sessions.task_type/topic/intent`，`session_aggregator.go:298`）**零生产调用方** `SOURCE-VERIFIED` | N/A |
| **消费方** | 摘要/tag 供 admin/分析；intent 供路由模型推荐；聚类供项目视图 | 记忆供注入 |

### 1.1 准实时分析管线（关键修正）

本仓库**已有**一条 PostResponse 异步管线（`domains/hooks/sessionanalysis/hook.go`），由 `AnalysisHook.Execute` 起 goroutine `runAnalysis`（`hook.go:99-130`），依次：
1. `RequestSummarizer.SummarizeSession`（逐步摘要，规则模式，快速）
2. `SessionTagger.TagSession`（派生多维标签 → `session_tags`）
3. 首请求 `TitleGenerator.GenerateTitle`（受 `TitleOnFirstRequest` 控制）

装配：`cmd/gateway/main_pipeline.go:542`（注册 hook）、`:1272-1279`（构造 `Engines{RequestSummarizer, Tagger}`）。`ClusterRunner` 在 `:1280` 单独装配供 admin 手动/cron 触发。

**因此 omni-ref3 初版"tag/summary 无归纳、未接线"的判断作废。** 真实缺口收窄为：① `Engines.TitleGenerator` 在管线中未注入（首请求标题实际跳过）；② 结构化 `session_tags` 表与 live `Session.Tags`（Redis hash 字符串）两套并存、未互通；③ 归纳产出未写回 V2 `sessions.task_type/topic/intent`（`SetSessionMetadata` 零调用）；④ 元数据三存储（Redis hash / `session_summaries` / V2 `sessions`）无单一事实源。

### 关键洞察（修正后）

本仓库的会话管理**已是一条完整的准实时归纳管线**，结构化 tag/summary/cluster/intent 均已落库。剩余是**收敛与互通**问题，不是"从无到有"：
1. **标题断线**：`TitleGenerator` 接口存在但管线注入为 nil（小修即可）。
2. **双 tag 体系不互通**：结构化 `session_tags`（OLAP）vs 自由 `Session.Tags`（live Redis）。
3. **V2 列空写**：归纳已产出，但未流向 `sessions.task_type/topic/intent`。
4. **三存储无单一事实源**（标题/意图在 Redis hash、`session_summaries`、V2 `sessions` 三处）。

## 2. omniroute 的优点（可吸收）

1. **cache-safe 注入**（`memory/injection.ts:105,158`）：把记忆插在**最后一条 user 之前**而非开头，保住 provider prompt-cache 的字节稳定前缀。本仓库的摘要 marker `[smm_v1:]` 注入在第一条 assistant（`session_compressor.go:536-584`），未考虑 cache 前缀——**可吸收**。
2. **类型化衰减 + 访问免疫**（`memory/typedDecay.ts:40`）：只有 EPISODIC 衰减，被注入 ≥3 次的记忆获免疫。本仓库摘要无衰减/淘汰机制，`session_summaries` 永久增长——**可吸收**（用于旧摘要归档）。
3. **token 在 SQL 内聚合**（`store.ts:549` `SUM((LENGTH(content)+3)/4)`）：不把全部内容拉进内存。本仓库 `summarystore` 读消息时全量 SELECT——大规模下可优化。
4. **检索预览（dry-run）**（`retrievePreview`）：不 bump access count，供 Playground 调试。本仓库摘要无预览入口。

## 3. 本仓库的优点（保留）

1. **LLM 真摘要 + 增量 rolling**：omniroute 全程规则。`GenerateRollingSummary`（`summarizer.go:286-329`）读“上次摘要 + 新消息”做融合，质量上限更高。
2. **向量聚类做项目归纳**：`SessionClusterer` 是 omniroute 完全没有的能力。
3. **逐轮意图演化 + 漂移检测**：`session_intent_evolution` + KL 散度，可用于“意图切换时触发再总结/换模型”。

## 4. 缺口 / 债务

| 编号 | 债务 | 证据 | 影响 |
|---|---|---|---|
| M1 | **归纳产出未写回 V2 `sessions` 行**（`SetSessionMetadata` 零生产调用） | `SOURCE-VERIFIED` | V2 `task_type/topic/intent` 列恒空；V2 视图缺数据 |
| M2 | **三套标题/意图存储无单一事实源** | Redis hash vs `session_summaries` vs `sessions` V2 | 读路径要三处 join；标题可能不一致 |
| M3 | **双 tag 体系不互通**：结构化 `session_tags`（自动）vs live `Session.Tags`（自由字符串） | `tagger.go` vs `session.go:68` | live 端看不到自动标签；两套并行维护 |
| M4 | **摘要 marker 注入不 cache-safe** | `session_compressor.go:536-584` 注入第一条 assistant | 破坏 provider prompt-cache 前缀，降命中率 |
| M5 | **摘要无衰减/归档** | `session_summaries` 无 TTL/归档 | 表无限增长 |
| M6 | **`_to-be-deprecated/compressor/` 旧副本仍在**（含摘要旧路径） | 见 02 | 误接风险 |
| M7 | **`Engines.TitleGenerator` 管线注入为 nil** | `main_pipeline.go:1272-1279` 未设 `TitleGenerator`；接口在 `hook.go:36` | 首请求标题实际跳过（接口已就绪，仅需接线） |

## 5. 优化建议（带优先级）

> **实现前复核的关键调整**：初版 M1 主张"新建 `domains/sessionmeta/` 聚合器"。复核发现 `AnalysisHook`（`sessionanalysis/hook.go`）**已是**这条异步归纳管线，且 `SessionTagger` 已落 `session_tags`。**新建聚合器会重复造轮子**。故 M1 改为**扩展现有 `AnalysisHook`/`SessionTagger`**，仅在末步加一次 `SetSessionMetadata` 写回。

### M1（P1）扩展 AnalysisHook 写回 V2 sessions 行 `NEW-DESIGN`（改：不再新建聚合器）
在现有 `AnalysisHook.runAnalysis`（`hook.go:99`）末尾，把已产出的 `session_summaries.user_intent/key_topics` 经 `SessionAggregator.SetSessionMetadata`（`session_aggregator.go:298`）写回 V2 `sessions.task_type/topic/intent`。
- **不新建包**：复用 `Engines` 结构（加一个 `MetadataWriter` 依赖）或直接注入 `*SessionAggregator`。
- **节流**：不每轮触发；按"每 N 轮 / intent 漂移（`session_intent_evolution.intent_drift_score` 越阈）/ 摘要刷新"触发。
- **前置**：D8 命名空间调查（确认 `domain/analysis` vs `domains/analysis` 活跃包）；`AnalysisHook` 需能拿到 `SessionAggregator`（main_pipeline 装配处注入）。
- 门禁：失败 fail-open（已有 `OnError` 吞错）；按租户灰度。
- 验收：多轮会话后 `sessions.task_type/topic/intent` 非空且与 `session_summaries` 一致；写放大在节流预算内。

### M2（P1）统一元数据事实源 `NEW-DESIGN`
确立 **V2 `gateway.sessions` 为单一事实源**；Redis hash 的 `Title/Tags` 降级为缓存（读时回填）。`session_summaries` 保留为"摘要版本历史"。
- 兼容期：dual-read（V2 优先，miss 回退 Redis）；写路径双写。
- 退役条件：V2 读全量放开（见 03 的 V2 灰度）。

### M3（P1）桥接双 tag 体系 `NEW-DESIGN`（改：tag 归纳已存在，改为互通）
`session_tags`（结构化、自动）已存在且已打标。缺口是它与 live `Session.Tags`（自由字符串）**不互通**。建议：
- 读侧：live `Session.Tags` 读取时，合并 `session_tags` 中 `tag_source='auto'` 的值（去重）。
- 写侧：保留客户端 `X-Gw-Tags` 透传为 `user_tags`；自动标签只走 `session_tags` 表，不污染 `Session.Tags`。
- 检索：admin 按 tag 检索走 `session_tags`（已有 GIN 索引评估），不走 Redis 字符串。
- **不**把 `Tags string` 改 `[]string`（破坏面大）；用视图/读取层合并。

### M4（P1）cache-safe 摘要注入 `NEW-DESIGN`（吸收 omniroute）
把 `[smm_v1:]` marker 改为注入**最后一条 user 之前**（当存在 provider prompt-cache 时），或在 IR 层用 `cache_control` 断点表达。需结合 04 的 prompt-cache 前缀分析。
- 风险：改变 marker 位置会影响 `isSummaryMarkerMsg` 的免索引逻辑（`diff.go:292-316`），需同步调整；客户端回放兼容需验证（审计 §3）。

### M5（P2）摘要衰减/归档 `NEW-DESIGN`（吸收 omniroute typedDecay）
对 `session_summaries` 增加 `archived_at`；超过 N 天未被访问且会话已结束的摘要归档。

### M6（P0）删除 `_to-be-deprecated/compressor/` `TARGET-BOUNDARY`
**已确认无外部引用**（`grep` + `go build ./...` 均无）。删除即可。与 M1 解耦，先行。

### M7（P0→P1）接线 Engines.TitleGenerator `NEW-DESIGN`（改：这是真实小修）
`main_pipeline.go:1272-1279` 构造 `Engines` 时**未设 `TitleGenerator`**，导致 `AnalysisHook` 首请求标题实际跳过。接口已在 `hook.go:36` 就绪（`GenerateTitle(ctx,tenant,session,firstMsg)`），由 `sessionsummary.Summarizer.GenerateTitle` 实现。
- 建议：在 `main_pipeline.go` 构造 `Engines` 处注入一个适配 `sessionsummary.Summarizer` 的 `TitleGenerator`（或直接传 `*Summarizer` 若其已实现该接口）。
- 受 `cfg.TitleOnFirstRequest()` 控制，默认行为可保持原状（按租户开）。
- 验收：开启配置后，首请求会话的 `session_summaries.title` 非空。
- 低风险、高收益，可独立先行。

## 6. 不做（明确非目标）

- **不引入 omniroute 的 keyed 记忆 CRUD**（`memories` 表 + FTS5/向量）：本仓库的“记忆”语义由会话摘要 + 向量聚类覆盖，避免第二套记忆系统。
- **不做 omniroute 的正则 fact 抽取**：质量低于现有 LLM 摘要，且英文偏向。
- **不做记忆的三存储镜像同步**（SQLite + sqlite-vec + Qdrant）：本仓库用 PG + 单一向量列（`session_embeddings.embedding_v2`），复杂度更低。

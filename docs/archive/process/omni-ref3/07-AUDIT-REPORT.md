# 07 — 方案审计报告（自审 + 修正）

> 本文件是对 01–06 的自审计：逐条核对证据、暴露矛盾与过于宽松的假设、修正不准确表述、列出遗留风险。审计基于 2026-08-06 对两个 checkout 的源码复核。

## 1. 审计结论

方案整体成立，但经**两轮**审计修正。第一轮（§2，6 处）修正 intent/cluster 接线状态；**第二轮（实现前复核，§2B，3 处）发现更重大的遗漏：`SessionTagger` 结构化打标管线已存在且已接线**，故 01 的"tag 无归纳"判断作废，M1/M3/M7 重写。最终核心判断（**扩展现有管线 > 新建；吸收门禁/熔断/DSL，不吸收引擎/对抗变换**）不变并强化。

## 2. 审计修正（已在 01–06 隐含，此处集中披露）

### 修正 1：`session_intent_evolution` **确有写入**（原 00 标“待核”→ 现确认）
- 原文（00 §3）标“待核”。
- 复核：`domains/intentconfig/store.go:60` 确实 `INSERT INTO session_intent_evolution`，`:126/:182` 读取。
- **结论**：逐轮意图轨迹**已持久化**。因此 01-M1 的“归纳未落库”应更精确为：**intent 已落 `session_intent_evolution`，但未落 live `sessions.task_type`**（`SetSessionMetadata` 零调用依然成立）。M1 的本质是“把已产出的 intent/topic 汇聚写回 sessions 行”，而非“新建 intent 能力”。

### 修正 2：`SessionClusterer` **已在 main 装配 + admin 触发**（原 01 表述偏“离线”）
- 复核：`cmd/gateway/main_pipeline.go:1280` 经 `admin.SetClusterRunner(sessionanalytics.NewSessionClusterer(...))` 装配；`admin/session_clusters_handler.go:212` 提供触发入口。
- **结论**：聚类是**已上线的 admin/cron 能力**，不是“待建”。01-M1 的措辞修正为“复用已装配的 `SessionClusterer`，将其产出（cluster_id/label/topic_path）回填到 live session 的 project/tag 维度”。M1 工作量下调。

### 修正 3：归纳层比方案描述更丰富（topic 建模 + 多会话聚类）
- 复核发现：`domain/analysis/topic.go`（`Topic`/`TopicNode`/`TopicAssignment`）+ `summary.go`（`MultiSessionCluster`）。
- **结论**：01 应补充“项目归纳 = SessionClusterer（单租户聚类）+ MultiSessionCluster（跨会话）+ TopicSummarizer（话题树）”的完整图景。优化建议不变（接线写回），但 M3（结构化 tag）可直接复用 `TopicNode` 作 tag 来源。

### 修正 4：`cache/semantic/` 是**旧/未接线**，新缓存是 `domains/hooks/cache/`
- 复核：`domains/hooks/cache/types.go:8` 注释明确“本包是新抽象，**不依赖旧 cache/semantic、cache/prefix、sessions/session_cache.go**”。
- **结论**：04-D6 应修正为：**`cache/semantic/` 是历史遗留（未接新 hook pipeline）；线上语义缓存是 `domains/hooks/cache/`（exact-hash，非语义相似）**。因此“吸收 omniroute 严格 temp===0 门禁”的对象是 `domains/hooks/cache/hook.go`，而非 `cache/semantic`。D6 增加一条“评估旧 `cache/semantic|prefix|delta|kv` 是否可随 V1 一起退役”。

### 修正 5：存在 `domain/analysis`（无 s）与 `domains/analysis`（有 s）**双包**
- 复核：`domain/analysis/topic.go`、`summary.go`（旧 `domain/` 命名空间）vs `domains/analysis/clusterer.go`（新 `domains/`）。
- **结论**：这是命名空间迁移的遗留（`domain/` → `domains/`，与 B1 routing CQRS 重构相关）。**新增债务条目 D8**：归纳代码横跨两个命名空间，落地 M1 前应先确认哪个是活跃的、合并或明确弃用关系，避免在死包上接线。

### 修正 6：最大迁移号是 **466**（非占位）
- 复核：`ls sql/migrations/startup/ | sort -n | tail` → 466。
- **结论**：06 的”新增迁移前重检”正确；本方案任何新迁移应从 467 起，且落地前再次 `sort -n | tail` 复核。

## 2B. 第二轮修正（实现前复核，2026-08-06）— 更重大遗漏

进入实现时复核发现第一轮审计**仍低估了现有能力**。以下修正直接改写了 01 的现状表与 M1/M3/M7。

### 修正 7：`SessionTagger` 结构化打标**已存在且已接线**（01 初版”tag 无归纳”作废）
- 复核：`domains/analysis/tagger.go` 的 `SessionTagger` 从 `session_summaries` 派生多维标签（task/client/llm/topic/intent/provider/quality），幂等写入 `session_tags`（`tag_source='auto'`，迁移 351）。**已接线**：`cmd/gateway/main_pipeline.go:1275` 构造 → `domains/hooks/sessionanalysis/hook.go:115` 每`TagSession` 调用。另有 `SessionStateProjector`（`approval_integration.go:137`）把 v6 审计态投影到同表。架构文档 `docs/2026-07-09-session-tagging-redaction-architecture.md`。
- **结论**：01 §1 的 tag 行从”自由字符串、无归纳”改为”**结构化自动打标已存在** + live `Session.Tags` 自由字符串两套并行”。M3 从”新建结构化 tag”改为”**桥接双 tag 体系**”。

### 修正 8：`AnalysisHook` 已是异步归纳管线（M1 不应新建聚合器）
- 复核：`domains/hooks/sessionanalysis/hook.go` 的 `AnalysisHook`（PostResponse，priority 250，异步 goroutine）已在 `main_pipeline.go:542` 注册，`runAnalysis`（`:99`）依次跑 `RequestSummarizer.SummarizeSession` → `SessionTagger.TagSession` → 首请求 `TitleGenerator.GenerateTitle`。
- **结论**：01 初版 M1”新建 `domains/sessionmeta/` 聚合器”会**重复造轮子**。M1 改为”**扩展现有 `AnalysisHook`**，在 `runAnalysis` 末尾加一次 `SetSessionMetadata` 写回 V2 sessions 行”。

### 修正 9：`Engines.TitleGenerator` 管线注入为 nil（M7 改为真实小修）
- 复核：`main_pipeline.go:1272-1279` 构造 `Engines{RequestSummarizer, Tagger}`，**未设 `TitleGenerator`**（接口在 `hook.go:36` 就绪）。故首请求标题实际跳过。
- **结论**：M7 从”首轮即时 task_type（hot-path 同步写）”改为”**接线 `TitleGenerator`**”——低风险小修，可独立先行。原 M7 的”hot-path 同步写 task_type”成本过高，降级/搁置。

### D8 调查结论（已落地，2026-08-06）`SOURCE-VERIFIED`
P0 调查项 D8（命名空间 / 旧缓存）已复核：

**(a) `domain/analysis`（无 s）vs `domains/analysis`（有 s）—— 两者都活跃，非重复弃用。**
- `domain/analysis`（12 个非测试外部引用）：放**共享领域模型/事件类型**（`events.go`/`failure.go`/`intent.go`/`optimization.go`/`prompt.go`/`summary.go`/`topic.go`）。被 `cmd/gateway/main_pipeline.go`、`domains/analysis/{bus,workers}/*`、`domains/assets`、`domains/clientprofile`、`domains/integration`、`domains/intentconfig/types.go` 引用。
- `domains/analysis`（7 个非测试外部引用）：放**具体 runner/投影器**（`clusterer.go`/`tagger.go`/`request_summary.go`/`state_projector.go`/`optimizer.go`/`stage_config.go`/`db.go`/`openai_client.go`）。被 `cmd/gateway/{main,main_pipeline,approval_integration,embedding_adapter}.go`、`domains/hooks/{sessionanalysis,sessionaudit}` 引用。
- **结论**：这是 **类型层（`domain/`）与运行层（`domains/`）的有意拆分**，不是死包。M1 接线时：归纳类型/事件用 `domain/analysis`，runner/写库用 `domains/analysis`。**无需合并**，但应在 M1 设计中注明两者职责边界（避免新人误删其一）。`domains/analysis/workers/*` 反向依赖 `domain/analysis`，符合该分层。

**(b) 旧缓存包 `cache/*` —— 仅 `cache/prefix` 仍 live；`semantic`/`delta`/`kv` 已孤立。**
| 包 | 非测试外部引用者 | 状态 |
|---|---|---|
| `cache/prefix` | `domains/streaming/handler.go` | **live**，保留 |
| `cache/kv` | 仅 `cache/delta/store.go` | 经 delta 间接，delta 本身无引用 → 实质孤立 |
| `cache/delta` | **0** | **孤立，可删** |
| `cache/semantic` | **0** | **孤立，可删** |
- 链路：`handler.go → cache/prefix`（独立活）；`cache/delta → cache/kv`（两者仅彼此引用，无外部入口）。
- **结论**：`cache/semantic`、`cache/delta`、`cache/kv` 三者构成**孤立簇**（kv 仅被 delta 用，delta 无人用）。可在 P2 与 V1 退役一起删（先二次确认无测试/反射引用）。`cache/prefix` 保留。这与 `domains/hooks/cache/types.go:8`”新 hook 不依赖旧 cache/*”的注释一致——新缓存层已是 `domains/hooks/cache/`。

**对路线图的影响**：04-D8 与 06-P2 的”旧缓存包退役”范围精确化为 `cache/{semantic,delta,kv}`（保留 `cache/prefix`）；M1 的命名空间前置调查**结论为”无需合并，仅注明分层”**，不再阻塞 M1。

## 3. 需降级 / 重新评估的建议

| 条目 | 问题 | 处置 |
|---|---|---|
| 01-M2（统一元数据事实源：V2 为准，Redis 降缓存） | 依赖 V2 读全量放开（A1）+ Redis hash 当前是 live 路由/凭据状态的载体（不止 title/tags）。把 Redis 降为“缓存”会影响 `LastCredentialID`/`Status` 等热字段语义 | **降级**：M2 拆为两步——(a) 仅 title/tags 走 V2 为准（窄）；(b) 整个 Redis hash 角色重定义需独立 RFC，移出本方案 |
| 02-C8 / 01-M4（cache-safe marker 注入到末条 user 前） | marker 当前是首条 assistant 文本前缀（`[smm_v1:]`），改动注入位置会冲击 `isSummaryMarkerMsg` 的免索引逻辑与客户端回放兼容 | **重新评估**：需先验证客户端（Cursor 等）能否容忍 marker 位置变化；可能需保留首条注入但**额外**用 IR `cache_control` 断点表达前缀稳定，而非移动 marker |
| 03-A1（V2 读灰度）的验收“与 V1 持平 ±5%” | V2 delta 重建与 V1 LCS 在边缘（attachment-only、orphan tool）本就有 submit-mode 探测差异，5% 阈值可能过严 | **放宽 + 细化**：改为“outbound hash 一致率 ≥99%（正常会话）+ 边缘用例人工审计通过” |

## 4. 遗留风险（落地前必须再核）

1. **V2 `Message` 强类型化（A5/E6）的存量兼容**：`session_bodies` JSONB 已是弱类型格式（`Content string`、`ToolCalls []map[string]any`）。强类型化需“读时兼容旧格式”+ 迁移期双读，**不可一次性切**。风险高，建议 P1 末或 P2 做，且必须有兼容读层。
2. **secret mask（C2）的误伤**：正则 mask 可能误伤合法内容（如含 `sk-` 前缀的非密钥文本、代码里的假 key）。必须有 allowlist + 正负例回归 + flag 可关。
3. **`SetSessionMetadata` 接线后的写放大**：M1 聚合器若每轮触发，会给 V2 aggregator 增加写压力。必须**节流**（如每 N 轮或 intent 漂移时才写），否则 hot-path 成本超预期。
4. **prompt-cache 前缀分析器（D7）与 provider 实际缓存行为的偏差**：omniroute 的前缀分类是启发式（system_only/.../system_tools_history），不一定匹配 OpenAI/Anthropic 实际缓存断点语义。需用真实 cache hit/miss 校准，否则“缓存感知压缩”反而降命中。

## 5. 方案内部一致性检查

| 检查 | 结果 |
|---|---|
| 依赖链无环 | ✅ P0 → P1(A1) → P2(M2) → P3 线性；A2/A6 → A1；C4 → A3/D7；D7 → C8 |
| 无“同时新增 + 删除同一物” | ✅ 删 `_to-be-deprecated/compressor`（C5）与新建无冲突 |
| 不变量未被建议破坏 | ✅ fail-open/NeverWorse/toolChainIntact/RLS 均在 06 清单显式保留 |
| feature flag 默认关 | ✅ 所有 NEW-DESIGN 标注按租户灰度 |
| 不引入非目标项 | ✅ 明确拒绝 omniroute 引擎全量、对抗变换、keyed 记忆、进程内 sticky |

## 6. 被明确拒绝的 omniroute 特性（备查）

| 特性 | 拒绝原因 |
|---|---|
| ~12 压缩引擎全量 / stacked 多引擎串行 | 配置面爆炸（~25 嵌套）；早期有损引擎污染后续；本仓库单管线+LLM 摘要已覆盖 |
| LLMLingua SLM（ONNX） | 额外部署、收益不明 |
| keyed 记忆 CRUD + FTS5/sqlite-vec/Qdrant 三镜像 | 第二套记忆系统；本仓库摘要+聚类已覆盖；同步复杂 |
| 正则 fact 抽取（英文偏向） | 质量低于 LLM 摘要 |
| 进程内 sticky Map | 多实例失效；本仓库 Redis+PG 更优 |
| combo/accountFallback 转发 | 本仓库数据面已是 owner |
| CC-bridge 对抗变换（身份注入/计费头伪装/ZWJ 混淆） | 合规与稳定性风险 |
| translator O(N²) 注册表 | 本仓库超集 hub-and-spoke 更优 |

## 7. 被吸收的 omniroute 特性（备查）

| 特性 | 吸收到 | 落点 |
|---|---|---|
| 引擎/摘要熔断 + half-open 探测 | C1 | 02 |
| 保真度门禁 + 风险门禁（secret mask） | C2 + fidelityGate | 02 |
| 膨胀守卫（诚实统计） | 与 NeverWorse 合并 | 02 |
| 结果 memo（gated on principal） | C3 | 02 |
| 缓存感知压缩 + preserveSystemPromptMode | C8 | 02 |
| 压缩预览 REST | C7 | 02 |
| Anthropic PNG 图片 token 数学 | C4 | 02/统一 token |
| 上下文窗口自校正 | A4 | 03 |
| 旧内联图片占位化 + 多格式识别 | A3 | 03 |
| cache-safe 注入（末条 user 前） | M4 | 01（需重评，见 §3） |
| 类型化衰减 + 访问免疫 | M5 | 01 |
| token SQL 内聚合 | M5/M2 | 01 |
| 严格 temp===0 缓存门禁 + apiKeyId 明文前缀 | D6 | 04 |
| prompt-cache 前缀分析器 | D7 | 04 |
| 通用 LRU byte+count+ttl | D3 | 04 |
| 统一 cache_metrics 表 | D2 | 04 |
| 声明式 provider 变换 DSL（TransformOp） | E1 | 05（仅结构 op，非对抗） |
| GLM 版本感知角色归一化 | E4 | 05 |
| Responses 输入净化细节（input_image/output_text/item-id/函数名） | E5 | 05 |

## 8. 建议的下一步（评审通过后）

1. 先做 P0 五项（独立、低风险、可并行），其中 C2（secret mask）需先出正负例集评审。
2. P1.1（V2 读灰度）单独立 RFC，含 outbound hash 一致性对比方案与回滚。
3. 01-M1（元数据聚合器）出详细设计：复用现有 `SessionClusterer` + `intentconfig` + `Summarizer`，明确节流策略与写回字段映射。
4. D8（domain/`domains` 命名空间）先于 M1 调查清楚，避免在死包接线。

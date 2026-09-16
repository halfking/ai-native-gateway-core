# 特性需求:成本设置快捷化 + auto 多维会话评分 + 停滞换模型

> 状态:**RFC v2(已过审计,复用优先重写;待分阶段实施)**
> v1 → v2 变更:经 `AUDIT-auto-multidim-progress-reuse-analysis.md` 审计,
> 70% 的"新建组件"改为复用/重构现有实现;总代码量 ~700 行 → ~370 行
> 范围:原始诉求"成本设置" / "auto 正确可靠" / "经济但高效" / "停滞换模型" / "多维会话评分驱动 3 种动作"
> 核心参考实现:`domains/hooks/goal/`(已具备停滞检测 → 换模型的完整闭环)
> 关联缺陷:O5(已记录,本 RFC 不解 O5);2026-09-08 audit O2 `decision_trace` 已落地

---

## 1. 背景与问题

`model=auto` 已具备多维评分(2/4/5 维三版本共存,见 §6.1),
`admin/pricing.go` 已具备 9 个成本端点 + 1286 行 Vue UI,但仍有 3 类痛点:

1. **成本设置繁琐**:运营改一个 provider 全量模型成本需逐条编辑。
2. **auto 不够"可靠"**:缺会话级*进展状态*,无法区分"模型表现差"与"任务本身在推进"。
3. **auto 没有自我纠错**:连续多轮失败/重复/延迟爆炸时,auto 仍按既定评分走,
   没有"换模型 / 加上下文 / 建议停止"的回路反馈。

**审计带来的关键认知**:第 2、3 条痛点在 **goal mode 里已经解决过一遍**。
`domains/hooks/goal/loop_detector.go` 的 `detectLoop → pickNextModel → applyModelSwitch`
已是"停滞检测 → 选新模型 → 原子 CAS 换模型 → 重置预算"的完整闭环。
本 RFC 的主要工作因此从"设计新机制"转为**把 goal 已验证的机制抽象上提,让 auto 复用**。

## 2. 原始需求 → 规格条目对照

| 原始需求(摘录) | 规格条目 | 验收方式 |
|---|---|---|
| 增加方便快捷的供应商模型成本设置 | §3.1 成本预设(移植 `goal/cost_presets.go` 范式) | 3 个端点 + UI chip 栏 |
| 优化成本设置能力,让 auto 正确可靠 | §3.2 `decision_trace` 扩 4 字段 | 每次决策携带 4 维评分 |
| 既经济又确保效率 | §3.3 评分权重(**已具备**,本 RFC 只显式化) | 价格 0.2 + 通道质量 0.3 |
| 一段时间没进展,auto 自动更换模型 | §3.4 复用 `RecommendModelAlternatives` + goal 换模型范式 | `X-Gw-Auto-Switched-Model` 非空 |
| 多维状态评分决定停止/换模型/加上下文 | §3.5 4 维评分 + §3.6 三动作 | 4 维可见 + 动作可 SQL 验证 |

## 3. 规格条目(可验收)

### 3.1 成本设置快捷化(复用 `goal/cost_presets.go` 范式,不建新表)

**审计结论**:`goal/cost_presets.go`(206 行)已有成熟的"预设表 + `GetPreset(key)` +
`InferCostMode` 回退"三件套范式。移植该范式即可,**不需要 migration 715**。

- `GET /api/pricing/presets` —— 返回内置预设 map(6 套:OpenAI 官方 / Anthropic 官方 /
  国内大厂 / 第三方聚合 / 免费层 / 性能旗舰),Go map 直接 JSON 序列化
- `POST /api/pricing/presets/preview` —— **dry-run 必经**,返回将变更的 `offer_id` 与字段差异
- `POST /api/pricing/presets/apply` —— 批量写入,`pricing_source='preset'`
- 沿用 `admin/pricing.go` 现有 `writeEndpoints` map + `RequireSuperAdminForWrite` 门禁
- 沿用现有 `pricing_source` 枚举,**追加**第六值 `preset`(现有五值:manual/imported/copied/inherited/auto)

**不做**:不建 `pricing_presets` 表。预设是代码常量(与 `goal.ModePresets` 同构),
可 code review、可测试、可随版本演进;运营自定义预设留待后续迭代按需再上表。

### 3.2 decision_trace 扩展(复用现有字段载体)

`autoRouteDecision` 已有 `CandidatesTop3 / EnabledFeatures` 等字段,**追加** 4 个:

- `health_score int` —— 0-100 综合分
- `health_dims {progress, quality, cost_efficiency, task_fit}` —— 4 维明细
- `switched_from string` + `switched_reason string` —— 本轮是否换模型及原因
- `suggested_action string` —— `continue | enrich_context | switch_model | suggest_stop`

沿用现有 `maxWireDecisionBytes`(16 KiB)截断保护与 `X-Gw-Auto-Decision` 头通道。

### 3.3 评分权重(**已具备**,本 RFC 仅显式化为契约)

审计确认此项**无需新代码**,现状已满足"经济但高效":

- `ScoreWithChannelQuality`:intent 0.4 + **price 0.2(经济)** + **channel 0.3 + reliability 0.1(效率)**
- `ScoreWithAffinity`:上述 4 维 × 0.85 + affinity 0.15(生产默认)
- 池分层 `StratifyByChannelQuality`(≥50 preferred / <50 fallback)+ `ApplyFallbackDemotion`
  已实现"可靠资源优先,免费不可靠者除历史无错否则跳过"

本 RFC 只把它写入用户可见契约(§3.2 的 `health_dims` 让权重效果可观测),不改权重。

### 3.4 停滞换模型(复用两处现成实现)

**复用清单**:
- `autoroute/model_alternatives.go::RecommendModelAlternatives` —— 已实现"排除 `TriedModels`、
  过滤 `IQ < initialIQ`、按 `PreferredModels` 排序"的次优候选推荐
- `goal/loop_detector.go` 的换模型范式 —— `pickNextModel`(跳过当前模型轮转)+
  `applyModelSwitch`(经 `AtomicModelSwitch` 做 CAS,赢者才 `ModelSwitchCount++` 并重置预算)

**新增最小面**:
- 换模型冷却:同一会话 5 分钟内最多 1 次自动换模型(Redis key `auto:switch:cooldown:{sessionID}`),
  防换模型风暴 —— 对应 goal 侧的 `MaxModelSwitch` 预算上限,auto 用时间窗替代次数窗
- 换模型结果写回 `decision_trace`(§3.2 的 `switched_from / switched_reason`)

### 3.5 4 维评分(纯计算,复用 goal 的信号定义)

| 维度 | 数据源 | 公式 | 复用来源 |
|---|---|---|---|
| D1 进展度 | 近 5 轮 success + finish_reason + token 增量 | `success_rate*0.6 + finish_natural_ratio*0.3 + token_growth*0.1` | `RoutingOutcome.Success` |
| D2 质量度 | 近 5 轮 p95 + tool 错误率 + 输出重复度 | `latency_score*0.4 + tool_success*0.4 + novelty*0.2` | **`goal/loop_detector.go::hashResponse`**(重复检测) |
| D3 成本效率 | 累计 cost / 同类任务 P50 | `clamp(100*(1 - cost/p50), 0, 100)` | `RoutingOutcome.CostUSD` |
| D4 任务契合 | 任务切换次数 + 负反馈 | `100 - task_switch*10 - negative_feedback*30` | `SessionIntentCache.HitCount` 漂移检测 |
| 综合 | 加权和 | `0.35*D1 + 0.30*D2 + 0.20*D3 + 0.15*D4` | — |

**实现要点**:`autoroute/session_health.go` 为**纯函数**(无 I/O),便于单测钉桩;
数据由已有的 `ReportRoutingOutcome` 回路喂入(见 §4)。

### 3.6 评分 → 动作映射(抽象 goal 的 `loopDecision`)

goal 已有 `loopDecision{canContinue, switchModel, giveUp, reason}` 三态决策。
本 RFC 抽象为四态(多一个 `enrich_context`),两侧共享:

```
composite >= 80         → continue        (对应 goal canContinue)
70 <= composite < 80    → enrich_context  (auto 新增:注入更早 tool result 摘要)
50 <= composite < 70    → switch_model    (对应 goal switchModel)
composite < 50          → suggest_stop    (对应 goal giveUp)

硬升级(任一维独立触发,不看综合分):
  D1 < 30                      → switch_model
  D2 的 p95 > 10s 持续 2 轮     → switch_model
  D4 < 20                      → suggest_stop
  连续 3 轮 composite < 50      → suggest_stop
```

**`suggest_stop` 的语义边界(重要)**:auto 侧**只建议不强制**——
写 `X-Gw-Auto-Suggested-Action: suggest_stop` 头 + `decision_trace`,
**不**主动断流、**不**返回错误。是否停止由客户端/上层 agent 决定。
(与 goal 的 `giveUp` 不同:goal 是自己的循环,有权终止;auto 是代答通道,无权替客户端决定。)

## 4. 集成位置(复用优先)

| 改动 | 挂点 | 复用/新建 |
|---|---|---|
| 4 维评分公式 | 新文件 `autoroute/session_health.go`(纯函数) | 新建 ~80 行 |
| 评分数据喂入 | **扩展** `autoroute/outcome_feedback.go::RoutingOutcome` 加 `SessionID` 字段 | 复用现有回路 |
| 结果回流触发 | **已存在**:`ReportRoutingOutcome` 已在 `domains/streaming/handler.go:6174` 与 `request_log_pipeline.go:1123` 两个 choke point 被调用 | 零改动 |
| 重复度检测 | 复用 `goal/loop_detector.go::hashResponse` 上提为共享工具 | 重构 ~15 行 |
| Redis 存储 | 新增 autoroute 私有 `SessionHealth` + `HealthRedisStore` 接口,复用 `session_intent_cache.go` 的接口模式/并发范式/Redis 连接(**不改**共享 `ursmcache.Intent`,见审计 §1.2) | 新建 ~60 行 |
| 评分 → 动作 | 新文件 `autoroute/progress_decision.go`,抽象 goal 的 `loopDecision` | 新建 ~60 行 |
| 接入决策链 | `autoroute/decision_v2.go::DecideV2` 末尾(现有路径) | 扩展 ~40 行 |
| 换模型执行 | 复用 `RecommendModelAlternatives` | 零改动 |
| wire 字段 | `domains/streaming/auto_route.go` 的 `autoRouteDecision` 加 4 字段 | 扩展 ~20 行 |
| 成本预设 | 新文件 `admin/pricing_presets.go`,移植 `goal/cost_presets.go` 范式 | 新建 ~80 行 |
| 前端 | `PricingManagementView.vue` 顶部加 `<PricingPresetBar>` + 8 国 i18n | 新建 ~60 行 |

**总计**:~370 行(v1 初版估 ~700 行,**减少 47%**),且**零 migration**、**零新表**、**零新 Redis 连接**。

## 5. 实施阶段

| 阶段 | 范围 | 代码量 | 风险 | 依赖 | commit |
|---|---|---|---|---|---|
| 0 | 方案 + 审计文档 | — | 低 | — | ✅ 已完成 |
| 1 | 成本预设(移植 `cost_presets` 范式 + 3 端点 + UI + i18n) | ~140 行 | 低 | 无 | `feat(pricing): 成本预设一站式应用` |
| 2 | 4 维评分纯函数 + `RoutingOutcome` 加 `SessionID` | ~80 行 | 低 | 无 | `feat(autoroute): 会话 4 维健康分计算` |
| 3 | `SessionHealth` Redis 存储(复用 intent 缓存范式) | ~60 行 | 低 | 阶段 2 | `feat(autoroute): 健康分 Redis 存储` |
| 4 | 抽象 `progress_decision.go`(goal/auto 共享四态决策) | ~60 行 | 中 | 阶段 3 | `refactor(autoroute,goal): 停滞决策四态抽象共享` |
| 5 | 接入 `DecideV2` + wire 字段 + 换模型冷却 | ~60 行 | 中 | 阶段 4 | `feat(autoroute): 健康分驱动换模型/扩上下文/建议停止` |
| 6 | 可观测(Prometheus + SQL 视图) | ~50 行 | 低 | 阶段 5 | `feat(observability): 健康分与动作指标` |

**全程 feature flag 门禁**:`AUTO_USE_SESSION_HEALTH=false` 默认关,
沿用 `autoroute/feature_flags.go` 现有 5 开关机制(`LoadFeatureFlagsFromEnv` + `activeFeatureNames`)。
阶段 4 的 goal 侧重构需保证**行为完全不变**(现有 `loop_detector` 测试全绿为准入门槛)。

## 6. 现状盘点(避免重复造轮子)

### 6.1 auto 评分已具备
- `autoroute/scoring_simplified.go`:`ScoreSimplified`(2 维)/ `ScoreWithChannelQuality`(4 维)/ `ScoreWithAffinity`(5 维)
- `StratifyByChannelQuality` + `ApplyFallbackDemotion` 池分层
- `ComputeCorrectionScore` 校正分(同模型上次成功 +5 / 失败 -10)
- `ScoringBreakdown` 已有 11 个 0-100 维度字段

### 6.2 换模型闭环已具备(两套)
- **auto 侧**:`autoroute/model_alternatives.go::RecommendModelAlternatives`
- **goal 侧**:`goal/loop_detector.go` 的 `detectLoop`(210 行)→ `pickNextModel` → `applyModelSwitch`(`AtomicModelSwitch` CAS)
- `routing_decision_log.decision_trace` 已落地(2026-09-14 O2)

### 6.3 outcome 回路已具备(2026-09-08 audit Track A)
- `autoroute/outcome_feedback.go`:决策时 stash → 完成时匹配 → 写 feedback
- `RoutingOutcome{RequestID, Success, LatencyMs, CostUSD}`
- `domains/streaming/routing_outcome.go::routingOutcomeFromEntry` 投影
- `autoroute/outcome_metrics.go`:5 个计数器(stashed/matched/orphan/expired/dropped)已上 Prometheus

### 6.4 成本管理已具备
- `admin/pricing.go`(1045 行)9 端点:`tree/summary/bulk-update/export/import/table/stats-window/copy/auto-inherit` + `setFreeModels`
- `web/src/views/PricingManagementView.vue`(1286 行)+ 8 国 locale
- `goal/cost_presets.go`(206 行)成本分档范式

### 6.5 session 状态:已有"事后"评估(**不能驱动实时**,这是真缺口)
- `bg/session_health_worker.go` 写 `session_summaries.health_score/health_grade/quality_score/outcome`
- 维度:ErrorCount / AvgLatency / **ModelSwitchCount** / Compliance / PromptInjection / PII / Toxic
- 缺口:会话**关闭后**异步算,进不了 auto 实时决策回路
- **本 RFC 不动此模块**,只新增实时版;二者维度命名保持一致以便对账

## 7. 风险与缓解

| 风险 | 概率 | 缓解 |
|---|---|---|
| 评分误判误换模型 | 中 | feature flag 默认关;super_admin 灰度;换模型走 `IQ >= initialIQ` 门槛 |
| 换模型风暴 | 中 | 同会话 5 分钟 1 次冷却(Redis) |
| 阶段 4 重构回归 goal 行为 | **中高** | 现有 `loop_detector` 全部测试为准入门槛;抽象只提取不改语义;goal 侧行为零变更 |
| Redis 写频率 | 低 | 写由 `ReportRoutingOutcome` 驱动(每请求 1 次),非每 token |
| 预设价格写错 | 中 | `preview` dry-run 必经;`pricing_source='preset'` 单独审计列;super_admin only |
| 与 O5 耦合 | 高(已知) | 本 RFC 不解 O5,只解决"评分正确时如何动作" |
| `suggest_stop` 被误解为强制 | 中 | §3.6 明确只写头不断流;文档与 header 命名都用 `Suggested` |
| Windows syscall 编译错误 | 已知,与本任务无关 | 不在本 RFC 范围(`internal/fsstore`、`bg/storage_retention_worker`) |

## 8. 显式不做

- 不训练 ML 模型(纯规则评分)
- 不改 OpenAI/Anthropic 协议(只加 `X-Gw-*` 头)
- 不引入新存储依赖、不建新表、不加新 Redis 连接
- 不做跨会话联邦学习
- 不改现有评分权重(§3.3 已满足诉求)
- auto 侧不强制终止会话(只 `suggest_stop`)

## 9. 关联文档

- `AUDIT-auto-multidim-progress-reuse-analysis.md` —— **本 RFC 的审计依据(532 行)**
- `autoroute/V2_IMPLEMENTATION_STATUS.md` —— auto V2 评分历史与 feature flag 机制
- `domains/hooks/goal/loop_detector.go` —— 停滞检测 → 换模型的参考实现
- `domains/hooks/goal/cost_presets.go` —— 成本分档预设范式
- `autoroute/outcome_feedback.go` —— outcome 回路(2026-09-08 audit Track A)

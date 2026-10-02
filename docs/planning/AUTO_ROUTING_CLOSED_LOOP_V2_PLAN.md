# AUTO 路由专项测试与分类/选型闭环优化总体方案 v2

- 日期: 2026-09-24
- 状态: 方案定稿，待执行（P0 起可立即开工）
- 前驱文档:
  - `docs/planning/AUTO_ROUTING_OPTIMIZATION_PLAN.md`（v1.1, 2026-09-15，首轮现状审计 + OmniRoute 研究 + Phase 1/2）
  - `docs/planning/TASKPROFILE_MODULE_DESIGN.md`（2026-09-18，taskprofile v1 已实现 + §八审计补记）
- 本文定位: 上述两份文档之后的**第二代总体方案**——v1 落地后重新盘点资产、对照外部项目找缺口，
  把"专项测试 → 人工标注 → 自动优化 → 门控生效"整合成以 taskprofile 为核心的插件式闭环模块，
  并给出 P0-P4 待执行规划。

---

## 一、需求与交付口径

原始诉求（与 2026-09-18 taskprofile 立项同源，本次为第二迭代）：

> 针对 auto 模型进行专项测试；在测试过程中或从实际运行情况中，对任务的解析归类、
> 模型的选择进行**自动优化**；建立机制让每个请求的自动任务类型分配可以被**人工优化与标注**
> 后反馈回系统；把"任务类型识别 + 建议的模型分层"数据**整合成一个模块**，减少对其它模块
> 的干扰，以**类似插件**的形式存在，将来可独立升级。

v1（taskprofile）已交付：人工修正表 + 4 个 admin 端点 + 分层建议引擎（人工触发 apply）+
置信度阻尼回冲。v2 在此之上补齐"自动优化真正生效"与"专项测试体系化"两块：

| # | 需求 | v1 已交付 | v2 增量交付 |
|---|------|-----------|-------------|
| 1 | auto 专项测试 | autoclass-bench（LLM 兜底分类器宏 F1 门禁）、autoroute-e2e-audit（60 例 E2E 套件）、tuning-backtest（调参回放）——三件散落工具 | 统一 harness（`auto-testbench`）：套件扩充 ≥200 例 + 一键回归 + 报告归一 + 分类/选型双层评测（G4） |
| 2 | 任务解析归类自动优化 | 修正 → PostClassify 置信度阻尼（间接：更早触发 LLM 重分类） | 修正 → **调参提案**自动生成（关键词/阈值建议草稿，走 tuning-backtest 门禁 + 人工批准热调参）（G5） |
| 3 | 模型选择自动优化 | 分层建议引擎（人工点 apply 只落 `task_type_tier_config`，**运行时消费为零**） | TierSelector 热路径接线：影子模式先行 → 灰度开关 → 生效（G1）；tier 词汇统一文档化（G3） |
| 4 | 每请求人工标注反馈 | 双流已通（provider 维度 `training_human_annotations` + 任务类型维度 `task_type_corrections`，AnnotationView 联动） | 采样策略增强（分歧采样/分层抽样）+ 标注效率与质量指标（G7） |
| 5 | 插件式整合模块 | `taskprofile/` 包（热路径零改动、不 import autoroute/routingopt） | 保持边界，模块内新增 analyzer/suggest v2 子能力；对外触点仍全部可摘除（见 §4.2） |
| 6 | 独立升级 | 档案 schema_version + `TASKPROFILE_OVERLAY` 热加载 + reload 端点 | 延续；评测套件与建议引擎产物也带版本，升级=换数据不动代码 |
| 7 | （v2 新增）效果可度量 | 分类正确率 Prometheus 聚合、auto_route_selections 训练事实 | 单值质量指标 GRRQ + 回归门禁（离线套件阈值进 harness）（G8） |

---

## 二、现状盘点（截至 2026-09-24）

### 2.1 已交付资产

**决策热路径**（`model=auto` → 真实模型）：

| 组件 | 位置 | 说明 |
|------|------|------|
| 入口拦截 | `domains/streaming/auto_route.go:345` | `maybeResolveAuto`，魔法名 `auto`；响应头 `X-Gw-Auto-Decision`；非 chat 端点 feature flag 默认关 |
| 决策器 | `autoroute/decision.go` `decision_v2.go` | 会话意图缓存 → profile(smart/speed_first/cost_first) → 分类 → 三级候选漏斗 → 覆盖级联（override pin > role_route > work_type tier） |
| L1 分类器 | `autoroute/classifier.go` | 8+3 类启发式（vision 硬覆盖/编程强信号/专项硬覆盖/长上下文/agent/正则层/关键词层），阈值 TuningStore 热调；低置信触发 LLM 兜底（`classifier_llm.go`，3s 超时） |
| V3 细分类 | `autoroute/classifier_v3.go` `task_types_v3.go` | 10 类 tier-a/b/c 体系——**已实现未装配**（仅 taskprofile 档案当数据用） |
| Tier 选择器 | `autoroute/tier_selector.go` | 头部覆盖/深度降级/租户覆盖/`task_type_tier_config` 表——**无生产构造点** |
| 反馈 | `autoroute/outcome_feedback.go` `affinity_store.go` | outcome 回填、task→model 亲和学习（role_route 行剔除） |

**插件与反馈模块**：

| 组件 | 位置 | 说明 |
|------|------|------|
| routingopt 插件 | `routingopt/`（29 文件；R63 §三.12 勘误：原记 30） | PreClassify/PostClassify/RecommendModel/RecordFeedback 四 hook + ONNX re-ranker + A/B + AdaptiveLearner |
| taskprofile v1 | `taskprofile/` | 档案注册表（overlay 热加载）+ 修正存储（迁移 724）+ 建议引擎（纯函数）+ 4 admin 端点 + 审计 hook（R45） |
| 标注双流 | `training_human_annotations`(669) / `task_type_corrections`(724) | AnnotationView 提交时联动写两流；CSV 导入导出（R43 后单批次事务化） |
| 人工反馈回冲 | `routingopt/confidence.go:53` | `blended=(acc×S+human_rate×2×C)/(S+2×C)` 置信度阻尼，已上线 |
| reqprobe | `internal/reqprobe/` | 请求侧异常探测 + 学习开关（默认开，24h TTL）——参数维度自动优化已闭环，可作范式参照 |

**数据落点**：`request_logs(task_type/auto_decision/auto_confidence/is_auto_request)`、
`auto_route_selections(_hot)`（分类器/置信度/候选/reward/role 归因）、`session_turns.task_type`、
`task_type_tier_config`、`tuning_params`/`tuning_signals`、`work_type_config`/`work_type_model_route`。

**专项测试工具（现存六件，散落）**：`cmd/autoclass-bench`（宏 F1）、`cmd/autoroute-e2e-audit`
（60 例套件 E2E）、`cmd/tuning-backtest`（提案回放"would fix X miss Y"）、`cmd/routing-test-client`
（会话负载）、`cmd/traffic-replay`（流量回放+显著性）、`cmd/scenario_driver`（S22/S23 场景）。

### 2.2 缺口分析

| # | 缺口 | 证据 | 影响 |
|---|------|------|------|
| G1 | **分层建议运行时消费为零** | `autoroute.NewTierSelector` 无生产构造点（TASKPROFILE §8.5 遗留#1）；apply 只持久化运营意图 | 人工标注→分层优化的链条断在最后一环，"模型选择自动优化"未生效 |
| G2 | **V3 十类分类器未进热路径** | grep 全仓 `NewV3Classifier` 仅 taskprofile handler/csv 自用 | 细分类只在档案里当静态数据，分类精度天花板停留在 L1 |
| G3 | **两套 tier 词汇并存未统一** | tier-a/b/c（成本档）vs primary/secondary/fallback（路由档），`work_type_model_route.tier` 与 `task_type_tier_config.preferred_tier` 语义不同 | 文档与运维认知负担，P2 接线前必须先裁决 |
| G4 | **专项测试无统一入口/回归门禁** | 六件工具各自 CLI；套件仅 60 例；CI 不跑分类回归 | 改分类器/调参无离线安全网，靠三轮人工审计（09-14~16）兜底 |
| G5 | **修正→分类器改进断链** | `tuning_signals`/`tuning-backtest` 基础设施在，但无"从人工修正自动生成调参提案"的通路 | 归类自动优化只有置信度阻尼一条间接路径 |
| G6 | **无 task-profile/tuning 管理页** | `web/public/menu-config.json` 模型与路由组无对应项；tuning admin API（`admin/auto_route_tuning.go`）无前端 | 运营只能 curl；标注闭环最后一公里依赖 AnnotationStats 页内嵌 |
| G7 | **标注采样策略单一** | 低置信采样 + first-turn 两种 | 无分歧采样（分类器间不一致）/分层抽样，标注边际效益受限 |
| G8 | **无单值质量指标与回归门禁** | 分类正确率有 Prometheus 聚合，但无"改一版路由，整体变好还是变坏"的可判定口径 | 灰度裁决靠人工读多维报表（Wave2 报告即此形态） |

---

## 三、外部经验映射（OmniRoute 详研 + 高星项目）

### 3.1 OmniRoute（本地 `/Users/xutaohuang/workspace/ai/OmniRoute`，TS/Next.js 网关）

| 经验 | 出处 | 本仓库落地 |
|------|------|-----------|
| 任务类型映射**意图而非具体模型 id**（字面模型会腐烂且绕过健康/熔断检查） | `open-sse/services/taskAwareRouter.ts:157-183` | 已隐式遵守（分类→漏斗而非 pin 模型）；写入纪律：taskprofile 建议只产 tier 意图，永不产模型名 |
| RouterStrategy 小接口 + 注册表 + fail-open + Decision 带 reason | `routerStrategy.ts:47-51,351-365` | P2 TierSelector 接线时同样带 `reason` 字符串落 `auto_decision`，可解释性零成本 |
| 多信号分层：任务类型（关键词）+ 复杂度（6 维数值→5 级→最低档）+ 意图，各服务不同决策点 | `specificityRules.ts` `complexityRouter.ts` | 对应本仓 classifier(L1) + structured_features(658) + profile；P1 调参提案可引入复杂度维度做条件阈值 |
| **tools schema 存在 → tier 硬升级**（函数调用可靠性优先于成本） | `complexityRouter.ts:55-60` | 建议引擎 v2 增加同款硬规则：function_call 类任务 min tier 不降 |
| taskFitness 五级解析链：user_override > 外部榜单 > 能力 tier > 静态表 > 通配 | `taskFitness.ts:344-383` | 本仓等价链：taskprofile overlay > corrections 建议 > `task_type_tier_config` > 档案默认——P1 把该链显式化进 suggest v2 |
| 反馈闭环复用请求日志（滚动窗口聚合 p95/errorRate，历史不足给保守 bootstrap 而非拒判） | `combo.ts:371-529` | credential_model_index 5min 滚动已是此形态；bootstrap 思想用于建议引擎样本不足分支 |
| 离线评测单值指标 **AIQ = successRate×100 − p95/1000 − cost×1000** + Pareto + 回归阈值门禁 | `scripts/router-eval/` `src/lib/routerEval/index.ts:146-150` | 直接借鉴定义本仓 GRRQ（§4.7），进 auto-testbench 回归门禁（G8） |
| 影子路由采样（fire-and-forget 不影响真实响应） | `shadowRouting.ts` | P2/P3 影子模式的形态参照（本仓用"决策旁路记录 would-be"更轻，不起真实上游流量） |
| 坑：静态 pattern 短者遮蔽长者（最长优先修复）；关键词分类精度天花板；配置写 DB 后必须有显式 hydrate | `taskFitness.ts:315-324` 等 | 套件设计覆盖别名对（glm-5.3 双映射教训）；V3 词汇最长优先；overlay 已有 reload+hydrate |

### 3.2 RouteLLM / OpenRouter / LiteLLM（公开项目）

| 经验 | 落地 |
|------|------|
| RouteLLM（lm-sys，ICLR 2025）：用偏好数据训练 router，四变体（相似度/BERT/因果 LLM/分类器），开源 train-serve-eval 框架 | P4 远期：本仓 `auto_route_selections`（reward 列）+ 双流标注已是现成偏好数据集；先做离线评估（router vs 规则基线），显著优于才考虑学习型 router 上线 |
| OpenRouter Auto：按意图分类路由，公开 ~20 意图类别 | 意图类别数参考：L1 11 类 vs V3 10 类已够用，不盲目扩类，优先提准 |
| LiteLLM/label-studio 标注回流惯例：人工样本权重 ×2 | 已落地（670 起口径），v2 延续 |
| MorphLLM 等报告路由分级普遍带来 40-70% 成本节约 | 作为 P2 灰度的收益预期锚点（保守取下限 30%，与 V3 计划目标一致） |

---

## 四、v2 方案总体设计

### 4.1 设计原则

1. **插件边界不动**：一切新逻辑收敛进 `taskprofile/`（+独立 cmd 工具），不 import
   autoroute/routingopt（既有 wiring_guard_test 钉桩）；对外触点全部加法、可整体摘除。
2. **灰度纪律延续**：热路径新消费点（TierSelector/V3）一律 feature flag 默认 off +
   flag-off 字节级不变钉桩（`feature_flags.go` 既有契约）；影子先行、灰度跟进。
3. **显式优于隐式**：建议→生效必须过门禁（离线回归阈值）+ 人工批准；不做运行时配置被
   反馈环静默漂移（v1 §5.2 已裁决，v2 的"自动"= 自动**生成提案**，不是自动**改配置**）。
4. **复用优先**：调参走既有 tuning_params/tuning_signals/backtest；标注走既有双流表；
   评测复用既有六件工具，只做编排与扩充，不造新轮子。
5. **意图与模型解耦**：模块产出物只有任务类型 + tier 意图 + 置信度，永不直接产模型名。

### 4.2 架构：taskprofile v2 = 任务智能插件（五层）

```
┌──────────────────────────────────────────────────────────────────┐
│ L4 热路径消费（灰度门控，autoroute 侧已有能力，只做接线）          │
│    TierSelector(tier-a/b/c→候选过滤)  +  V3 影子分类              │
│    flag: AUTO_TIER_SELECTOR / AUTO_V3_SHADOW  （默认 off）        │
└──────────────▲───────────────────────────────────────────────────┘
               │ task_type_tier_config（唯一运行时配置面）
┌──────────────┴───────────────────────────────────────────────────┐
│ L3 应用层（人工批准 + 门禁）                                       │
│    apply-tier-config（已有）｜调参提案→tuning-backtest→admin 热调  │
└──────────────▲───────────────────────────────────────────────────┘
               │ 建议（带 reason + 版本）
┌──────────────┴───────────────────────────────────────────────────┐
│ L2 建议层（taskprofile v2 新增 analyzer/suggest v2，纯函数可单测）│
│    a) 分层建议（已有，+tools 硬规则/五级解析链显式化）             │
│    b) 调参提案：从被改判样本提取判别 token → keyword/阈值草稿      │
│    c) 置信度阻尼（已有，routingopt CorrectionSource）              │
└──────────────▲───────────────────────────────────────────────────┘
               │ 事实
┌──────────────┴───────────────────────────────────────────────────┐
│ L1 评测层（auto-testbench：统一 harness，G4/G8）                   │
│    离线套件回归（≥200 例）｜E2E 审计｜GRRQ 单值指标+阈值门禁        │
└──────────────▲───────────────────────────────────────────────────┘
               │
┌──────────────┴───────────────────────────────────────────────────┐
│ L0 事实层（全部既有，零新表起手）                                  │
│    auto_route_selections ｜ task_type_corrections ｜               │
│    training_human_annotations ｜ tuning_signals ｜ request_logs    │
└──────────────────────────────────────────────────────────────────┘
```

### 4.3 专项测试统一 harness（G4）

新增 `cmd/auto-testbench`（编排器，纯加法）+ `scripts/auto-testbench.sh`（一键入口）：

1. **套件扩充**：`autoroute/testdata/auto_matching_suite.jsonl` 60 例 → ≥200 例。来源：
   - 从 `auto_route_selections_all` 按任务类型分层抽样真实请求的结构化特征（658 迁移列，
     不含 prompt 原文，隐私口径与 annotation CSV 一致）；
   - 人工修正样本回流（human_task_type 作为金标签——**修正即造测试集**，这是标注反哺评测的关键设计）；
   - 保留既有 60 例为基线子集，禁止改写（回归锚点）。
2. **双层评测**：
   - 分类层：macro-F1/正确率（按任务类型分桶），复用 autoclass-bench 指标口径；
   - 选型层：每例记录 chosen/tier 意图/候选 top3，E2E 模式复用 autoroute-e2e-audit。
3. **GRRQ 单值指标**（借鉴 OmniRoute AIQ，适配本仓语义）：
   `GRRQ = 分类正确率×100 − 平均候选分差惩罚 − tier 过配惩罚`（具体权重 P0 定稿，
   首版可直接用 `正确率×100 − 过配率×30`，过配率=简单任务落 tier-a 占比）。
4. **门禁**：`auto-testbench -gate baseline.json`，低于阈值 exit 1；本地提交前跑，
   进 CI（与 `TestNumericUpMigrationVersionsAreUnique` 同级纳入 pre-push 习惯清单）。
5. **报告**：JSONL + markdown 汇总，失败按任务类型归因（哪类掉分、掉在分类还是选型）。

### 4.4 自动优化闭环（三条回路）

**回路 A：置信度阻尼（已上线，v1）** —— 修正频繁的任务类型置信度被压低 → 更早触发
LLM 兜底重分类。不动。

**回路 B：分层建议 → 门控生效（v2 核心，G1）** ——
suggest 引擎产出 tier 建议 → 人工 apply 落 `task_type_tier_config`（已有）→ **新增**：
TierSelector 消费该表（影子模式先记录 would-be tier，不影响路由；灰度开关打开后参与
候选过滤，位置在 override/work_type 级联之后，尊重既有优先级）。接线点：
`cmd/gateway/main.go` 构造 `NewTierSelector` 注入 Decider（flag 默认 off +
flag-off 字节级不变钉桩测试）。

**回路 C：调参提案（v2 新增，G5）** ——
`taskprofile/analyzer.go`：对"修正率最高的任务类型"聚合被改判样本的判别特征
（structured_features 的语言/长度桶/code 指示/关键词命中），生成 tuning 提案草稿：
新增关键词 X→通道 Y、阈值 Z 从 0.7→0.65 等。提案**不自动生效**：先过
`tuning-backtest` 回放（would fix/miss 量化），报告进 admin 页，人工批准后走既有
`/api/admin/auto-route/tuning` 热调参。提案与回放结果落 `tuning_signals`/
提案记录（复用既有 proposal 形态，P1 执行时核对 schema，若 proposal 无落表则加一张
`tuning_proposals` 独立表——遵守迁移五点同步纪律）。

### 4.5 人工标注与回流增强（G7）

1. 采样策略 v2（改 `admin/annotation_handler.go` samples 查询）：
   - **分歧采样**：LLM 兜底分类器与启发式结论不一致的行（classifier 列可判）优先入队；
   - **分层抽样**：按任务类型 × 置信度桶配额，避免高频类垄断；
   - **修正即测试**：每次人工修正自动转为套件候选样本（脱敏结构化特征 + human 标签）。
2. 前端（G6）：新增 `web/src/views/TaskProfileView.vue`（档案+统计+建议+apply，替代
   AnnotationStats 页内嵌区）与 `web/src/views/AutoTuningView.vue`（tuning 参数 +
   提案列表 + backtest 结果 + 批准按钮）；菜单加 `/routing-v2/task-profile 任务档案`、
   `/routing-v2/auto-tuning 路由调参` 进"模型与路由"组；i18n 八语言补齐；
   **vue-tsc 进构建门禁**（既有盲区：build 不跑 typecheck）。

### 4.6 热路径消费接线（G1/G2/G3，全部灰度）

1. **G3 先行裁决**（纯文档 + 注释，P2 第一步）：明确两套词汇的映射与边界——
   tier-a/b/c = 成本能力档（taskprofile/TierSelector 语义），primary/secondary/fallback =
   凭据路由档（Index/work_type 语义）；在 `autoroute/tier_selector.go` 与
   `work_type_route_store.go` 头注释互相引用，本文档附录登记映射表。
2. TierSelector 接线：flag `LLM_GATEWAY_AUTO_TIER_SELECTOR`（默认 off）。off =
   现状字节级不变（钉桩）；on-shadow = 决策旁路记录 would-be tier 进
   auto_route_selections（新增列或 auto_decision JSONB 内字段，优先后者避免迁移）；
   on = 参与 L2 漏斗前过滤（MinStandardIQ 门禁语义保持，取交集不替代）。
3. V3 影子分类：flag `LLM_GATEWAY_AUTO_V3_SHADOW`（默认 off）。影子记录 V3 结论 vs
   L1 结论的分歧率进 Prometheus；套件离线对比 macro-F1；分歧率与 F1 双达标后再谈替换
   （替换本身是独立裁决，不在本计划内完成）。

### 4.7 指标与验收

| 指标 | 定义 | 现状基线 | v2 目标 |
|------|------|----------|---------|
| 分类正确率 | 套件 macro 判对比例（E2E 口径） | 60 例套件 ~98.9%（09-16 审计） | 扩到 ≥200 例后 ≥95% 且不掉回 |
| 人工修正率 | corrections 中 agrees=false 占比 | 待 P0 建基线（按任务类型分桶） | 每类 <30%（建议引擎触发线下限） |
| LLM 兜底占比 | classifier=llm_fallback 行占比 | 曾 53%→30%（N 系列修复后） | 不因阻尼回路劣化超 +5pp |
| tier 过配率 | 简单任务（chat/simple 类）落 tier-a 占比 | 无（TierSelector 未生效） | 影子期建立基线，灰度后 ↓30% |
| GRRQ | §4.3 单值指标 | P0 定基线 | 每次调参/档案变更门禁不回退 |
| 成本 | auto 请求 avg cost vs 全 tier-a 假想 | P0 起在 testbench 报告 | 灰度验证期 ≥30% 节约（V3 计划口径） |

---

## 五、数据模型增量

起手**零新迁移**。可能的新增（均在对应 Phase 执行时按需裁决）：

1. `auto_route_selections.auto_decision` JSONB 内增 `would_be_tier`/`v3_shadow` 字段
   （影子记录，避免加列）；
2. `tuning_proposals` 表（仅当既有 proposal 无落表时；迁移遵守五点同步 +
   `TestNumericUpMigrationVersionsAreUnique`）；
3. 套件文件属主归 `autoroute/testdata/`（与离线回归同源），testbench 只读。

---

## 六、待执行规划

| Phase | 内容 | 主要触点 | 验收门禁 | 风险 |
|-------|------|----------|----------|------|
| **P0 测试地基 + 前端最后一公里**（可立即开工，纯加法） | ① auto-testbench 编排器 + 一键脚本 + GRRQ 定稿 ② 套件 60→≥200 例（分层抽样 + 修正回流） ③ TaskProfileView/AutoTuningView + 菜单 + i18n ④ vue-tsc 进 build 门禁 ⑤ 采样策略 v2（分歧+分层） | `cmd/auto-testbench/`（新）、`scripts/auto-testbench.sh`（新）、`autoroute/testdata/`、`web/src/views/`、`web/public/menu-config.json`、`admin/annotation_handler.go` | 套件回归绿 + 基线 JSON 落库；四语言页面可用；`npm run typecheck` 进 CI；标注采样 SQL 有执行计划 | 低（无热路径改动） |
| **P1 调参提案闭环**（G5） | analyzer 从 corrections/tuning_signals 生成提案草稿 → tuning-backtest 自动回放 → admin 页展示 would fix/miss → 人工批准热调参 | `taskprofile/analyzer.go`（新）、`cmd/tuning-backtest`（复用）、`admin/auto_route_tuning.go`、AutoTuningView | 端到端演示：一批真实修正 → 提案 → 回放量化 → 批准生效 → 套件复跑不回退；提案全程可审计 | 中（提案质量；靠 backtest 门禁 + 人工批准兜底） |
| **P2 TierSelector 生效**（G1/G3） | ① 两套 tier 词汇裁决文档化 ② 影子模式（would-be tier 记录）③ 灰度开关接线 ④ 影子数据建立过配率基线 → 小流量灰度 | `cmd/gateway/main.go`、`autoroute/tier_selector.go`、`autoroute/decision_v2.go`（过滤位）、feature_flags | flag-off 字节级不变钉桩；影子期 ≥7 天且过配率基线成立；灰度期 GRRQ 与成本不回退；回滚 = 关 flag | 中（与 MinStandardIQ/work_type 级联交互需测试矩阵覆盖） |
| **P3 V3 影子分类**（G2） | V3 分类器影子接线，分歧率指标，套件 F1 对比 | `cmd/gateway/main.go`、`autoroute/classifier_v3.go` | flag-off 不变钉桩；影子分歧率 + F1 报告连续两周；**替换另行裁决** | 中 |
| **P4 学习型 router 评估**（远期） | 用 auto_route_selections(reward) + 双流标注构造偏好数据集，离线评估学习型 router vs 规则基线（RouteLLM 四变体思路） | 离线 only，`cmd/` 新分析工具 + `docs/` 评估报告 | 仅当离线显著优于基线（GRRQ + 成本双指标）才立项上线 | 高（数据量/漂移；不达标即止步于报告） |

每 Phase 结束按既有惯例：`docs/audit/` 记录验证证据；部署验证走 llm-gateway-deploy-test
skill 流程（本地→252 只读标注验证→245/154 按当期窗口）；**252 共享库只读 + 标注表写入，
不注入合成流量**。

---

## 七、风险与纪律

1. **反馈环自激**：回路 B/C 都可能把系统推向过配/震荡。防线：建议阈值（≥5 样本、
   ≥30% 修正率）、门禁（套件回归 + GRRQ）、人工批准、apply 后冷却期（同一任务类型
   7 天内不重复升档，防抖）。
2. **灰度契约**：所有热路径 flag 默认 off；off 路径字节级不变是硬性钉桩要求
   （延续 Wave2/R51 纪律）。
3. **迁移纪律**：新增表走五点同步（canonical/embeddata/runner/embeddedSQLFiles/
   sequence）+ 唯一性测试；push 前 `pull --rebase`（多写者 main）。
4. **隐私红线**：套件与提案只用结构化特征（658 迁移列口径），不落 prompt 原文；
   CSV 导入导出维持 v1 上限与审计 hook。
5. **并行会话**：编辑共享热区（main.go/admin/handler/menu-config）前先读 HEAD 真实
   形态（taskprofile §8.4 教训）。
6. **不过度承诺"自动"**：自动的是评测、提案生成、量化回放；生效永远是门禁 + 人工批准。
   这与 v1 §5.2 显式原则一脉相承，也是本方案对"自动优化"的安全定义。

---

## 附录 A：两套 tier 词汇映射（P2 裁决草稿）

| 成本档（taskprofile/TierSelector） | 语义 | 对应路由档参考（Index/work_type） |
|------|------|------|
| tier-a（$15-50/1M） | 高性能 | primary 池中的高 popularity 子集 |
| tier-b（$5-15/1M） | 标准 | primary/secondary 主力 |
| tier-c（$0.5-5/1M） | 经济 | secondary/fallback + free 池 |

映射仅用于报表口径统一，运行时各自独立判定，不做强绑定。

> **2026-09-25 补记**：本附录已按 P2 轮升格为 G3 裁决定稿（含两处修订：词汇同名陷阱列、provider_models.tier 第三套污染登记），**裁决见 P2 设计稿** `docs/planning/AUTO_ROUTING_V2_P2_TIER_SELECTOR_DESIGN.md` §3。

## 附录 B：与前驱文档的关系

- `AUTO_ROUTING_OPTIMIZATION_PLAN.md`（v1.1）的 Phase 1/2 已完成（routingopt/标注/ONNX），
  其 OmniRoute 12 因子研究中 taskFit 因子已由 taskprofile v1 以人工修正率补上第一块；
  本文 P0-P4 是其后续阶段的收敛重排。
- `TASKPROFILE_MODULE_DESIGN.md` 的 §8.5 遗留清单在本文的对应：遗留#1（TierSelector
  消费为零）→ P2；#3（252 不发 web 资产）→ P0 部署验证时按当期脚本能力裁决；#2/#4 低风险
  挂账维持。

# Jev 快速判断节点适用性分析与整合方案 —— 任务编排「匹配 / 过程 / 完成」三节点

日期：2026-09-24
状态：方案（Phase 0 已落地，其余待评审）
前置调研：2026-09-18 Jev 调研轮（Awesome Jev 14 项目全量抓取 + TypeSafe 官方公告/API 文档核对，结论存 `docs/design/` 本文件与提交 a1f561bb1 的 classifier_jev.go 注释）
关联代码：`autoroute/classifier_jev.go`（已落地）、`autoroute/decision.go`（fallback 槽）、`taskprofile/`（画像+纠正闭环）、`autoroute/feedback.go:137`（DetectSessionDrift）

---

## 0. 结论摘要

针对「任务的自动化匹配 / 任务过程中的自动化判断 / 任务完成判断」三类快速判断节点：

| 节点 | 判定 | 依据 | 行动 |
|---|---|---|---|
| A. 入口任务匹配（task→模型/层级） | **强意义，已落地实验** | jev-codex-router 同构场景实测 −60% 成本、0.6s/$0.00003 每轮、置信度门单向安全 | 已有 classifier_jev.go（a1f561bb1），等 TYPESAFE_API_KEY 在 245 开 |
| B. 过程判断（会话内每轮/每步） | **有意义，但严格限定 advisory + 冷路径** | jev-ultrafast/1v1/winnow 证明高频判断可行，但我们的热路径是请求阻塞路径，延迟预算不兼容；会话级（非请求级）判断可行 | Phase 3：drift 二意见 + 标注预筛；明确不做清单见 §4.4 |
| C. 任务完成判断（会话级 goal-reached） | **有意义，且是我们当前空白** | neo4jev 证明 choice+noul 同调用零额外往返的完成判断模式；我们 summarystore/titlestore 只有内容生成、没有完成信号 | Phase 2：shadow 起步，只写决策日志不驱动行为 |
| D. 批量语义筛（map-reduce 高基数） | 边际意义 | blink/playground 模式可用，但候选上限 255 不适配我们规模 | 仅标注工作台预筛（并入 Phase 3），不做通用服务 |

核心价值公式（三类节点共用）：**判断延迟 数秒→70-500ms + 校准概率使「置信度门」策略首次可落地（现有 LLM 兜底的置信度是假的——自由文本解析）+ 单次调用批量多问摊薄往返**。

一句话回答标题问题：**有意义，但意义不在"更快"，而在"校准的概率分布"让每个判断节点第一次有了可审计、可设阈值、可回放校准的决策依据；Jev 只是当前最快的一个供应商，方案按供应商可替换设计。**

---

## 1. 分析方法与 Jev 特性基线

### 1.1 Jev 关键特性（决策相关的最小集）

- 三原语：`noul`（是/否概率）、`choice`（选项概率分布+confidence）、`score`（有序等级，可落两级之间）。
- **一次调用可携带多个问题**——jev-ultrafast 的"Two decisions, one network round trip"和 neo4jev 的"choice+noul 同调用，每 hop 恰好一次往返"都建立在这上面。
- 70-500ms、输入 $0.042/MTok、输出免费（≈$0.00003/决策）；基数上限 255。
- 校准训练（RLCD）：confidence 由概率分布导出，生态项目直接把它当策略阈值用。
- 供应商风险：waitlist 早期访问、定价可持续性官方自认存疑、单点。

### 1.2 评估维度

每个候选节点按五维打分：延迟预算是否兼容 / 失败时是否可 fail-open / 数据出境是否可接受 / 判断质量是否可校准验证 / 调用量级与成本。

---

## 2. 生态项目判断节点证据表

14 个项目中 12 个真含 Jev（agent-desktop、prism 两项目页面实无 Jev，已剔除），按四类节点归纳：

### 节点 A：入口任务匹配（task → 模型/层级）

| 项目 | 机制 | 实测 |
|---|---|---|
| jev-codex-router | 每轮 choice 判难度 → 三档模型（luna/sol/astra）+ effort；**置信度 <0.5 时"不降级"**，保持中档并记日志 | 237 真实 turn 回放 **−60% 成本**；0.6s、$0.00003/轮；1065 次/天实测 |
| typesafe-on-neon | 判断-然后-转发门：Jev 审请求体 → pass/review/block → 转发原字节 | 注入拦截类，归入安全节点 |

**工程定式（A 类）**：置信度门单向安全（低置信只升不降）；决策全量 JSONL 落盘用于回放校准；kill switch 哨兵文件秒级退出；任何 Jev 错误 fail-open 走安全路由。

### 节点 B：过程循环判断（每步/每轮）

| 项目 | 机制 | 实测 |
|---|---|---|
| jev-ultrafast | 每次页面观察 → 编号元素表 → 一次预测调用返回操作+目标（**投机：CLICK 时只有 click_target 生效**）；文字生成延迟到 TYPE_TEXT 才调小模型 | Google Flights 7.1s 全程；协议调用 1092→101（−90%） |
| 1v1 Jev | 9Hz 决策 tick，扇出 move/yaw/ADS/fire/jump；**TypeSafe 不可达时确定性启发式兜底 "matches never stall"** | 实时控制回路证明 70-500ms 足够高频循环 |
| winnow | Read/Bash/Grep 输出 >1500 字符才触发、25 行分块、每块问"当前任务需要吗"；**P≥0.5 保留、P<0.1 才藏、不确定保留、含错误绝不藏** | 上下文 GC 的不对称安全设计 |

**工程定式（B 类）**：判断不对称（不确定→保守动作）；确定性兜底保活；触发条件有下限（不是每步都判，先过便宜门）。

### 节点 C：完成判断（goal-reached）

| 项目 | 机制 | 实测 |
|---|---|---|
| neo4jev | 每跳 choice（走哪条边）+ **noul（goal 到达没）同调用**；对数概率 beam search；**goal 概率过阈值早停** | "each hop costs exactly one round-trip"——完成判断零额外成本 |
| jev-review | 五维风险矩阵 choice → 严重度 score → **条件 choice 决定是否升级重审** | "escalation 由概率决定，阈值留代码" |

**工程定式（C 类）**：完成判断 piggyback 在已有判断调用里（不单飞）；阈值可配；升级/终态动作由代码执行不由模型执行。

### 节点 D：批量语义筛（高基数 map-reduce）

blink（100 walker 按路径名概率分配）、playground（>253 候选二分递归 choice 收敛）。适用面：无索引场景的一次性筛除。我们的模型目录/凭据池都有索引，**通用场景不缺这个能力**；唯一对位是标注工作台样本预筛（见 §3.4）。

---

## 3. 映射到 llm-gateway-go（代码实证）

### 3.1 节点 A：任务匹配 —— 已落地实验，画像层是放大器

- 分类器链（heuristic → fallback 槽）已由 `classifier_jev.go`（a1f561bb1）占位，双 env 门控默认关，等 key。
- **放大器在 taskprofile**：`taskprofile/suggest.go` 的层级建议由**人工纠正率**驱动升级（`correctionEscalationRate=0.30`）——这是现成的校准闭环，但纠正数据靠人工标注积累，慢。Jev 决策日志若持续记录（分类结果+概率+后续人工纠正），可直接喂 correction stats，把"最弱任务类型"的识别从月级提到天级。
- 会话意图缓存（`session_intent_cache.go` shouldReclassify）复用同一分类信号，无需单独接 Jev。

### 3.2 节点 B：过程判断 —— 只做会话级 advisory，不碰请求阻塞路径

- **明确不是我们的**：客户端上下文 GC（winnow 场景）——网关不拥有 agent 的上下文窗口，强行做只能做响应旁路筛选，收益低风险高，不做。
- **是我们的、且是纯规则的**：`DetectSessionDrift`（feedback.go:137，prevTask≠currTask 且非硬覆盖即漂移）。规则版已够用于纠错记账；Jev 二意见价值在**区分"用户换了任务"vs"分类器错了"**——这直接影响纠正记账该记谁头上，是 taskprofile 闭环的质量问题。会话级触发（每会话漂移事件一次），非请求级，延迟预算兼容。
- retry/failover（smartretry/streamretry/errorsx）：**维持 2026-09-17 既定决策，语义判断不进凭据状态翻转路径**（404 假不可用事故教训：堵死恢复通道的代价远大于错分代价）。仅允许离线审计复核消费决策日志。

### 3.3 节点 C：任务完成判断 —— 当前空白，价值链最长

现状：会话层没有"这个多轮任务完成了吗"的信号。summarystore/titlestore 是内容生成（给标题/摘要），不是完成判断。缺这个信号的直接代价：

1. **sticky profile 更新时机盲**（affinity 会话滑窗到期是时间驱动，不是任务边界驱动）；
2. **会话摘要/标题触发点拍脑袋**（靠轮次/字数阈值）；
3. **标注工作台采样随机**（没有"完整任务会话"优先的信号）；
4. 计费/用量归因按请求，无任务级视图。

Jev 模式（neo4jev）：在已有判断调用里 piggyback 一个 `noul`（"该会话的原始诉求是否已被满足"）。网关侧对位：**会话末轮/空闲超时触发一次**（每会话至多一次，不是每请求），choice（任务类型复核）+ noul（完成度）同调用。起步只写决策日志。

### 3.4 节点 D：标注预筛 —— 并入 Phase 3

/routing-v2/annotations 的 first-turn 样本可用 choice 打分预排序（哪些样本值得人工标），提高标注吞吐。量小、离线、无风险。

---

## 4. 整合方案（分期）

### 4.1 Phase 1：jevclient 决策原语包（地基）

新建 `internal/jevclient`（纯客户端，无业务语义）：

- 三原语 + **批量问题单往返**封装（对齐 /v1/systemone wire format）；
- 复用 classifier_jev 的 breaker/env 门控/指标模式，抽出公共层；
- **决策日志表** `jev_decision_logs`（迁移 744+）：question_id、state 摘要（脱敏，同 ClassificationSignals.MarshalJSON 隐私保证）、答案、概率分布、confidence、latency、consumer（classifier/drift/completion）、correction 关联列（后续人工纠正回填）；
- **Provider 抽象**：`TYPESAFE_BASE_URL` 已可指自建端点；接口按 noul/choice/score 设计而非按 TypeSafe 路径设计，未来可换自托管约束解码实现（TypeSafe 官方自己评估基线就是 LLM 包装器）。

### 4.2 Phase 2：会话完成判断 shadow（节点 C 落地）

- 触发：会话空闲超时（如 10min）或末轮标记，每会话至多一次；
- 载荷：choice（任务类型复核）+ noul（完成度）+ score（会话质量，可选）单调用；
- **只写 jev_decision_logs，不驱动任何行为**；
- 验收：跑两周后出校准报告（分桶 reliability + 与人工标注对照），达标线见 §5；
- 成本上限：每会话 1 调用 ≈ $0.00003，全量会话也可忽略；env 门控 + 采样率双控。

### 4.3 Phase 3：过程判断 advisory 最小集（节点 B 落地）

- drift 二意见：DetectSessionDrift 命中时，Jev 判断"换任务 vs 分错类"，结果只影响**纠正记账归属**（classifier 错 → 记分类器账；换任务 → 记会话边界账），不翻转任何路由决策；
- annotations 预筛（节点 D）：离线批处理，choice 排序样本。

### 4.4 明确不做清单（红线）

1. **请求阻塞热路径上的任何新增 Jev 调用**（fallback 槽是既有低置信路径，量有界，属 A 类例外）；
2. **errorsx/凭据健康/retry 决策的语义翻转**——404 事故既定决策，只允许离线消费决策日志；
3. 客户端上下文 GC（所有权不在网关）；
4. 无 kill switch 不上线、无决策日志不上线、无采样率控制不上线。

### 4.5 横切要求（全 Phase 生效）

- 隐私包络：出境字段白名单（同 classifier_jev：封顶文本+结构信号，工具结果内容不出境）；
- env 门控三件套：总开关 + 采样率 + 每日成本预算熔断（超预算自动降级到关）；
- 指标：`jev_*` 指标族（按 consumer 分标签的调用结果/延迟/成本）+ 决策分布直方图；
- kill switch：env 秒级生效（进程内原子读，不改代码不发版）。

---

## 5. 度量与验收

- **校准验证（核心）**：对每个 consumer，按 confidence 分桶（0.5-0.6/0.6-0.7/...），桶内"概率 x% 的事件实际发生 ≈ x%"，最大偏差 <0.15 且样本 ≥100/桶 才算校准达标；
- 对照基线：节点 A 用 heuristic+LLM 兜底的历史分布；节点 C 用人工标注 100 会话；
- 业务指标：分类纠正率（taskprofile correction stats）是否下降、sticky 命中率是否提升（仅 Phase 2 达标后评估）；
- 供应商指标：延迟 P50/P99、错误率、熔断次数、日均成本。

## 6. 风险与开放问题

1. **供应商单点/停服**：Provider 抽象 + fail-open 已覆盖功能风险；商业风险（定价转正）由成本熔断兜底；
2. **隐私出境合规**：Phase 2/3 上线前需过一次租户合规评审（出境字段白名单固化为审计项）；
3. **校准漂移**：Jev 模型更新可能改变分布——决策日志需记录 model 版本（响应里有 model 字段，已解析）；
4. 开放问题：会话"完成"的定义口径（用户不再来 vs 诉求已满足）需在 Phase 2 校准阶段用数据定，先双 noul 都记；
5. 开放问题：multi-tenant 下决策日志的租户隔离策略（沿用 request_logs 分区模式 or 独立表）。

---

## 附：落地状态追踪

- [x] Phase 0：classifier_jev.go 入口匹配实验（a1f561bb1，2026-09-18）
- [ ] Phase 1：jevclient 包 + jev_decision_logs（待评审，迁移号领取时核对远端 744+）
- [ ] Phase 2：会话完成判断 shadow（待 Phase 1 + TYPESAFE_API_KEY）
- [ ] Phase 3：drift 二意见 + 标注预筛（待 Phase 2 校准报告）

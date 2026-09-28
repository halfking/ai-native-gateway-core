# D11 — auto 模型全量实现

> 领域编号: D11 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：model=auto 的自动路由/模型选择全量链路：会话分析→分类→推荐→re-rank（P2.2）→affinity→settle→feedback_log→AdaptiveLearner 回灌；ML/ONNX 推理；多维进度评分与成本预设（新 RFC）；auto 的数据管道（特征/promote 漂移）。
**不管**：通用队列与限流（D04）；统计聚合表（D10）；免费凭据池的发现（D13，但 auto 如何消费免费池在本域）。

## 2. 参考基线

设计文档：
- `docs/03-design/02-feature-design/design/AUTO_SELECTION_SPEC.md` + `AUTO_SELECTION_IMPLEMENTATION_PLAN.md`
- `docs/03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V2.md` / `V3_*` 系列
- `docs/auto-model-optimization/`（02 架构 / 05 存储优化 / 06 免费 LLM 接入 / 08 ML 训练管道 / 09 ONNX / 11 路线图）
- `docs/planning/AUTO_ROUTING_OPTIMIZATION_PLAN.md`、`docs/p2-ml-routing/`
- 新 RFC（48h 内）：`docs/03-design/FEATURE-REQ-auto-multidim-progress-and-pricing-presets.md` + `AUDIT-auto-multidim-progress-reuse-analysis.md`

代码入口：
- `autoroute/`、`routingopt/`、`domains/analysis/`、`domains/modelquality/`、`capabilityscore/`、`modeliqdata/`、`recentmodels/`
- `bg/`（settle/affinity worker）、`annotation/`、`ml-training/`

## 3. 检查清单

1. **决策链闭环**：分类→推荐→re-rank→affinity→settle→feedback_log→AdaptiveLearner→回灌，每环有产出与消费方；窗口内改动不断链（R30 已证实闭环健康，防回退）。
2. **终态 outcome 回填**：auto 请求（流式与非流式）所有终态（成功/中断/失败）都回填 outcome 到路由事实——**成功路径也要**（F1 教训：matched/expired/accuracy 统计依赖）。
3. **数据管道三缺陷基线**：promote 不丢特征列、chat 记录不丢 signals、exporter 扫描无 bug（历史缺陷，窗口内触碰相关代码必须复核）。
4. **回放契约**：回放/造数流量 model 参数必须小写 `auto`；大小写混用会绕过 auto 路径——新增测试/工具沿用。
5. **ML/ONNX 配对**：zipmap=False、ORT 版本配对（1.29.0 基准）、词汇表一致性——窗口内模型/词汇表变更需三对齐。
6. **新 RFC 落地一致性**：多维进度评分与成本预设的实现与 RFC v2（~370 行版）一致，不实现 v1 已废弃口径。
7. **灰度门槛**：放量构建 ≥ e8ddff41d；灰度验证报告口径（matched/expired/accuracy）可复算。

## 4. 历史回归点（轮末回注区）
- [R35 09-17] e8ddff41d 单标记半修双 P1 回归：成功终态只透传 IsAutoRequest（TaskType 恒 nil）→ isInternalAutoEntry 兜底把业务 auto 成功轮剔出 session_turns 镜像 + emitTuningSignal 全路径死（门需 TaskType，还读 AutoDecision/AutoConfidence）——并行 R34（01bd55ed7）扩展 propagateIsAutoRequestToEntry 全字段修复、R35 撤并采用并补钉桩；**教训：终态 entry 补 auto 字段必须全字段（单标记传播会翻转 isInternalAutoEntry 兜底语义），且 claim/镜像双门必须共享同一判定（telemetry.IsInternalAutoEntry）**；放量门槛构建随之更新

- [09-16/17] F1 成功终态 outcome 回填缺失 → matched 22→120 / expired 98→0 / accuracy 0→95.8%（闭环断裂，P0 级）— 修复 e8ddff41d（propagateIsAutoRequestToEntry）；放量门槛构建 ≥ e8ddff41d
- [历史] AUTO 路由数据管道三缺陷：promote 漂移丢特征列 / chat 记录丢 signals / exporter 扫描 bug —— 复核基线
- [R30] auto 决策链闭环证实健康（分类→…→回灌双回路有消费方）—— 防回退基准
- [运维] 回放必须小写 auto；154 旁路造数实例为验证环境
- [R37] R35-R2 口径定案：灰度观测面与训练信号面剔除全部网关合成轮（autoroute.IsSyntheticActor＝内部回环三 actor + goal- 前缀）；决策时 recordFeedbackAsync 单 choke 点设门（stash+legacy 双路径），终端 ReportRoutingOutcome 在计数器之前丢弃；settle baselines/mr LATERAL 与 work_types 四查询走 SQLExcludeSyntheticActors。遗留：selection 行 settle/affinity 反连接（selections 表无 origin_actor，需 request_id 反连接+settled 标记防批堵塞）、routing_analytics_source 扩投影需迁移 718。154 复测 llmgw_autoroute_outcome_total 已可按此口径

- **R44 | fallback 槽置信度门在 decider 层统一**：decision.go 对 fallback 结果 < effectiveLLMThreshold() 保留 heuristic winner（钉桩 TestDecide_FallbackBelowThreshold_KeepsHeuristic / TestDecide_FallbackError_KeepsHeuristic）。新占位者不得在 classifier 层把低置信当 error 返回——那会被误计熔断失败打开熔断；breaker 拒绝路径 gauge 报真实连击（breakerOpenConsecutive）非阈值常量。

## 5. 子代理派发提示词

```text
你是 D11（auto 模型全量实现）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D11-auto-model.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内 auto 链路改动的闭环完整性与终态 outcome 回填覆盖。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R45 回注（2026-09-19，Jev 面独立复核全过 + clamp）
- R44 三修复（decider 层置信度门/gauge 真实连击/明文+双配置 Warn）独立复核全部成立；门的位置定式再确认：**classifier 层返 error 会被误计熔断失败，置信度门必须落 decider 层**。
- `thresholds.llm_confidence` tuning 现已 clamp (0,1]（超范围拒绝）：此前无校验时 >0.85 的 tuning 会每次白付 fallback 外呼（LLM 槽恒 0.85 必被门拒）。tuning 键新增时沿用"设值即校验"定式。

### R75 回注（2026-09-28，fail-open 观测 + 坍缩真场景钉桩）
- fail-open 中性化分支新增 `autoroute_task_vocabulary_absent_total{task}`——此前词表被误清空在指标面与正常路由不可区分；部署后核对出数。
- 「词表在场 + winner<30 → 坍缩」真场景首次有钉桩（TestRecommendV2_VocabularyPresentUnderThresholdCollapses：long_context 5 元单命中 20<30 + hotCanonicals 缓存注入）；原 DiscriminatingLowMatchStillFallsBack 名称与断言相反已改名。ANY 语义下坍缩分支仅 long_context 类 5 元 required 可达，其余任务单命中即 ≥30——扩展 required 列表时须同步补坍缩用例。
- 登记：P1/P3 语言段与 P2 词表不同构（django 族 11 名）；无空格变体（写一个python快速排序）待实测钉桩。**两项均已在 R76 收口，见下**。
- **L-4 收口（分类层词表同构）**：M-4/M-8 的 `\s*` 与 P3 拆分在 R73 已落地，残留仅是 P2 后期长出的框架族（django/flask/spring/gin/echo/flutter/nextjs/nuxt/tailwind/html/css/powershell）从未同步到 P1 与 P3b——「写一个 <框架> <编程对象>」三处 pattern 全不命中（实测 14 例中 12 例落 chat 0.10，含零空格形「写一个Django中间件」）。修法：抽 `codeLangVocabulary` 常量供 P1/P2/P3a/P3b 共用，不再手抄四份；新增两条回归（14 例正例 + 4 例逐案误伤边界：Spring Cloud 架构方案→planning、React 年度规划→planning、HTML 邮件文案→creative、Gin 路由压测报告→code）。变异检验：抽掉框架族两条回归全红（12 条断言失败）。误伤面用 20 条对抗样本实测零新增误伤，240 例套件仍 1.0/1.0/100。**教训：「词表与 X 共用」的注释不是证据，词表必须由单一常量生成**。
- 新登记观察（非本轮引入，未修）：带框架词的非代码请求仍落 chat 0.10——「帮我写一个 NextJS 项目的技术选型文档」「写一个 Django 项目的迁移计划」「写一个 Flutter 团队周报」；planning 通道缺「选型/迁移/周报」类触发词，属关键词层词表缺口，宜与选型层词表补齐同批评估。**其中前两条已在下一条收口**。
- **planning artifact 规则收口（同轮，接上条观察）**：新增 `hasPlanArtifactSignal`——判据从「动词+目标」扩到「规划交付物（计划/方案/规划/路线图/排期/里程碑/选型）+ 领域限定词（技术/架构/数据/…）」，不需要动词。修复前实测 4/4 落 chat 0.10。对抗样本实测抓到 2 处真实误伤（「把这个项目的计划删掉」「帮我看一下技术方案」），据此再加 `isRetroactivePlanTalk` 陈述守卫：回溯/删除/查看类语境且**无规范规划动词**时否决全部 planning 路径——「无动词」这个合取是关键，否则会误伤「已经完成调研，请制定技术选型方案」这类真规划。周报类文档任务经核实**不该**进 planning（无文档任务类型），只钉「不得为 code」不钉标签。双变异检验：摘掉 artifact 规则红 4 条、摘掉陈述守卫红 3 条。
- **选型层门禁落地（owner 拍板项已决）**：坍缩率独立基线 `testdata/e2e_baseline.json`（max_collapse_rate=0.05），**只有本轮真的合并了 E2E 报告才判阈**；无 E2E 报告是 skip——既不算通过也不算失败。阈值下界强制 >0：`fallback_used` 也会因候选池合法为空而触发，零阈值会把健康态判成失败。编译产物实测退出码：坍缩 50% → 1、健康 0% → 0、无效基线 → 2、纯离线 → 0。回归 4 条（坍缩必红 / 健康必过 / 无 E2E 必须 skip 且明说 / 阈值语义校验）+ 随仓基线文件可用性校验。**教训：把阈值塞进算不出它的基线 = 装了一个永不触发的门**。
- 仍未开放：P2 TierSelector 未接线（G1 堵点）；选型层词表补齐（约 870 个 canonical 需标注）是数据/运营活；E2E 真实基线需 inference key 才有第一份「本机实测」数据，当前随仓 0/240 沿用 2026-09-28 已归档实测值。
- **D11-#7 收口（md 报告盲点）**：`reports/auto-testbench.md` 此前只有 pass/failures，坍缩率只落在 console 与 JSON——人读的主产物看不到选型层，124/240 坍缩与健康运行的 markdown 完全相同。E2E 节已补上游报错数、选型层（取到决策用例为分母）的坍缩率/单候选池、分类层 decided 准确率与坍缩分任务归因表；helper 抽成 `e2eMarkdownSection` 并加两条回归（坍缩 run / 健康 run）。**教训：度量进了 console+JSON 不等于可观测，人读产物没带上就是盲点**——新增报表指标时三个出口（console / JSON / md）一起改。
- **M-7 根修（同轮，flaky 定位）**：`TestDecideV2_RecommendModelHook_ReordersWinner` 间歇红（本机 4 次同形态跑挂 1 次，包内 `-count=6`×3 全绿，合计 ~2/23）。根因不是测试脆弱而是**生产代码不确定**：hot-top3 不足 3 时按 `for canonID := range byCanonical` 回填候选池，Go map 迭代随机序，同分候选经 `sort.SliceStable` 保留入池顺序，赢家由 map 迭代序决定——同一请求可路由到不同模型，决策 trace 不可复现。修法：回填前取 key 升序排序（中立稳定 tie-break），不改打分与权重。新增 `TestDecideV2_TiedCandidatesPickSameWinnerEveryRun`（24 次迭代同赢家，修复前必红——实测第 3 次迭代即失败）。**教训：flaky 优先怀疑生产代码的 map 遍历，不要先改测试断言**。

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
- **R77 回注（2026-09-28 晚，240 例 E2E 实跑 + code_audit 坍缩根修）**：本机第一次拿到带真实 inference key 的选型层实测。跑法要点：8782 是 build 2296 / SHA `0ae9b5e2`，落后 HEAD 18 个 commit 且**不含** L-4 / planning-artifact / M-7，直接拿它跑等于旧码自证——以 HEAD 源码起旁挂实例（同 DSN、同 Redis、`LLM_GATEWAY_BG_MODE=data-plane` 关写侧后台任务避免与 8782 双跑 reaper），监听 8783。实例含修复的证明用同 key 同 prompt 两端口对照：`写一个Django中间件` 旧 `chat 0.10` / 新 `code 0.65`。
  - 实测：分类 **240/240 = 1.0000**（decided 覆盖率 100%），坍缩 **12/240 = 5.00%**，上游 429 216/240。
  - **坍缩 12 例 100% 是 `code_audit`（12/12）**，其余 10 个 task_type 全部 3 候选。code_audit 唯一候选是 minimax-m3/cred 21 且 `quality=0 reliability=0 tier 空`，对比 code 的 3 个 `tier=primary quality=100 reliability=92`——是降级候选，分类判对了但没有第二第三候选可退。
  - **既有缺口非本轮回归**：3 条 code_audit prompt 在 8782（旧 build）与 8783（HEAD）决策完全一致（`cand=1 / fallback=True / scores=[50]`）。根因仍是能力标注覆盖（线上只发布 cap:long-context/tool-use/function-call/reasoning/vision，无 code/review/security 标签），属数据运营活。
  - **阈值**：先因 0.0522 把 0.05 调到 0.08 棘轮，L-5 根修后坍缩归零，**已调回 0.05**（余量 12/240，第 13 例才红）。
  - **口径订正**：首轮归档误按 249 例报数（13/249=5.22%）——v3 套件 9 行 `#` 注释被误当用例、末 9 例被重复计入。套件头注即 60+40+140=240，正确值 12/240=5.00%，两份归档均已订正。
  - **教训 A**：带决策头的行即使 HTTP 429 也能拿到 task_type 与候选池，分类判决不依赖上游成功——本次一度因查错 JSON 键（`decision` 是嵌套字段）误判「错误行没有决策头」而差点丢弃全部数据。
  - **教训 B**：上游 429 与选型层正交，但暴露**独立集中度风险**——96/126 次上游尝试打到 sensenova 单一 provider，该 provider 一限流就同时打穿所有任务类型的退路；降速实测无效（provider 侧持续限流，非打得太快）。
- **D11-L5 根修（同晚，接上条误判的归因）**：`TaskVocabularyRepresented` 只做子串匹配，于是线上真实存在的**分类标签** `family:codegemma` / `family:codex` / `family:starcoder2` / `version:codestral-latest` / `version:gpt-4o-audio-preview` 全部含子串 `code`，**模型族名叫 codex 就被当成了 `cap:code` 能力词表在场**。连锁反应：中性化（`recommend_v2.go:144`）被跳过 → top 候选 MatchScore 留 0 → 0 < 30 触发 `recommend_v2.go:339` 的 48h 热度坍缩守卫 → code_audit 候选多样性 3→1（`composite=50 quality=0 tier 空`），复现 §2.2 的「坍缩放大限流」。修法：`autoroute/scoring.go` 新增 `isCapabilityTag`，在场判定只认 `cap:` 命名空间 + 无命名空间裸标签（`Candidate.Tags` 历来支持 `["reasoning","code"]` 写法）；`family:`/`version:`/`modality:` 不再能冒充能力证据。**刻意不动 `TaskMatchScore`**——它做排序不做在场判定，子串宽松是有文档有测试的既有行为。
  - 验证：3 条新回归（`autoroute/task_taxonomy_tag_regression_test.go`），修复前全红；变异检验摘掉 `isCapabilityTag` 过滤 → 3 条全红、恢复后全绿；真机 12 例 code_audit 复跑 `cand=1/fallback=True/[50]` → `cand=3/fallback=False/[79.2,76.2,76.2]`，分类 12/12；同基线 240 例前后对比坍缩 **12/240=5.00% → 0/240=0.00%**，分类 240/240 不变。
  - **教训 C**：**「库没有这个词」与「库里有这个词」是两个不同的判定，后者更危险**。ANY 语义的子串匹配一旦撞上一个恰好含该子串的**族名/版本名**，就会把「没有能力分类法」伪装成「有」，让 fail-open 分支整体失效。判定「在场」必须锚在标签的**命名空间**上，不能锚在字符。
  - **教训 D**：**归因到「数据缺口」之前先问「是不是代码口径错了」**。本条先归因为「~870 模型待标注的数据运营活」，被真机 A/B 推翻——真因是判定串口径，与标注量无关。数据活的判断一旦贴上标签就很难再被质疑。
- **P2 TierSelector 启动评估完成（R77，R66 点名优先入口）**：`NewTierSelector` 零生产构造点的断言仍成立（自 R43 起挂账五轮）。但评估结论把「启动前置」改了：**它不是一项独立接线工作，而是 V3 分类器灰度的下游消费者**。全仓两套互不相交词表——V2（`chat`/`code`/`code_audit`/`planning`…，生产 `NewHeuristicClassifierWithTuning` @ main.go:5036 实际发出，240 例 E2E 实证）与 V3（`architecture`/`audit`/`coding`…，`AllTaskTypesV3`，**恰等于 task_type_tier_config 的 10 条种子**）；`NewV3Classifier` 同样零生产构造（其注释自述「保留供实验对照, 下轮清理候选」）。两条接线路都试算过：`enableV3=false` → `SelectTier` 首行返回 tier-b 对所有请求一视同仁（离线分布 tier-a=85/tier-b=105/tier-c=50，爆炸半径远超小步灰度）；`enableV3=true` → V2 类型配置全 miss + `TaskTypeTierMapping` 无 V2 键 → 仍 tier-b。**零收益 + 大爆炸半径**。正确顺序是 V3 分类器先落地（影子 ≥7 天硬前置）。
  - 钉桩 `autoroute/tier_selector_wiring_prereq_test.go` 4 条特征化护栏（tier 配置词表 == V3 词表 / 两套词表零交集 / v3-off 是一刀切 tier-b / v3-on 对 V2 类型仍落兜底）。变异检验：把 V2 的 `TaskCode` 塞进 `AllTaskTypesV3` → 2 条红并给出可操作报错，恢复后全绿。**这 4 条不是缺陷测试而是特征化护栏**——它们在「前置条件改变」时才会红，那就是「评估需重做」的信号。
  - 顺带证实 R43 的 `ensureTaskTypeTierConfig` 确实在工作、非空壳：真库 `task_type_tier_config` 为 V370 形态（BIGINT tenant_id + `min_confidence numeric(3,2) DEFAULT 0.70` + `UNIQUE(task_type, COALESCE(tenant_id,0))`），10 条种子齐全；设计稿点名的 main.go 行号漂移 6–22 行、语义未变；两个 flag 仍未在 `config/` 定义（与设计稿 §89 一致）。
  - **教训 E**：**「零调用点」不等于「排期问题」，先查它下游消费的是什么**。本项被挂了五轮「按计划自身节奏」，但真正的问题是它的输入词表（V3 分类器）根本不在生产——那不是节奏问题，是前置未落地。
- **D11-#8 候选池多样性指标数错对象（R77）**：`candidates_top3` 的三个条目是**三个凭据**、不是三个模型，而 `single_candidate_pools` 数的是条目数。L-5 修复后该指标报 0/240（读作健康），实际 **217/240 = 90.4% 的候选池只有 1 个不同模型**。凭据级也不等于上游级：实测 failover 链 cred 21 → cred 42 同属 provider 14 / `api.minimaxi.com`；429 侧 cred 3/4/25/49 全指 `token.sensenova.cn`（三条 provider 记录 24/24/24/33089 同一 host）。而数据侧够分散——`deepseek-v4-flash` 有 12 provider / **10 个不同上游** / 17 凭据，`glm-5.2` 11/9/14，故 429 占 96/126 上游尝试与「可用上游只有 1 个」无关。修法只补可观测性：`ModelMonotone` + `DistinctModelRate()`，三出口同步，md 明示「单候选池按条目数、条目=凭据」；3 条回归 + 变异检验（摘掉计数 → 2 红）。**是否强制模型/上游多样性是路由策略裁决，本轮不替 owner 定**。
  - **教训 F**：**指标进了三个出口还不够，它得数对东西**。D11-#7 修的是「数字没进 md」，这次是「数字进了但数的是错的对象」——两者都会让人读产物时得到错误的安全感。审计指标要问一句：它数的和我以为它数的是同一个吗？
  - **教训 G**：**「N 个候选可退」是个隐含假设**。N 是凭据数时，N-1 次 failover 可能全落在同一上游甚至同一限流配额上，梯子形同虚设。评估 failover 韧性必须查**上游**维度的分散度，不能停在凭据维度。

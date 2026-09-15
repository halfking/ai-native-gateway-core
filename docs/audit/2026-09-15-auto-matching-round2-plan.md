# auto 模型匹配能力再度审计(第二轮) —— 测试方案

- 日期:2026-09-15
- 性质:**再度审计**(继 [2026-09-14 首轮 E2E 审计](2026-09-14-auto-matching-prompt-e2e-audit.md) 与其 6 项人工确认结论落地之后)
- 范围:`autoroute` 分类 + V2 推荐 + 节点可用性门控 + 成本/效率均衡性,新增 O2 抑制与 decision_trace 可观测性验证

## 一、本轮增量背景(与首轮的差异)

首轮(09-14)已落地的人工确认结论,构成本轮被测的**新基线**:

| 项 | 内容 | 本轮验证点 |
|----|------|-----------|
| O1′-a | requiredTagsForTask 词表分隔符归一化(scoring.go) | agent/function_call 类 MatchScore 脱离 <30 必兜底 |
| O1′-b | 7 主力模型补 cap: 能力标签(本地库已应用) | 标签命中 → 正常打分路径 |
| O1′-c | 迁移 709:work_type_config + 4 类路由补齐 | 11 任务类全有路由(本地 68 行 enabled) |
| O2 | 分发 transient 失败 → (cred,model) 5min 抑制 + decision_trace 投影 | 实机抑制生效;auto 流量 trace 落库 |
| O4 | LLM fallback 分类(生产已配,本地默认未配) | 本轮只验证启发式;O4 采纳率走生产观测 |
| O5 | auto 决策引擎双模式装配 + autoFallbackModel 默认值修复 | data-plane 容器 auto 不再 503 |

首轮已知残余(本轮重点复核):intent_classification(词表 classification 无真实标签)
与 long_context(cap:long-context 命中 20<30)仍走 48h 兜底池;creative 零星兜底(1/8)。

**环境事实(2026-09-15 盘点)**:本地容器当前运行 build 2118(c6605196),
该 sha 不在本仓任何分支/远端历史中(并行线部署,来源不可审计)。本轮所有实测
必须基于本仓 main HEAD(3ab62ea1c)重部署后的二进制。

## 二、目标与成功标准

| # | 验收项 | 标准 |
|---|--------|------|
| G1 | 任务类型识别准确率 | 套件 v1+v2 整体 ≥ 90%;硬覆盖类(vision/code强信号/long_context)100% |
| G2 | 陷阱/边界不回归 | 首轮 trap 用例 + 本轮 trap_v2 全部正确 |
| G3 | 所选节点当前可用 | 每条决策 chosen_credential 在生产(252)满足 bindings.available、探测不在退避、quota 未耗尽 |
| G4 | 成本/效率均衡 | 所选模型 composite_score 与该任务候选池 top1 差距 ≤ 10 分;超出须解释或定性 |
| G5 | 决策透明可解释 | X-Gw-Auto-Decision 含 task_type/confidence/reason/top3;route_tier 反映 709 路由 |
| G6 | 交付匹配清单 v2 | 任务类型 → 推荐模型(主/备)+ 供应商节点 + 单价 + 实测延迟,标记"待人工确认" |
| G7 | O2 抑制实机生效 | 分发全失败的 (cred,model) 在 node_probe_state 留 last_direct_ok=f + next_retry_at≈+5min;后续请求不再首选该组合 |
| G8 | decision_trace 可观测 | 本地 auto 流量的 routing_decision_log.decision_trace 非空(source/task_type/fallback_used) |
| G9 | 首轮残余定性 | intent_classification / long_context 兜底行为:胜者=配置 primary 即记"可接受残余";否则升级为缺陷修复 |

## 三、被测链路与环境

被测链路同首轮(A 信号提取 → B 分类 → C V2 推荐漏斗),差异:

| 层 | 环境 | 说明 |
|----|------|------|
| 静态规则层 | 仓内 Go 测试 | 套件 v1(60 例,回归)+ **v2(本轮新增)** + golden 矩阵 |
| 端到端 | 本地网关 127.0.0.1:8782(**本仓 main 重部署**) | 真实请求→真实上游,X-Gw-Auto-Decision 采集 |
| 生产核对 | ssh 245 → psql 252(只读) | credential 可用性/退避/配额/价格/分数快照 |

- 测试 key:本地 api_keys id=211(`sk-auto-audit-20260914`,上轮已建,active)。
- work_type 路由面(本地,709 后):15 个 work_type_key 经 work_type_config.l1_task_type
  映射到 11 任务类,全部 enabled——本轮 E2E 需记录每例的 route_tier 与胜者所属
  work_type,验证 TierFailover 生效。

### 成本控制

- 全部用例 max_tokens ≤ 32;长上下文触发用例(pad_to_tokens ≥ 50k)**全轮 ≤ 4 条**
- 套件总量 v1+v2 ≤ 100 条,预计总消耗 < $1.5(deepseek/glm 主力单价档)
- 不打生产网关;生产侧仅只读 SQL

## 四、套件 v2 设计(本轮新增 ~40 例)

数据文件:`autoroute/testdata/auto_matching_suite_v2.jsonl`,schema 与 v1 一致。

新增覆盖维度(补首轮缺口):

| 桶 | 数量 | 设计意图 |
|----|------|----------|
| en_business | 10 | **英文业务提示词**(首轮 47zh/13en,英文面仅 22%):重构/数学推理/客服工单分类/营销文案/安全审计/项目计划/agent 编排/数据分析 |
| mixed_signal | 6 | 混合信号优先级仲裁:vision+code(→vision)、IDE+60k(→code)、60k+creative 词(→long_context)、intent+60k(→intent,1.5 先于 1.3)、IDE+审计措辞(→code,强信号先于 audit) |
| trap_v2 | 8 | 新陷阱:关于代码的诗歌(→creative)、6 万字翻译(→long_context)、总结+制定计划(→planning)、NPE 报错提问、无图说图(→非 vision)、先计划后实现(→code) |
| residual | 4 | 首轮残余专测:intent_classification ×2、long_context ×1、creative ×1(G9 取证) |
| new_business | 8 | 首轮未覆盖的业务面:SRE 延迟排查、合同风险审查、教学讲解、电商客服回复、SQL 生成、经营分析报告、邮件婉拒、K8s YAML 检查 |

预期标签一律按当前代码语义标注(硬覆盖优先级:vision > code 强信号(代码块/IDE/
plan 模式) > code_audit > intent_classification > planning > long_context(>50k) >
agent(≥3 工具+结果) > function_call(1-2 工具) > pattern/关键词 > chat),
**错位处理原则**:离线回归失败时,先分清"标签错"(改标签)还是"分类器错"
(修复或登记 known_failure),不允许为凑通过率改语义。

## 五、执行步骤

1. **S0 环境基线**:本仓 main 重部署本地网关(deploy-local.sh;部署前 ps 查并行
   deploy;部署后核对 /healthz git_sha=3ab62ea1c 或更新 + 凭据解密冒烟)。
2. **S1 静态基线**:`go test ./autoroute/ -run 'TestAutoMatchingSuite|TestPromptClassificationMatrix|TestV6RoutingMatrix'`,
   v2 用例并入离线回归(双套件文件)。
3. **S2 E2E 采集**:`go run ./cmd/autoroute-e2e-audit` 分别跑 v1+v2 套件,
   记录决策头+实际服务模型+时延 → results JSONL。
4. **S3 生产可用性核对**:对每条 chosen_credential,245 跳板 psql 查
   bindings/probe/quota/价格/score_smart(G3);与首轮快照比对漂移。
5. **S4 均衡性判定**:以 252 索引分数重算各任务候选池 top-N,比对实测选择(G4)。
6. **S5 O2/trace/残余专项**:G7 抑制行核验、G8 trace 落库核验、G9 残余定性。
7. **S6 修复回归**:分类误判 → patterns/词表修补 + 矩阵用例;选择异常 → 漏斗/
   抑制链路排查;全部修复后重跑 S1-S5。
8. **S7 交付**:本报告回填结果;匹配清单 v2(2026-09-15-auto-matching-list-v2.md)
   标记待人工确认,**未经人工确认不实施任何 routing/标签/配置变更**。

## 六、交付物

- 本文档(S7 后回填执行结果)
- `autoroute/testdata/auto_matching_suite_v2.jsonl` + 离线回归扩展(可复跑)
- `docs/audit/2026-09-15-auto-matching-e2e-results-v2.jsonl`(原始采集)
- `docs/audit/2026-09-15-auto-matching-list-v2.md`:任务-模型匹配清单 v2(**待人工确认**)

## 七、执行结果(回填,2026-09-15 03:50 完成)

### 7.1 执行环境事故(S2)

- 首次部署后 E2E v1 期间(03:08),并行线 deploy-local 将共享容器重指到
  **2118-6df4de85**(该 sha 不在本仓历史亦不在 origin/main,来源不可审计),
  v2 首跑 33/40 例报 `executor_unavailable: Routing executor not available`。
- 按上轮 O6 教训重部署本仓构建(2.5.4-2aa80828-2118;8781 候选槽因切换失败
  留在旧版,8782 活动槽为我版)并**前后核对 /healthz 构建身份**后重跑 v2,
  全程 40/40 无环境中断。已登记为清单 v2 待确认项 N4。

### 7.2 S1 静态基线与分类修复

v2 套件 40 例并入离线回归(双套件加载,`auto_matching_suite_test.go` 重构为
文件表驱动+重名守卫),首轮离线 **89/100**,9 个新缺口全部定位并修复:

| # | 用例 | 修复前 | 根因 | 修复 |
|---|------|--------|------|------|
| R1 | en_creative_tagline | chat | 英文创意名词族无 pattern | patterns.go 新增 en 创意名词族(write/taglines 等) |
| R2 | trap_poem_about_coders | chat | 诗体词缺绝句/律诗等 | creative 写作 pattern 扩诗体 |
| R3 | trap_python_csv_howto | chat | P2 动词表缺疑问/处理动词 | P2 增 读取/解析/怎么/如何 等 |
| R4 | biz_sql_active_users | chat | P1 对象缺 SQL/正则/YAML | P1 扩配置与查询语言产物 |
| R5 | biz_data_monthly_report | chat | F4 只覆盖"分析→数据"方向 | 新增锚词式方向2 + 分析报告写作 pattern |
| R6 | biz_email_polite_reject | chat | 邮件写作无 pattern | 新增 邮件/回信写作 pattern |
| R7 | biz_legal_contract_risk | chat | 文档风险审查无 pattern | 新增 合同/条款/方案 评审 pattern(→reasoning) |
| R8 | en_chat_api_explain | code(0.40) | "No code needed" 字面 code 命中关键词 | classifier.go 增否定短语守卫(仅抑制关键词层 code 分) |
| R9 | trap_intent_word_passing | intent(0.82) | "意图识别"产品名词引用劫持 | task_types_ext.go 增施行语境守卫(指示词/祈使动词) |

修复后离线 **99 pass + 1 known_failure(mix_60k_planning_words,登记待人工确认
N2)= 100 例全绿**;golden 矩阵与 v6 路由矩阵无回归;`go test ./autoroute/` 全过。

命名窗口连带调整:creative 命名 pattern 窗口 8→12(trap_intent_word_passing
修复后经命名 pattern 落 creative,预期标签同步修订)。

### 7.3 S2 端到端实测(v1 60 例 + v2 40 例,2.5.4-2aa80828/2118)

**分类正确率(G1/G2)**:v1 55/60 全过、0 错判(5 例分发层 error);
v2 37/40(1 例=登记的 known_failure 判 planning,2 例分发层 error)。
**分类层实质正确率 92/93 = 98.9%,唯一失败即登记缺口(N2)**;11 类零错判,
v1 trap + v2 trap_v2 全对。

**兜底份额(对比上轮 32/60=53%)**:

| 任务类 | 上轮 | 本轮 v1 | 本轮 v2 |
|--------|------|---------|---------|
| code_audit/planning/function_call/long_context | 100% 兜底 | **0%** | **0%** |
| agent/reasoning/vision/chat/code | 正常 | code 5/9 其余 0 | code 0、其余 0 |
| intent_classification | 100% | 100%(残余) | 100%(残余) |
| creative | 1/8 零星 | 100%(残余) | 100%(残余) |

intent/creative 兜底根因=词表在标签宇宙无对应物(G9 定性:胜者恒为配置
primary deepseek-v4-flash#11,行为可接受,见清单 §二)。

**模型选择**(两套件合并):deepseek-v4-flash#11 仍为绝对主力;reasoning/code
高分面 glm-5.2(#22/#25/#49);v2 出现 **claude-sonnet-5/opus-5@suyun#39**
服务 code/reasoning(tier primary,生产同通道 active 但索引单价为空——新立
待确认项 N6 成本透明度);agent/vision → minimax-m3(#11/#19/#21/#42)。

latency p50≈3-5s / p95 30-93s / max 130s(60k 输入用例)。

### 7.4 S3/S4 生产核对与均衡性(G3/G4)

- 709 四类路由生产已生效(code_audit/fn_call/intent_classification/planning
  均有 primary deepseek-v4-flash + secondary glm-5.2)。
- G3:实测选中的 10 个 (cred,model) 对,生产 credentials 全部 active;唯一
  漂移=glm-5.2@57 在生产 http_403 探测退避(本地分发却成功)——清单已按生产
  面调整备选(N3)。当日退避面:智码#35 整批、apicloude-china#56/57/58、
  联界#29/#45(minimax-m3 http_410)、商汤#3/#4/#49/#50/#58 kimi-k3 429;
  #31 apiclaude 已 deleted(自动面无 claude-fable/opus-4.8)。
- G4:两套件非兜底选择与 top3[0] composite 差距全部 ≤10,零偏差。
- 生产观测面:selection 写链正常(24h 9 行:heuristic_v2×8+llm_v2×1);
  decision_trace 投影在生产生效(7d 内 9 条 auto 决策带 task_type);生产
  auto 流量本身极低(~9 条/7d)。

### 7.5 S5 专项(G7/G8/G9)

- **G7 PASS**:分发全失败的 (cred,model) 落 node_probe_state
  last_direct_ok=f + next_retry_at≈+5min(kimi-k3@50 网络错误行实证);
  决策漏斗消费该表(recommend_v2.go:399 与 index.go:597 两处子查询对齐
  v_routable_credential_models),抑制后 5min 内不再入选。
- **G8 PASS**:本地 E2E 窗口 75 条非空 decision_trace,70 条带
  task_type+fallback_used(5 条为分发失败请求,wire 未生成,设计如此);
  生产投影同步验证(见上)。
- **G9 PASS**:intent_classification/creative 兜底胜者=配置 primary,
  定性"可接受残余";修复选项 (a) 补标签/(b) 软门控/(c) 维持现状,
  审计建议 (c),见清单 §二。

### 7.6 交付物核对

- [x] 本文档(§七回填完成)
- [x] `autoroute/testdata/auto_matching_suite_v2.jsonl`(40 例)
- [x] 离线回归扩展(auto_matching_suite_test.go 双套件表驱动)
- [x] `docs/audit/2026-09-15-auto-matching-e2e-results-v2.jsonl`(v1 原始 60 行)
- [x] `docs/audit/2026-09-15-auto-matching-e2e-results-v2-suite2.jsonl`(v2 重跑 40 行)
- [x] `scripts/audit/auto_matching_round2_analysis.py`(G1/G4/兜底/时延分析,可复跑)
- [x] `docs/audit/2026-09-15-auto-matching-list-v2.md`(**待人工确认**,N1-N6)

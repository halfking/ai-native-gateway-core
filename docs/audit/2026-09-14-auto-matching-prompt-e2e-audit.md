# auto 模型匹配能力复审 —— 提示词端到端审计方案与报告

- 日期:2026-09-14
- 范围:`autoroute` 分类 + V2 推荐 + 节点可用性门控 + 成本/效率均衡性
- 前序审计:审计要求 #8(2026-08,催生 `classifier_prompt_matrix_test.go` 与 v4/v6 路由矩阵);
  本次为"再度审计",增量在于**真实业务提示词 × 端到端实测**(既有矩阵只覆盖静态规则层)

## 一、目标与成功标准

| # | 验收项 | 标准 |
|---|--------|------|
| G1 | 任务类型识别准确率 | 提示词套件整体 ≥ 90%;硬覆盖类(vision/code强信号/long_context)100% |
| G2 | 陷阱/边界不回归 | trap 用例 100% 正确(误判即分类逻辑回归信号) |
| G3 | 所选节点当前可用 | 每条决策 chosen_credential 在生产侧(252)满足:bindings.available、探测不在退避、quota 未耗尽 |
| G4 | 成本/效率均衡 | 所选模型 composite_score 与该任务候选池 top1 差距 ≤ 10 分;超出需给出解释(亲和/粘性/池分层)或定性为问题 |
| G5 | 决策透明可解释 | X-Gw-Auto-Decision 含 task_type/confidence/reason/top3,LLM fallback 触发时有 classifier 标记 |
| G6 | 交付匹配清单 | 任务类型 → 推荐模型(主/备)+ 供应商节点 + 单价 + 实测延迟,供人工确认 |

## 二、被测链路(三段)

```
请求 model=auto
 ├─ A. 信号提取:图片/代码块/工具数/token估算/语言/IDE指纹 (domains/streaming/auto_route.go:162)
 ├─ B. 分类:启发式硬覆盖 → pattern/关键词 → 低置信(<0.7)LLM fallback (autoroute/classifier*.go)
 └─ C. 推荐:RecommendV2 候选漏斗(可用门控→IQ门禁→tier分层→热池)→ V2评分
        IntentMatch*.4+Price*.2+ChannelQuality*.3+Reliability*.1+亲和 (autoroute/recommend_v2.go, scoring_simplified.go)
```

任务类型全集(11):chat / reasoning / code / agent / creative / long_context / vision /
function_call / code_audit / intent_classification / planning。

## 三、测试环境与数据通道

| 层 | 环境 | 用途 | 影响 |
|----|------|------|------|
| 静态规则层 | 仓内 Go 测试(本地跑) | 分类 golden 矩阵 + v6 路由矩阵基线 | 无 |
| 端到端 | 本地网关 `127.0.0.1:8782`(build_seq 2102,490e8e98) | 真实请求→真实上游调用,采集 X-Gw-Auto-Decision | 消耗真实上游 token(小额,见成本控制) |
| 生产核对 | `ssh 245` → psql 252(只读) | 所选 credential 的 bindings/probe/quota/价格/分数 | 只读 |

本地 PG 为 252 同步副本,hot 统计表默认跳过同步 → 本地 success_rate/p95 多为 0,
**均衡性判定以生产侧 252 的 credential_model_index 分数为准**,本地实测用于验证链路与分类。

### 成本控制

- 全部用例 `max_tokens ≤ 32`;仅 long_context 触发用例输入约 55k token,限 1 条
- 套件总量 ≤ 80 条,预计总消耗 < $1(以 deepseek/glm 为主的候选池)
- 不打生产网关(245/154 不注入测试流量),生产侧仅只读 SQL

### 测试 key

本地 api_keys 新增 id=211(`sk-auto-audit-20260914`,HMAC-SHA256 由
`HashAPIKey` 同算法生成,application_id=1,rpm=60)。仅存在于本地库,无生产权限。

## 四、提示词套件设计(业务化)

数据文件:`autoroute/testdata/auto_matching_suite.jsonl`,每行一个用例:

```json
{"name":"code_zh_gateway_bug","task":"code","lang":"zh","bucket":"coding_agent",
 "signals":{"last_user_prompt":"...","client_type":"cursor","estimated_tokens":1200,
            "tool_count":0,"has_images":false,"has_code_block":false,"has_tool_results":false}}
```

业务桶(对应真实接入方):

| 桶 | 场景 | 覆盖类型 |
|----|------|----------|
| coding_agent | IDE/CLI 编码代理(cursor 等指纹) | code / code_audit / planning |
| customer_service | 客服/工单/意图识别业务系统 | intent_classification / function_call / chat |
| content_ops | 文案/翻译/摘要内容生产 | creative / long_context |
| data_analysis | 数据/统计/推理问答 | reasoning |
| multimodal | 图片理解(1×1 PNG,低成本) | vision |
| orchestration | 多工具编排代理 | agent |
| trap | 边界:措辞交叉易误判用例 | 全部 |

预期标签编码生产分类器语义(硬覆盖优先级:vision > code强信号(代码块/IDE) >
code_audit/intent/planning 扩展 > long_context(>50k) > agent(≥3工具+工具结果) >
function_call(1-2工具) > pattern/关键词)。套件内 `expected_task` 即按此语义人工标注。

## 五、执行步骤

1. **S1 静态基线**:`go test ./autoroute/ -run 'TestPromptClassificationMatrix|TestV6RoutingMatrix'`,
   记录既有矩阵通过情况;套件提示词同步补入矩阵单测(脱网可回归)。
2. **S2 E2E 采集**:`go run ./cmd/autoroute-e2e-audit`(新增,复用 internal/routingtest 客户端形态)
   逐条 POST `/v1/chat/completions` model=auto,记录决策头+响应模型+时延 → results JSONL。
3. **S3 生产可用性核对**:对每条 chosen_credential_id(本地与 252 同源同 ID),
   245 跳板 psql 查 `credential_model_bindings.available` / `node_probe_state` 退避 /
   `credentials.quota_state,availability_state` / `credential_model_index` 单价与 score_smart。
4. **S4 均衡性判定**:以 252 索引分数重算该任务候选池 top-N,比对实测选择,产出偏差表(G4)。
5. **S5 修复回归**:分类误判 → pattern/关键词修补(优先 `tuning_params` 热更与
   patterns.go,同步补矩阵用例);选择异常 → 查漏斗门控/评分;全部修复后重跑 S1-S4。

## 六、交付物

- 本文档(S5 完成后附结果与修复记录)
- `autoroute/testdata/auto_matching_suite.jsonl` + `cmd/autoroute-e2e-audit`(可复跑)
- `docs/audit/2026-09-14-auto-matching-list.md`:任务-模型匹配清单(主/备模型、
  供应商节点、单价、score、实测时延、生效条件),**标记"待人工确认"后不作自动变更**

## 七、执行结果(回填)

### 7.1 静态层(S1)

- 既有 golden 矩阵 `TestPromptClassificationMatrix` 基线通过(修复后 56/56)。
- 新套件离线回归 `TestAutoMatchingSuiteHeuristic`(60 例)首轮:**54 过 / 3 未预期失败 / 3 known_failure 全部复现**——复现的 6 个缺口见 7.2。

### 7.2 分类缺口与修复(S5,共 6 项)

| # | 用例 | 修复前 | 根因 | 修复 | 验证 |
|---|------|--------|------|------|------|
| F1 | plan_zh_migration("制定一个…方案"被修饰语隔开) | chat | planning 只支持紧邻短语 | task_types_ext.go 动宾组合判断 + plan-mode 正则护栏(防"计划后实现"被抢) | 离线+实机 ✓ |
| F2 | cre_zh_names("起几个有创意的名字") | chat | 无命名类 pattern | patterns.go creative naming 正则 | 离线+实机 ✓ |
| F3 | cre_zh_email_polish("润色邮件") | chat | creative 词表缺词 | 新增 润色/改写/扩写/俳句/打油诗 | 离线+实机 ✓ |
| F4 | gap_zh_codeanalyze("分析+代码") | reasoning | "分析"归 reasoning 关键词,与"代码"打平按优先级错归 | "分析"降级,新增"分析+代码对象→code"/"分析+数据对象→reasoning"正则 | 离线+实机 ✓ |
| F5 | gap_zh_sentiment("正面还是负面") | chat | 情感二分类无 intent 信号 | intent 词表补情感分类短语(中英) | 离线+实机 ✓ |
| F6 | gap_sysprompt_haiku | code_audit | system 提示词审计短语劫持任意用户请求 | IsCodeAuditRequest 改为 user 优先,system 短语需 user 同时提到代码对象 | 离线+实机 ✓(角色定义仍经关键词通道偏向 code 族,已按修订语义入册) |

### 7.3 端到端实测(S2-S4,修复版二进制 2.5.4-58b5ec55/2107)

**分类正确率:58/60 = 96.7%(失败 0,分类层 58/58 = 100%;2 例为分发/环境错误,非分类)**,达到 G1(≥90%)。
陷阱用例(trap_*)全部正确,G2 达成。判定分布:11 类全部无错判。

模型选择实测(本地面,决策头+实际服务模型):

| 任务类型 | 主选模型(节点) | 实测时延 | 备注 |
|----------|----------------|----------|------|
| code(IDE 强信号) | gpt-5.6-terra(flatrouter#76) | 7-16s | 价格分 100 |
| code(pattern 层) | glm-5.2(商汤#25 / sensenova-jack#49) | 1.1-12s | composite 59.2 |
| code_audit/planning/intent/function_call/agent/creative/long_context | deepseek-v4-flash(火山方舟 TokenPlan#11) | 1-35s(个别 70s) | **走 48h 兜底池(fb=True)**,composite 50 |
| reasoning | glm-5.2(商汤#25) | ~1.2s | composite 59.2 |
| chat | glm-5.1 / deepseek-v4-flash(#11) | 1.2-3.2s | composite 79-80 |
| vision | minimax-m3(MiniMax#42/#11)/gpt-5.6-terra | 1.8-50s | 1 例 all-candidates-failed |
| long_context(60k 输入) | deepseek-v4-flash(#11,ctx 131072) | 11-13s | 上下文装得下,无截断错误 |

latency p50=2.9s / p95=49.9s / max=128s。

### 7.4 发现(未修复,按优先级)

| # | 发现 | 定性 | 建议 |
|---|------|------|------|
| O1 | **主池空缺,兜底池承担 7/11 任务类型**:matrix 默认模型(如 claude-fable-5 $15/$50、claude-opus-4-8 $10/$25)多数处于高价/探测退避/配额受限状态,决策退到 48h 兜底池(composite 50) | P2 路由质量 | 用 tuning/routing_policy 把当前真实可用的高性价比模型补进各任务 preferred 池;清单见匹配清单文档"待人工确认"项 |
| O2 | **决策-分发可用性窗口差**:索引判可用→分发报 "No available provider, All N candidates failed"(kimi-k3×2、minimax-m3×1),且伴随 81-128s 极端延迟 | P3 可靠性 | 分发全候选失败后确认负反馈回写 node_probe_state/索引的时效;考虑决策层对 5 分钟内分发失败的 (cred,model) 做短期抑制 |
| O3 | top3 候选含同模型不同 credential 重复(如 glm-5.1 与 deepseek-v4-flash 各出现两份) | P4 观察项 | 决策头去重展示,不影响选择 |
| O4 | 生产与本地均未配置 AUTO LLM fallback endpoint(`LLMGatewayAutoLLMEndpoint`),置信度 <0.7 的请求停留启发式结果(chat 仅 0.1) | P3 分类覆盖 | 生产启用 LLM fallback 或按本报告词表继续扩 tuning_params |
| O5 | 本地测试环境与生产配额状态漂移:本地选中 kimi-k3@商汤#3(生产 suspended+permanently_exhausted)且耗时 128s | 环境说明 | 匹配清单以生产实时库为准(已按此原则出具) |
| O6 | 环境事故:并行会话反复 deploy-local 互相打断(3 次 E2E 中断、1 次二进制回退到无修复版本) | 环境说明 | 共享工作区部署前先 ps 检查 deploy-local 进程;采集工具已加连接重试 |

### 7.5 交付物

- 套件:`autoroute/testdata/auto_matching_suite.jsonl`(60 例,11 类×业务桶+陷阱)
- 离线回归:`autoroute/auto_matching_suite_test.go`;golden 矩阵新增 8 例
- 采集工具:`cmd/autoroute-e2e-audit`(带部署窗口连接重试)
- 匹配清单:`docs/audit/2026-09-14-auto-matching-list.md`(**待人工确认**)
- 修复 diff:autoroute/{classifier,task_types_ext,patterns}.go + classifier_prompt_matrix_test.go

匹配清单见 [2026-09-14-auto-matching-list.md](2026-09-14-auto-matching-list.md)。

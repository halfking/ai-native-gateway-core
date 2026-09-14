# auto 匹配清单 —— 任务类型 → 模型/供应商节点(2026-09-14)

> **状态:6 项已人工确认并实施(2026-09-14 晚,§三结论栏/§五四轮实施记录)。**
> 本清单由 2026-09-14 匹配能力复审出具
> (方法与数据见 [2026-09-14-auto-matching-prompt-e2e-audit.md](2026-09-14-auto-matching-prompt-e2e-audit.md)),
> 三轮机制定稿见 §一;§三为 6 项决策原提请(含审计建议与人工结论)。
> 二次复核推翻了原 O1 的机制描述与杠杆选择,详见 §三引言与 §五复验记录(二轮)。
>
> 数据来源:
> - 实测:本地网关 2.5.4-58b5ec55/2107 端到端 60 例(2026-09-14 05:00-05:20)
> - 可用性:252 生产库(经 245 只读 psql)credential 状态 + node_probe_state 探测退避(2026-09-14 05:00 快照)
> - 单价:252 credential_model_index 最新桶(未标注 = 索引无价,以通道计费计划为准)

## 一、任务类型 → 推荐模型清单

| 任务类型 | 主选(实测) | 节点(供应商) | 参考单价 ¥/1M in/out | 生产可用 | 实测时延 | 备选 |
|----------|--------------|----------------|----------------------|----------|----------|------|
| chat | glm-5.1 | 火山方舟 TokenPlan#11 / 智谱AI#22 | 0.1 / 0.1 | ✓ | 1.2-3.2s | deepseek-v4-flash(#11) |
| reasoning | glm-5.2 | 商汤 SenseNova#25 | 索引无价(通道套餐) | ✓ | ~1.2s | claude-fable-5(apiclaude#31,15/50,高质量高成本) |
| code(IDE/强信号) | gpt-5.6-terra | flatrouter#76 | 2.5/15(apigpt 同价面) | ⚠ 本地 cooling,上线前复检 | 7-16s | glm-5.2(#25/#49) |
| code(pattern 层/中文) | glm-5.2 | 商汤#25 / sensenova-jack#49 | 无价(套餐) | ✓ | 1.1-12s | deepseek-v4-flash(#11) |
| code_audit | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 4.6-11.8s | claude-fable-5(#31,15/50) |
| planning | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 5-70s | claude-opus-4-8(#31,10/25) |
| creative | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 1.2-6s | glm-5.1(#11/#22,0.1/0.1) |
| intent_classification | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 1-2.3s | glm-5.2(#25) |
| function_call | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 1.5-7.4s | minimax-m2.7(#11,0.1/0.1) |
| agent | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 2-3.9s | minimax-m3(#42) |
| vision | minimax-m3 | MiniMax#42 / 火山方舟#11 | 无价 | ✓(NVIDIA NIM 通道同模型在探测退避中) | 1.8-2.1s | gpt-5.6-terra(#76,multimodal) |
| long_context(>50k) | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓(ctx 131072) | 11-13s | glm-5-2-260617(#11,ctx 2M,0.1/0.1) |

总口径:deepseek-v4-flash@火山方舟 TokenPlan 承担了 34/60 的实测选择,是当前
成本效率最优的主力节点(套餐计费、单价档最低、成功响应 p50≈2.9s);glm 系为
各任务备选;claude 系为高质量高成本备选(仅在确认任务需要时人工选用)。

**兜底池口径(2026-09-14 审计补登)**:上表"主选(实测)"有 32/60 例的
X-Gw-Auto-Decision 标记 `fallback_used=true`——agent / code_audit / creative /
function_call / intent_classification / long_context / planning 这 **7/11 任务类
在本地库的 matrix preferred 池选不出候选,胜者实际来自 48h 热度兜底池
(composite 恒 50),未经过任务偏好的正常打分**;仅 chat / code / reasoning /
vision 4 类走正常打分。故"deepseek 承担主力"对这 7 类是兜底池胜者而非
matrix 首选,O1 的紧迫性以此量化为准。

**兜底机制修正(2026-09-14 三轮审计定稿;推翻二轮"标签荒"单因表述)**:上段的
"matrix preferred 池选不出候选"表述不准确,完整机制链路(代码+实机双核实):
1. 生产/本地默认走 **V2 决策漏斗**(UseChannelQualityRouting 默认开启,
   autoroute/feature_flags.go:134;实机 X-Gw-Auto-Decision 的
   enabled_features 仅 ["channel_quality_routing"])。
2. V2 实际消费的任务→模型配置是 **work_type_model_route + work_type_config**
   (WorkTypeRouteStore,decision_v2.go:203-255):有路由的类走**全量候选池**
   (FullCandidateSet)并在打分后按 tier 策略过滤出配置 primary;无路由的类
   只能用 48h 热榜 Top3 canonical 组池。**task_default_routing 在 V2 不参与**
   (decision_v2.go:230;生产该表 102 行 'v6.1 default' 全部惰性,本地副本 0 行)。
3. 兜底触发点:打分排序后**若榜首候选 MatchScore<30,整个推荐被替换为 48h
   兜底池胜者**(recommend_v2.go:272,composite 恒 50);池空另有 :117/:161
   同源兜底。
4. MatchScore=TaskMatchScore×100(scoring_simplified.go:205),即候选 tags 对
   requiredTagsForTask 词表(scoring.go:395)的子串命中率。**词表与库内标签
   系统性分隔符错配**:词表用下划线(tool_use/function_call/long_context),
   库内能力标签用连字符(cap:tool-use/cap:function-call/cap:long-context),
   子串匹配永不命中;当前主力新模型(deepseek-v4-flash/glm-5.2/minimax-m3/
   kimi-k3 等)仅 family 标签,带能力标签的多为旧世代模型(o1/o3 系、
   claude-opus-4.x、gpt-4o 系、gemini-2.5、deepseek-r1 等;生产 83/1022 个
   active canonical 带 cap: 能力标签)。
5. **7/4 分裂精确定因**(本地 12/12 确定性复现):code_audit/
   intent_classification/planning 本地无 work_type 路由 → 热榜3池全 match=0
   → 必兜底;agent/creative/function_call/long_context 有路由但词表错配/
   无对应标签 → 0 命中 → 兜底;chat 无必配词表恒 50;code/reasoning/vision
   有标签命中的旧世代候选(deepseek-r1 的 cap:reasoning、codegemma/
   starcoder2 的 family:*code*、gpt-4o 系的 cap:vision+multimodal)以
   33~100 分压住排序首位 → **门控不触发**,再被 tier 策略过滤成配置的
   primary —— 门控被掩盖,fallback_used=false(planning 本地虽有 deepseek-r1
   可用,但无路由进不了全量池,仍必兜底)。
6. 生产 work_type 路由覆盖(2026-09-14 只读复核):enabled 路由仅覆盖
   agent/chat/code/creative/long_context/reasoning/vision 7 类,
   **code_audit/function_call/intent_classification/planning 无路由**——
   这 4 类在生产同样必走兜底(比本地 E2E 还多 function_call 一类)。
7. 结论:O1 的正确杠杆 = **词表归一化(代码) + 主力模型补能力标签(数据)
   + work_type_model_route 补齐无路由类(配置)**,而非 task_default_routing。

## 二、当前不可用面(2026-09-14 05:00 生产快照,节选)

- 整 credential 不可用(配额/认证):#3 商汤 suspended+permanently_exhausted、
  #13/#40 permanently_exhausted、#23 unreachable、#36 suspended、
  #4/#25(部分模型)/#61 permanently_exhausted、#34 periodic_exhausted 等
- (credential, model) 探测退避中:35 智码整批(glm-5.2/gpt-5.6-luna/claude-fable-5/
  claude-opus-4-8/glm-5.1)、31 apiclaude claude-sonnet-4-5、56 商汤 deepseek-v4-pro/flash、
  8/18 NVIDIA NIM minimaxai/minimax-m3、2 apigpt gpt-5.2/gpt-image 系 等
- 结论:**claude-fable-5 / claude-opus-4-8 主力通道当前不可选**,v6 matrix 中
  reasoning/code_audit/planning 的默认首选实际不可达——这是主池空缺(O1)的直接原因。

**生产面二次复核增量(2026-09-14 11:18-11:23,245 只读 psql)**:
- #31 apiclaude 整 credential `auth_failed + permanently_exhausted`——claude 系
  不可达在 credential 级坐实(其 node_probe_state 行干净,是 credential 挂不是探测挂)。
- O1 候选可用:#25/#49 deepseek-v4-flash、glm-5.2 与 #22 glm-5.1 探测行当日
  0 连败;#11 deepseek-v4-flash 仅余 2026-08-31 http_429 陈旧行(7 连败但未
  paused、重试窗口早已过期),#11 glm-5.1/minimax-m3 干净。
- **8/18 NVIDIA NIM minimax-m3 当前无探测退避行**(本节 05:00 快照已过时)。
- **生产无 credential #76(flatrouter)**——§一 code(IDE) 行的 #76 主选在生产库
  不存在,该行仅反映本地副本,引用时须注意。
- 生产 `task_default_routing` 102 行(全部 'v6.1 default',7 类 primary 已是
  deepseek-v4-flash/glm-5.2 等),本地副本 0 行;`task_type_tier_config` 生产
  0 行/本地 10 行(另一套 AUTO_MODEL V3 机制,与 V2 漏斗无关)。
- 245 `/etc/llm-gateway-go/env` 与 `/opt/llm-gateway-go/.env` 均无 AUTO_* 开关
  覆写 → 生产跑代码默认 flags。
- **生产观测缺口**:routing_decision_log_hot 近 5000 条采样 `decision_trace`
  全为空对象 `{}`——fallback_used/task_type 在生产日志不可观测,只能靠逐请求
  X-Gw-Auto-Decision 响应头。任何修复的效果验证都需先补此观测。

## 三、待人工确认事项(2026-09-14 下午按修正后机制重述)

> 原第 1 项"写入 task_default_routing / routing matrix"的杠杆在生产 V2 路径下
> **机械上无效**(见 §一机制修正与 §五二轮记录),故 6 项全部按下述事实重述。
> 每项给出审计建议;**结论栏未经人工填写前,任何对应变更不得实施**。

1. **[O1′ 兜底类回正常打分路径]** 是否实施"让兜底类回到正常打分路径"?
   经三轮审计,杠杆为三件套(可独立确认,机制见 §一定稿):
   - (a) **词表归一化(代码,小改动)**:requiredTagsForTask 词表与库内标签
     的分隔符归一(tool-use↔tool_use、function-call↔function_call、
     long-context↔long_context;autoroute/scoring.go:395 + containsFold)。
     仅此一项即可让 agent/function_call 类靠既有 cap: 标签拿到 ≥30 分摆脱
     必兜底;需配套单测。
   - (b) **主力模型补能力标签(数据)**:按厂商公开能力给 deepseek-v4-flash/
     glm-5.2/glm-5.1/minimax-m3/kimi-k3 等补 cap: 系列标签(工具调用/推理/
     长上下文)。约束:按真实能力补,不虚标过门控;tags_locked=false 可直接
     更新。
   - (c) **work_type_model_route 补齐(配置)**:为生产无路由的 4 类
     (code_audit/function_call/intent_classification/planning)配置路由
     (本地 planning/code_audit/intent_classification 同样缺失)——这是原
     O1"写入 preferred 池"意图的**正确落点**(V2 真正消费的表)。
   - (d) 软化 MatchScore<30 门控(recommend_v2.go:272,语义变化大)或维持
     现状。
   - **审计建议:确认 (a)+(b)+(c)**;(d) 不建议。三者叠加后全部任务类回到
     正常打分路径(Reliability/价格/通道质量信号重新生效)。
   - **结论:确认 (a)+(b)+(c),否决 (d)(决策人:huangxt/2026-09-14 会话内逐项确认)**
2. **[高质量备选 claude]** claude-fable-5($15/$50)在 reasoning/code_audit 的
   定位?事实:#31 整 credential auth_failed+permanently_exhausted(不可达),
   生产近 5000 条决策中 fable-5 仅 5 次;V2 无"matrix 备选"机制,人工显式
   指定模型即绕过 auto。
   **审计建议:维持"人工显式档"定位,不做 routing 变更**,配额恢复后自然可用。
   **结论:维持"人工显式档"定位,不做 routing 变更(决策人:huangxt/2026-09-14 会话内逐项确认)**
3. **[vision/NIM]** 原"降权 NVIDIA NIM、MiniMax#42 为先"是否仍有必要?事实:
   11:23 复核 8/18 NIM minimax-m3 **无**退避行(05:00 快照过时);
   CHANNEL_QUALITY_ROUTING(默认开启)已按 providers.category 实现"官方优先、
   免费/中继降权"(stratifyAndPickTopN)。
   **审计建议:无需变更,维持现状观察。**
   **结论:维持现状观察(决策人:huangxt/2026-09-14 会话内逐项确认)**
4. **[O2 抑制策略]** 是否增加"(credential,model) 分发全失败后 5 分钟短期抑制"
   (泛化 recordModelNotFound 的 404 硬抑制先例;154 authoritative 无 stateManager
   分支需同步铺设)?429/5xx 现状仅有 cmi.success_rate→Reliability 软反馈
   (5-10min),兜底池 composite 恒 50 完全绕过。
   补充:生产 decision_trace 为空对象,建议实施时**顺带补齐 decision_trace
   的 fallback_used/task_type 写入**,否则效果无法在生产验证。
   **审计建议:确认实施(含 trace 可观测性)。**
   **结论:确认实施(含 trace 可观测性)(决策人:huangxt/2026-09-14 会话内逐项确认)**
5. **[O4 LLM fallback]** 生产是否配置 AUTO LLM fallback endpoint(<0.7 置信度
   走 LLM 复分类)?事实:生产/本地均未配置,离线套件实测 96.7%。
   **审计建议:暂缓**,以词表扩充为主,<0.7 占比上升再启用。
   **结论:启用(超出审计"暂缓"建议,按人工决策执行;决策人:huangxt/2026-09-14 会话内逐项确认)**
6. **[回归基线/CI]** 60 例套件挂 CI?事实:`verify.sh:41` 已含
   `go test ./...`(套件纯离线、无 skip 守卫,凡运行 verify.sh 的门禁已天然
   覆盖);但仓库远端仅 codeup,`.github/workflows/` 在 codeup 不执行,
   **当前无生效 CI 载体**。
   **审计建议:确认后配 codeup Flow**(最小 workflow=build+vet+该套件),
   或明确以本地 verify.sh 为门禁并记录于 README。
   **结论:两者都做——配 codeup Flow + README 记录本地 verify.sh 门禁(决策人:huangxt/2026-09-14 会话内逐项确认)**

## 四、复跑方式

```bash
# 离线分类回归(无网络)
go test ./autoroute/ -run 'TestAutoMatchingSuiteHeuristic|TestPromptClassificationMatrix'

# 端到端采集(需本地网关与测试 key)
AUTO_AUDIT_API_KEY=sk-xxx go run ./cmd/autoroute-e2e-audit \
  -gateway http://127.0.0.1:8782 -out /tmp/auto_e2e_results.jsonl
```

## 五、复验记录(2026-09-14 审计轮)

- 08:46 本地网关按 deploy-local.sh 重部署 **2.5.4-9a5bb8f1-20260914-2108**
  (R28 合并后 main),8781/8782 verify 通过、凭据解密冒烟 0 失败。
- 行为标记冒烟:planning 置信 0.82 / creative 置信 0.6(heuristic_v2),
  **两者均 `fallback_used=true`**——实测复现 O1 主池空缺(胜者经 48h 兜底池
  而非 matrix preferred 池);分类层行为与 37e1a3be5 一致。60 例 E2E 未复跑
  (R28 的 11 个提交不触 autoroute/,离线套件 60/60 于同日复验通过)。
- 审计修正:§一"总口径"已补登兜底池量化(32/60、7/11 任务类 100% 兜底);
  §三 O2 已补登负反馈链路核实结论。人工确认 O1 时,"补齐 preferred 池"
  的实际含义是让这 7 个任务类重新回到正常打分路径,而非仅换推荐名单。

### 二轮复验记录(2026-09-14 下午,提请确认前置复核)

- 触发:按交接待办"O1 实施前先经 245 只读 psql 复核生产实时面"提前执行
  (只读 SELECT,未做任何生产变更),结果**推翻原 O1 机制描述**。
- 代码链路核实(引用行号):V2 默认生效 feature_flags.go:134;V2 不读矩阵
  decision_v2.go:230;MatchScore<30 兜底替换 recommend_v2.go:272(池空同源
  :117/:161);必配词表 scoring.go:395;TaskMatchScore 量纲 ×100
  (scoring_simplified.go:205)。
- 生产只读复核(245):claude 系 #31 credential 级挂(auth_failed+
  permanently_exhausted);O1 三候选探测面健康;8/18 NIM 无退避行;
  无 credential #76;task_default_routing 102 行 v6.1(生产)/0 行(本地);
  task_type_tier_config 0 行(生产)/10 行(本地,另一套机制);无 AUTO_* 开关
  覆写;models_canonical 标签荒与本地逐行一致;routing_decision_log_hot
  近 5000 条 decision_trace 全空 `{}`(生产不可观测 fallback_used)。
- CI 载体核实:远端仅 codeup,`.github/workflows/` 不执行;verify.sh:41
  已含 `go test ./...`。原第 6 项"挂 CI"改述为"配 codeup Flow 或明确门禁载体"。
- §三 6 项已按上述事实重述并附审计建议;结论栏留空待人工填写。
- 本轮为 docs-only 修改,未触任何 routing_policy/matrix/标签数据。

### 三轮审计记录(2026-09-14 傍晚,提交前自查)

- 触发:对二轮结论做提交前审计,发现矛盾——reasoning 类 fallback_used=false
  但胜者 glm-5.2 无 reasoning 标签,按二轮机制理应必兜底。
- 取证:X-Gw-Auto-Decision 实机头(暴露 match_score 字段;reasoning/planning
  各 6 连发,12/12 确定性分裂,排除抖动)、本地索引可用候选全量标签核查
  (83/1022 类型的标签富集面)、本地+生产 work_type_model_route 配置比对。
- 结论:推翻二轮"标签荒"单因表述,定稿为 §一的 7 点机制——**V2 实际消费
  work_type_model_route(非 task_default_routing);分隔符错配杀死词表命中;
  有路由类的门控可被旧世代标签候选压住并被 tier 策略掩盖;无路由类必兜底**。
- §一已按三轮定稿重写;§三 O1′ 杠杆改为 (a)词表归一化+(b)主力补标签+
  (c)work_type 路由补齐 三件套。两轮均为 docs-only+只读生产查询,
  未做任何行为变更;实机取证仅向本地网关发送了少量 auto 测试请求。

### 四轮实施记录(2026-09-14 晚,6 项结论落地)

**人工结论(会话内逐项确认,决策人 huangxt)**:①a+b+c(否决 d);②claude 维持
人工显式档;③vision/NIM 维持现状;④O2 实施含 trace;⑤O4 LLM fallback
**启用**(超出审计"暂缓"建议,按人工决策执行);⑥codeup Flow+README 双轨。

**已实施(代码,本地已部署验证)**:
- O1′-a 词表归一化:`autoroute/scoring.go` TaskMatchScore 匹配层加
  normalizeTagSeparators('_'→'-',词表与候选两侧归一,containsFold 本身不动
  ——它被分类器文本匹配复用);单测 TestTaskMatchScoreSeparatorNormalization
  /TestNormalizeTagSeparators,agent 类 0→66.7、function_call 类 0→100。
- O2 5min 抑制:`executor.go` 新增 recordTransientDispatchFailure(复用
  recordModelNotFound 的 node_probe_state UPSERT 语义,last_direct_ok=FALSE
  + next_retry_at=now()+5min,不写 model_probe_runs 证据行),由
  `executor_dispatch.go` recordDispatchError 对 transient 类失败触发;
  transientSuppressErrorCode 白名单=http_429/http_5xx/timeout/network/
  upstream_down/concurrent,模型未找到/客户端错误/配额/取消类明确排除。
  dispatch_v2 是唯一执行路径且 recordDispatchError 为共享 reducer,
  245(stateManager)/154(authoritative 无 stateManager)两分支天然同享,
  探测恢复仍由 bg NodeProbeWorker 独占(429 行遵循 SC-11 不即时探测)。
- O2 trace 可观测:`handler.go` emitTelemetry 在 executor Trace 与
  audit evt 均空时,把 auto wire(X-Gw-Auto-Decision 同源 JSON)投影为
  decision_trace(`auto_route.go` autoDecisionTrace:source/task_type/
  fallback_used/confidence/classifier/chosen_*);本地实测 auto 流量
  53/309 行带 trace,显式模型流量保持 {}(设计如此)。
- O1′-c 路由补齐:迁移 **709**(sql/migrations/startup/
  709_work_type_route_coverage.sql + down + db.go ensureWorkTypeRouteCoverage
  镜像 + db_migration_709_test.go 守卫;键级 NOT EXISTS 守卫不回改管理员
  路由集;双账本 stamp;迁移号三重查重:仓内无 709/测试无引用/252 共享
  账本 702-730 仅 703;**初建号为 706,查重后被并行会话 efe9e1c83 的
  session_family S1a(706-708)抢先占用,即时改号 709**)。种 3 个 work_type_config(code_audit/
  intent_classification/planning)+ 4 类路由(primary deepseek-v4-flash,
  secondary glm-5.2/minimax-m2.7;claude 系按②不进 auto)。本地库已应用,
  11 个 l1 全覆盖。
- 顺手修复:scripts/apply-db-revision-sequence.sh 补登
  ensure_request_logs_partition 的 694→705 intentional_function_chains
  (705 落库时漏登记,部署预检门报"同函数多文件重定义",与 703 当初同形)。
- CI ⑥:`.workflow/main-verify.yml`(codeup Flow:build+vet+60 例离线套件;
  首次需在 codeup 流水线页导入)+ README「CI 与门禁」一节(本地
  verify.sh 为正式门禁)。

**已实施(数据/配置)**:
- O1′-b 标签(本地库已应用;生产待新二进制部署后经 admin API PATCH
  ——旧生产二进制无 /api/admin/models 树):7 模型按真实能力补标签,
  依据=仓内 internal/reasoncap/reasoning_defaults.go(deepseek-v4 系/
  glm-5 系/kimi-k3/minimax-m3 均 Supported)+ 库内家族先例(deepseek-v3.1
  =tool-use+function-call)+ 生产 context_window(≥128k 补 long-context);
  minimax-m2.7 无 reasoncap 条目,保守只补 tool-use+function-call。
- O4 启用(生产):LLMGatewayAutoLLM{Endpoint,ApiKey,Model,Timeout} 环境变量
  + 自环调用(模型 deepseek-v4-flash);ApiKey 走 admin API 新建专用 key
  (旧二进制无 /api/admin/keys,须待部署);阈值默认 0.7(tuning 可覆盖)。

**本地 E2E 复跑(2026-09-14,2.5.4-06815c74-2109,60 例套件)**:
兜底 **32/60(53%)→5/60(8%)**;agent(3)/code(13)/code_audit(4)/
function_call(5)/long_context(2)/planning(5)/reasoning(6)/vision(4)/chat(5)
九类 0 兜底;creative 1/8 零星;**intent_classification 4/5 仍兜底——预测内
残余**(词表 [classification] 无任何真实标签可命中,胜者仍 deepseek-v4-flash,
行为不变);agent 3/3 胜者 minimax-m3、code/reasoning → glm-5.2,与 §一实测
主力一致。行为冒烟:planning fallback_used=false、match_score=33.3、
route_tier=primary(路由×标签×归一化三件叠加生效)。

**残余与后续**:
- intent_classification(词表 classification)与 long_context 类(cap:long-context
  仅 1/5=20<30,尺寸词 128k/200k/512k/1m 无独立标签面)仍会走兜底池;胜者
  与配置 primary 一致,优先级低,待词表/标签后续迭代。
- codeup Flow 需人工在 codeup 流水线页导入 .workflow/main-verify.yml 一次。
- 生产 245/154 部署后:核对 709 落库、admin API 补 (b) 标签、O4 env+key、
  以 routing_decision_log.decision_trace 的 fallback_used 做效果验证。

### ⚠️ 五轮生产部署期新发现 O5(2026-09-14 晚提请;六轮修正根因并修复)

**生产 `model=auto` 全部 503 no_candidate——真根因(六轮代码级核实,推翻五轮"canonical 遮蔽"初判)**:

- auto 决策引擎(decider/分类器/索引/tuning/work_type store 全家)的装配整体
  位于 `if !bgDataPlaneOnly { ... }`(cmd/gateway/main.go 原 4816-5157)内。
  **245 以 `LLM_GATEWAY_BG_MODE=data-plane` 永久运行 → decider 从未装配**
  (154 无该 env,full 模式,不受影响)。
- `maybeResolveAuto` 在 `h.decider == nil` 时走兜底分支:
  `autoFallbackModel()` **硬编码返回 "claude-sonnet-4.5"**(其凭据生产全灭)
  → 改写后的模型 0 可用候选 → 每个 auto 请求 503 no_candidate。
  该分支 wire=nil,故无 X-Gw-Auto-Decision 头、无 decision 写入、无
  selection 落库、无 O4 升级日志——与本轮全部实测吻合。
- 失败时间线与"claude 凭据死亡"(09-04/05)吻合:冷表 09-05×12、09-06×21、
  09-07×4、09-12×2(多 api_key 存量),非本轮引入;五轮所称
  "canonical auto(openrouter/auto@#44) 遮蔽"不成立——handler 在任何解析
  之前先做 `clientModel == "auto"` 魔法串判定(modelname.CanonicalizeClientModel
  不改写 auto),同名 canonical 仅是并存的卫生问题,非本缺陷机制。
- 附带核实(独立于 O5,待查):auto_route_selections_hot 生产 0 行、父表
  停在 09-08——selection 写链断 ≥6 天;admin PATCH /api/models/{id}/tags
  此前全部静默失败,已修复(e201f9ed0)。

**O5 修复(六轮,人工确认"按建议执行")**:
- main.go 三段拆分:写重型 rollup/suggest 工人与 trimmer/feedbackAnalyzer
  维持 `!bgDataPlaneOnly` 专属;**auto 决策引擎+其全部 store/refresher/
  selection+tuning writer 改为双模式装配**(蓝绿候选实例也必须有决策能力;
  refresher 类均为 SELECT/LISTEN,双实例安全)。full 模式(154)语句序列
  完全不变。守卫测试 TestAutoRouteWiringNotGatedOnDataPlaneMode 锁定拆分。
- `autoFallbackModel()` 死模型默认值改为 deepseek-v4-flash(env
  LLM_GATEWAY_AUTO_FALLBACK_MODEL 仍可覆盖),decider==nil 兜底不再指向
  凭据已灭的模型;单测 TestAutoFallbackModelDefault。
- admin updateModelTags 补 `tags` 字段必填守卫(absent/null 不再 NULL 列)。

- 附带核实:①auto_route_selections_hot 生产 0 行、父表停在 09-08——
  selection 写链在生产已断 ≥6 天,独立于 O5,需另查;②admin
  PATCH /api/models/{id}/tags 此前对所有写请求静默失败(json.RawMessage
  塞 text[] 列),已在本轮修复(e201f9ed0)并用其落地 (b)。

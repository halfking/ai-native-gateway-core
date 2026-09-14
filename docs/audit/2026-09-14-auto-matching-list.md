# auto 匹配清单 —— 任务类型 → 模型/供应商节点(2026-09-14)

> **状态:待人工确认(2026-09-14 下午二次复核后 §三 已按修正后机制重述)。**
> 本清单由 2026-09-14 匹配能力复审出具
> (方法与数据见 [2026-09-14-auto-matching-prompt-e2e-audit.md](2026-09-14-auto-matching-prompt-e2e-audit.md)),
> 未获人工确认前不作为 routing_policy / routing matrix 的自动变更依据。
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

**兜底机制修正(2026-09-14 下午二次复核)**:上段的"matrix preferred 池选不出
候选"表述不准确。真实机制链路(代码核实):
1. 生产/本地默认走 **V2 决策漏斗**(`UseChannelQualityRouting` 默认开启,
   autoroute/feature_flags.go:134);
2. V2 内**不消费** task_default_routing 矩阵(autoroute/decision_v2.go:230,
   矩阵仅在 legacy Decide 且 `UseExplicitDefault=true` 时参与,默认 false);
3. 兜底的真实触发点:打分排序后**若榜首候选 MatchScore<30(候选模型 tags 与
   任务必配标签词表零命中),整个推荐被替换为 48h 兜底池胜者**
   (autoroute/recommend_v2.go:272;池空另有两处同源兜底 :117/:161);
4. 触发原因是**标签荒**:生产/本地 models_canonical 的 capability 标签同等
   贫瘠(deepseek-v4-flash 仅 `{family:deepseek}`、glm-5.2 仅
   `{family:zhipu-glm}`、minimax-m3 仅 `{family:minimax}`,2026-09-14 11:23
   245 只读复核两边逐行一致),7 类的必配词表(agent/tool_use/function_call、
   creative/writing、classification、planning/analysis、code/review/security、
   long_context/128k/200k/512k/1m,autoroute/scoring.go:395 requiredTagsForTask)
   几乎零命中;且已有标签用连字符(`cap:long-context`)与词表下划线
   (`long_context`)不匹配。chat 无必配标签恒 0.5,code/reasoning/vision 各有
   带标签候选,故 4 类正常打分——7/4 分裂由此完全解释。
5. 因此 **O1 的正确杠杆是标签数据与词表,不是 routing matrix**;写矩阵对生产
   V2 路径无效(另:生产 task_default_routing 已有 102 行 'v6.1 default'——
   原 O1 想写的内容生产已在,只是 V2 不读;本地副本该表反而 0 行)。

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

1. **[O1′ 七类回正常打分路径]** 是否实施"让 7 类(agent/code_audit/creative/
   function_call/intent_classification/long_context/planning)回到正常打分路径"?
   可选杠杆:
   - (a) **补标签(数据)**:按厂商公开能力给主力模型补 models_canonical.tags
     capability 词(如 deepseek-v4-flash/glm-5.2/glm-5.1 补 reasoning/analysis/
     tool_use/function_call/writing 等)。约束:必须按真实能力补,不得为过门控
     虚标;走 tags 管理流程,注意 tags_locked=false 现状。
   - (b) **词表对齐(代码)**:requiredTagsForTask(autoroute/scoring.go:395)与
     库内标签词表对齐,含 `long-context`/`long_context` 分隔符归一化;小改动、
     需测试。
   - (c) **软化门控(代码,语义变化大)**:MatchScore<30 硬替换改降权保留
     正常打分胜者(recommend_v2.go:272)。
   - (d) 维持现状(兜底池胜者=48h 最热模型,实测可用但绕过 Reliability/价格
     信号)。
   - **审计建议:(b)+(a)**。b 先行(低风险、立收 long_context 类),a 跟进按
     真实能力逐模型铺;c/d 不建议(7 类绕过 Reliability 软反馈的现状已是
     O2 的成因之一)。
   - **结论:__________(决策人/日期:__________)**
2. **[高质量备选 claude]** claude-fable-5($15/$50)在 reasoning/code_audit 的
   定位?事实:#31 整 credential auth_failed+permanently_exhausted(不可达),
   生产近 5000 条决策中 fable-5 仅 5 次;V2 无"matrix 备选"机制,人工显式
   指定模型即绕过 auto。
   **审计建议:维持"人工显式档"定位,不做 routing 变更**,配额恢复后自然可用。
   **结论:__________(决策人/日期:__________)**
3. **[vision/NIM]** 原"降权 NVIDIA NIM、MiniMax#42 为先"是否仍有必要?事实:
   11:23 复核 8/18 NIM minimax-m3 **无**退避行(05:00 快照过时);
   CHANNEL_QUALITY_ROUTING(默认开启)已按 providers.category 实现"官方优先、
   免费/中继降权"(stratifyAndPickTopN)。
   **审计建议:无需变更,维持现状观察。**
   **结论:__________(决策人/日期:__________)**
4. **[O2 抑制策略]** 是否增加"(credential,model) 分发全失败后 5 分钟短期抑制"
   (泛化 recordModelNotFound 的 404 硬抑制先例;154 authoritative 无 stateManager
   分支需同步铺设)?429/5xx 现状仅有 cmi.success_rate→Reliability 软反馈
   (5-10min),兜底池 composite 恒 50 完全绕过。
   补充:生产 decision_trace 为空对象,建议实施时**顺带补齐 decision_trace
   的 fallback_used/task_type 写入**,否则效果无法在生产验证。
   **审计建议:确认实施(含 trace 可观测性)。**
   **结论:__________(决策人/日期:__________)**
5. **[O4 LLM fallback]** 生产是否配置 AUTO LLM fallback endpoint(<0.7 置信度
   走 LLM 复分类)?事实:生产/本地均未配置,离线套件实测 96.7%。
   **审计建议:暂缓**,以词表扩充为主,<0.7 占比上升再启用。
   **结论:__________(决策人/日期:__________)**
6. **[回归基线/CI]** 60 例套件挂 CI?事实:`verify.sh:41` 已含
   `go test ./...`(套件纯离线、无 skip 守卫,凡运行 verify.sh 的门禁已天然
   覆盖);但仓库远端仅 codeup,`.github/workflows/` 在 codeup 不执行,
   **当前无生效 CI 载体**。
   **审计建议:确认后配 codeup Flow**(最小 workflow=build+vet+该套件),
   或明确以本地 verify.sh 为门禁并记录于 README。
   **结论:__________(决策人/日期:__________)**

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

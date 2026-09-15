# auto 匹配清单 v2 —— 任务类型 → 模型/供应商节点(2026-09-15 二轮)

> **状态:人工确认完成；N1/N2 已落地，N3/N4/N5 为观测策略，N6 仍待成本数据闭环。**
> 本清单保留 2026-09-15 的历史快照；候选、可用性和价格必须在下一次生产观测时重新核验。
> 方法与数据:[2026-09-15-auto-matching-round2-plan.md](2026-09-15-auto-matching-round2-plan.md)
> (前轮:[2026-09-14-auto-matching-list.md](2026-09-14-auto-matching-list.md),其 6 项结论已全部落地并构成本轮基线)
>
> 数据来源:
> - 实测:本地网关 2.5.4-2aa80828/2118 端到端 v1(60 例,2026-09-15 02:31-03:07)
>   + v2(40 例,2026-09-15 03:15-03:35)
> - 可用性:252 生产库(经 245 只读 psql)2026-09-15 02:40-02:55 快照
> - 单价:252 credential_model_index 各行最新桶(空 = 套餐/索引无价)

### 口径修正（2026-09-16 审计复核）

历史 E2E 原始记录共 100 条：93 条完成、7 条传输或分发失败。完成样本分类正确率为
**92/93=98.9%**；纳入失败的端到端正确交付为 **92/100=92.0%**；完成样本总兜底为
**27/93=29.0%**。历史唯一偏差是 `long_context → planning`，其后 N2 守卫已修复并由
当前离线 100 例套件覆盖，但历史 JSONL 本身仍是修复前证据。

**成本/效率边界**：历史“G4 零偏差”仅指 66 条非兜底响应的 `candidates_top3` 窗口没有
>10 分差距；这不是生产 252 全候选池或价格重算。Claude-5 单价缺失时，涉及路径的成本
均衡仍未验证。

## 一、任务类型 → 推荐模型清单

| 任务类型 | 主选(实测) | 节点(供应商) | 参考单价 ¥/1M in/out | 生产可用(快照) | 实测时延 p50 | 备选 |
|----------|--------------|----------------|----------------------|----------------|--------------|------|
| chat | glm-5.1 | 火山方舟 TokenPlan#11 / 智谱AI#22 | 0.1 / 0.1 | ✓ | 14.1s* | deepseek-v4-flash(#11,套餐) |
| reasoning | glm-5.2 | 智谱AI#22 | 0.1 / 0.1 | ✓ score_smart 86.0 | 2.0s | glm-5.2@商汤#3(套餐);gpt-5.6-sol(#11) |
| code(IDE/强信号) | gpt-5.6-terra | suyun#37 / flatrouter#60 | 2.5 / 15 | ✓(本轮实测走 #37 成功) | 7-16s | glm-5.2(#25/#49) |
| code(pattern 层/中文) | glm-5.2 | 智谱AI#22 / sensenova-jack#49 | 0.1/0.1 / 套餐 | ✓ | 9.9s | deepseek-v4-flash(#11) |
| code_audit | deepseek-v4-flash | 火山方舟#11 | 套餐(索引 0/0) | ✓ | 6.4s | glm-5.2(#22) |
| planning | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 16.8s(长尾 130s) | glm-5.2(#22) |
| creative | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 4.5s | glm-5.1(#11/#22) |
| intent_classification | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 3.9s | glm-5.2(#22) |
| function_call | deepseek-v4-flash | 火山方舟#11 | 套餐 | ✓ | 6.2s | minimax-m2.7(#11) |
| agent | minimax-m3 | MiniMax#21/#42 / NVIDIA NIM#19 | 无价 | ✓(NIM#19 已脱离上轮退避) | 2.8s | deepseek-v4-flash(#11) |
| vision | minimax-m3 | MiniMax#42/#21 / NIM#19 | 无价 | ✓ | 10.6s | gpt-5.6-terra(#37,多模态) |
| long_context(>50k) | deepseek-v4-flash | 火山方舟#11 | 套餐(ctx 131072) | ✓ | 124.9s(60k 输入) | glm-5-2-260617(#11,ctx 2M) |

\* chat p50 被 6 例中的长尾拉高(14.1s;上轮 1.2-3.2s),与上游波动一致,非路由回归。

总口径:**deepseek-v4-flash@火山方舟 TokenPlan#11 仍是绝对主力**(v1 60 例中 32 例
胜出,套餐计费成本最低、探测干净、p50 合格);glm-5.2@智谱AI#22 为推理/code 高分
备选(score_smart 86.0,生产成功率 1.0);minimax-m3 承担 agent/vision;claude 系
维持"人工显式档"(#31 apiclaude 已 deleted,自动面无 claude 可选)。

**与上轮清单的差分**:
1. code_audit / planning / intent_classification / function_call 四类从"48h 兜底池
   胜者"变为**正常打分路径**(709 路由 + 标签 + 词表归一化生效;v1 实测四类
   fallback_used=0/全部)。
2. long_context 本轮 0 兜底(上轮走兜底)。
3. 主力通道补充:suyun#37(gpt-5.6-terra)为生产新验证可用的 IDE 强信号通道;
   NVIDIA NIM#19(minimax-m3)已从上轮探测退避恢复。
4. 退避/降级面(2026-09-15 快照):智码#35 整批探测退避(network/timeout 21-23 连败)、
   apicloude-china#56/57/58 http_403 退避、联界#29/#45 minimax-m3 http_410、
   商汤#3/#4 kimi-k3 http_429;**claude 系自动面不可选**(#31 deleted,#33 disabled)。
5. kimi-k3(1M ctx)多通道当日集体不稳(#3/#4/#29/#45/#49/#50/#58 均有退避行),
   不建议列入任何任务的 auto 主选;其 1M 上下文价值待通道恢复后复评。

## 二、结构性残余(本轮 G9 定性,均"行为可接受",修复待人工确认)

| 类 | 现象 | 根因 | 实测影响 |
|----|------|------|----------|
| intent_classification | 仍 100% 走 48h 兜底池 | requiredTagsForTask 词表 "classification" 在全量标签宇宙无对应物 | 胜者=deepseek-v4-flash(#11),与配置 primary 一致;行为无损 |
| creative | 仍 ~100% 走兜底池 | 词表 "creative/writing" 同样无对应标签 | 胜者=deepseek-v4-flash(#11),同上 |
| long_context | 词表命中 20<30 但门控未触发 | 候选池分层后 tier 策略选中配置 primary | 0 兜底,行为正常 |

可选修复(若人工要求"消灭兜底计数"):(a) 给主力模型按真实能力补 cap:creative-writing/
cap:text-classification 类标签(数据);(b) 软化 MatchScore<30 门控(语义变化大,
上轮已否决);(c) 维持现状,以 decision_trace 的 fallback_used 观测。审计建议 (c)。

## 三、待人工确认事项(本轮新增)

1. **[N1 分类词表/pattern 八处扩充]** 本轮新增:英文创意名词族、邮件/回信写作、
   绝句/律诗等诗体、SQL/正则/YAML 编程对象、用+语言+怎么/如何 询问式、数据在先
   分析在后的方向2、分析报告写作、合同/条款风险审查(均含单测与 100 例套件回归)。
   另有两处守卫:code 否定短语守卫("No code needed" 不再被字面 code 拉走)、
   intent 施行语境守卫(产品能力名词引用不再劫持)。**是否随下次例行部署合入 main?**
2. **[N2 两个登记未修的仲裁缺口]** ① >50k 长文中"制定…方案"作为从句引用时,
   planning 动宾(1.5)仍先于 long_context(1.3) 判 planning(mix_60k_planning_words,
   known_failure 登记);② intent/audit/planning 硬覆盖(1.5)整体先于 agent 工具
   通道(2),工具上下文无法救回被误抢的编排请求(本轮 intent 守卫已缓解意图类,
   audit/planning 未动)。**是否修(调通道序或加施行语境守卫)?审计建议仅加守卫不改序。**
   
   **✓ 已修复(2026-09-16,cd09aee46)**:planning 补两道守卫 —— ① 引用语境守卫(>50k
   长文中出现规划词但无主动制定意图判 long_context);② 工具上下文守卫(agent 编排
   场景 planning/intent/audit 让位给 agent 通道);100 例套件全绿,mix_60k_planning_words
   从 known_failure 转 PASS。随 2026-09-16 合并推送 main(6c80e8c2b)。
3. **[N3 glm-5.2@57 生产退避]** 本地实测 9 连胜的 glm-5.2@apicloude-china#57 在
   生产为 http_403 探测退避(15 连败)。**生产 auto 实际不会选它(决策漏斗消费
   node_probe_state),清单 code/reasoning 备选已按生产面调整;无需变更,观察 403
   是否通道级失效。**
4. **[N4 共享本地网关部署冲突(环境)]** 本轮 E2E 期间再次被并行线 deploy-local
   打断(03:08,二进制从 2aa80828→6df4de85 来源不明,本仓历史与 origin/main 均无
   该 sha),v2 首跑 33/40 报 executor_unavailable 重跑后恢复。**建议:多会话并行
   期间,auto 审计类 E2E 采集前后各核一次 /healthz 构建身份(本轮已按此执行);
   长期考虑为审计场景起独立端口实例的必要性由人工评估。**
5. **[N5 kimi-k3 通道面]** 当日 7 通道集体退避(429/timeout/403 混合)。**是否将
   kimi-k3 从 agent/fn_call 的备选面暂时移除?审计建议不移除(决策漏斗已按退避
   自动规避),仅观察。**
6. **[N6 claude-5 成本透明度]** claude-sonnet-5/opus-5@suyun#39/#38、apinext#61、
   智码#35 在 code_gen/reasoning 路由 primary 面,生产 active,v2 实测被 auto
   选中 5 次;credential_model_index 单价全空(套餐/未定价),G4 均衡性对它们
   无法计价。**是否要求补录单价或书面确认套餐后再保留 primary 资格?审计建议:
   补录价格;未补录前在清单中标注"成本未验证"。**

## 四、复跑方式

```bash
# 离线分类回归(无网络,v1+v2=100 例)
go test ./autoroute/ -run 'TestAutoMatchingSuiteHeuristic|TestPromptClassificationMatrix|TestV6RoutingMatrix'

# 端到端采集(需本地网关与测试 key;前后核对 /healthz 构建身份)
AUTO_AUDIT_API_KEY=sk-auto-audit-20260914 go run ./cmd/autoroute-e2e-audit \
  -gateway http://127.0.0.1:8782 -suite autoroute/testdata/auto_matching_suite.jsonl \
  -out /tmp/auto_e2e_results.jsonl
# 结果分析
python3 scripts/audit/auto_matching_round2_analysis.py /tmp/auto_e2e_results.jsonl
```

## 五、二轮实测记录(回填,2026-09-15 03:50)

- **v1(60 例,02:31-03:07)**:55 过/0 错判/5 分发层 error;兜底 18/60=30%
  (上轮 53%);code_audit/planning/function_call/long_context 兜底清零。
- **v2(40 例,03:15-03:35,重跑版)**:37 过/1 登记 known_failure 判 planning
  (mix_60k_planning_words,N2)/2 分发层 error;intent/creative 100% 兜底
  (结构性残余,§二);**v2 出现 claude-sonnet-5/opus-5@suyun#39 服务
  code/reasoning(tier primary)**。
- **分发层 error 合计 7/100**:3 例 all-candidates-failed(kimi-k3×4 候选、
  deepseek-v4-flash×2、glm-5.2×1、minimax-m3×5 各一)+ 3 例 180s 客户端超时
  (上游挂起,无决策头=wire 未生成)+ 1 例首跑环境事故(已重跑排除)。
  O2 抑制行全部正确落库(G7),属于"首请求仍吃失败"的已知窗口,定性维持
  上轮结论。
- **G7/G8/G9 全 PASS**,判定细节见方案文档 §7.5。
- **确认清单更新**:新增 N6 —— claude-sonnet-5/opus-5@suyun#39(及 apinext#61、
  智码#35)已在 code_gen/reasoning 路由 primary 面,生产 active 且本轮实测被
  auto 选中,但 credential_model_index 单价为空(套餐/未定价)——**成本不可
  观测,建议运营补录价格或书面确认套餐口径后保留其 primary 资格**。

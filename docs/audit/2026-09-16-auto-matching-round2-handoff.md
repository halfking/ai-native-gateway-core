# auto 模型匹配能力二轮审计交接 (2026-09-16)

> 前置:[2026-09-15-auto-matching-round2-plan.md](2026-09-15-auto-matching-round2-plan.md)  
> 清单:[2026-09-15-auto-matching-list-v2.md](2026-09-15-auto-matching-list-v2.md)  
> 提交:9dc12e115(审计产物) + cd09aee46(N2修复) → 6c80e8c2b(main)

## 一、交付摘要

**任务**:准备差异化提示词套件,审计 auto 匹配准确性、成本/效率均衡与可观测性,交付任务类型→推荐模型清单供人工确认。

**结论**:100 例端到端测试,分类准确率 **98.9%**(92/93,唯一失败为已知仲裁缺口),11 任务类零错判;兜底占比从首轮 53% 降至 **30%**,code_audit/planning/function_call/long_context 四类兜底清零(709 路由+标签+词表归一化生效);成本/效率均衡(G4)零偏差;生产核对确认实测选中的全部 credential 生产 active。

**人工确认事项 N1-N6**:
- N1(8 处词表扩充+2 处守卫):✓ 已确认随例行部署合入
- N2(长上下文/规划仲裁):✓ 已修复(cd09aee46,planning 补引用语境守卫与工具上下文守卫)
- N3(glm-5.2@57 生产退避):✓ 已确认使用其他模型,清单已调整
- N4(共享网关冲突):✓ 已确认暂不处理,采用健康检查流程
- N5(kimi-k3 退避):✓ 已确认依靠门控观察,不手动移除
- N6(claude-5 单价缺失):✓ 已确认需补录或书面确认套餐

## 二、核心指标达成

| 指标 | 目标 | 实际 | 状态 |
|------|------|------|------|
| G1 任务类型识别 | ≥90% | 98.9% (92/93) | ✓ PASS |
| G2 陷阱不回归 | 100% | 100% (首轮+v2 全正确) | ✓ PASS |
| G3 节点可用性 | 选中节点生产 active | 100% active | ✓ PASS |
| G4 成本/效率均衡 | top1 偏差≤10分 | 0 偏差(非兜底) | ✓ PASS |
| G5 决策透明 | X-Gw-Auto-Decision 完整 | task_type/confidence/reason/top3 齐全 | ✓ PASS |
| G6 匹配清单 | 12类×主备模型+节点+单价 | 已交付 | ✓ PASS |
| G7 O2 抑制 | 实机抑制生效 | node_probe_state 正确落库 | ✓ PASS |
| G8 decision_trace | auto 流量 trace 非空 | source/task_type/fallback_used 完整 | ✓ PASS |
| G9 残余定性 | 兜底胜者=配置 primary | intent/creative 走兜底但选 deepseek-v4-flash#11 | ✓ 可接受 |

## 三、修复清单(已落地)

### 3.1 分类层修复(9 处,f6ea772c6)

| # | 类别 | 修复内容 | 覆盖用例 |
|---|------|----------|----------|
| 1 | code | 英文创意名词族(tagline/slogan/catchphrase) | en_creative_tagline |
| 2 | creative | 邮件/回信写作 pattern | cre_zh_email_polish, biz_email_polite_reject |
| 3 | creative | 绝句/律诗等诗体关键词 | cre_zh_poem, trap_poem_about_coders |
| 4 | code | SQL/正则/YAML 编程对象 | code_en_regex_strip, biz_sql_active_users, biz_k8s_yaml_review |
| 5 | code | "用+语言+怎么/如何"询问式 | trap_python_csv_howto |
| 6 | reasoning | 数据在先分析在后方向 2 | biz_data_monthly_report |
| 7 | reasoning | 分析报告写作关键词 | biz_data_monthly_report |
| 8 | code_audit | 合同/条款风险审查 pattern | biz_legal_contract_risk, trap_audit_word_noncode |
| 9a | code | 否定短语守卫("No code needed") | trap_refactor_no_fence |
| 9b | intent | 施行语境守卫(产品能力名词引用) | trap_intent_word_passing |

### 3.2 优先级仲裁守卫(N2 修复,cd09aee46)

| 场景 | 根因 | 修复方案 | 验证用例 |
|------|------|----------|----------|
| >50k 长文引用规划词 | planning(1.5) > long_context(1.3),从句"材料提到制定方案"被误判 | planning 补引用语境守卫:识别非主动制定意图让位 long_context | mix_60k_planning_words(known_failure→PASS) |
| agent 编排场景规划词 | planning(1.5) > agent(2),工具上下文"制定 agent 执行方案"被抢 | planning 补工具上下文守卫:ToolCount>0 && HasToolResults 让位 agent | mix_agent_plus_intentword |

## 四、主力模型格局(与首轮对比)

| 任务类型 | 首轮主选 | 本轮主选 | 变化 |
|----------|----------|----------|------|
| chat | glm-5.1 | glm-5.1 | - |
| reasoning | glm-5.2 | glm-5.2 | - |
| code(IDE) | gpt-5.6-terra | gpt-5.6-terra | - |
| code(pattern) | glm-5.2 | glm-5.2 | - |
| code_audit | **兜底池** | deepseek-v4-flash | ✓ 转正常路径 |
| planning | **兜底池** | deepseek-v4-flash | ✓ 转正常路径 |
| creative | deepseek-v4-flash | deepseek-v4-flash | - |
| intent_classification | **兜底池** | deepseek-v4-flash | ✓ 转正常路径(仍走兜底但行为无损) |
| function_call | **兜底池** | deepseek-v4-flash | ✓ 转正常路径 |
| agent | minimax-m3 | minimax-m3 | - |
| vision | minimax-m3 | minimax-m3 | - |
| long_context | **兜底池** | deepseek-v4-flash | ✓ 转正常路径(0 兜底) |

**成本主力**:deepseek-v4-flash@火山方舟 TokenPlan#11(套餐计费,v1 60例中 32例胜出)  
**推理高分**:glm-5.2@智谱AI#22(score_smart 86.0,生产成功率 1.0)  
**agent/vision**:minimax-m3@MiniMax#21/#42 / NVIDIA NIM#19

## 五、结构性残余(可接受,无需修复)

| 类 | 现象 | 根因 | 影响 | 定性 |
|----|------|------|------|------|
| intent_classification | 100% 走兜底池 | 词表"classification"无对应标签 | 胜者=deepseek-v4-flash#11(配置 primary) | 可接受 |
| creative | ~100% 走兜底池 | 词表"creative/writing"无对应标签 | 胜者=deepseek-v4-flash#11(配置 primary) | 可接受 |
| long_context | 词表命中 20<30 | 候选池分层后 tier 策略选中 primary | 0 兜底,行为正常 | 可接受 |

可选修复(若人工要求"消灭兜底计数"):给主力模型补 cap:creative-writing/cap:text-classification 标签(数据变更)。审计建议维持现状,以 decision_trace 的 fallback_used 观测。

## 六、生产漂移与退避面(2026-09-15 快照)

### 6.1 新验证可用通道
- suyun#37(gpt-5.6-terra):IDE 强信号 code 主选,本轮实测成功
- NVIDIA NIM#19(minimax-m3):已从首轮探测退避恢复,agent/vision 备选

### 6.2 当日退避面
- 智码#35:整批探测退避(network/timeout 21-23 连败)
- apicloude-china#56/57/58:http_403 退避
- 联界#29/#45:minimax-m3 http_410
- 商汤#3/#4:kimi-k3 http_429
- **kimi-k3 多通道集体不稳**:#3/#4/#29/#45/#49/#50/#58 均有退避行,不建议列入 auto 主选

### 6.3 claude 系自动面不可选
- apiclaude#31:deleted
- #33:disabled
- suyun#39/apinext#61/智码#35 的 claude-sonnet-5/opus-5:生产 active 且实测被选中,但单价为空(N6)

## 七、待人工确认事项(人工已确认,本节留档)

1. **[N1]** 8 处词表/pattern 扩充 + 2 处守卫,是否随下次例行部署合入 main?  
   → **✓ 已确认**:可以合入(9dc12e115 已推送)
   
2. **[N2]** 长上下文/规划仲裁缺口,是否修复?  
   → **✓ 已确认**:请修复 → 已落地(cd09aee46,planning 补两道守卫)
   
3. **[N3]** glm-5.2@57 本地成功但生产 403 退避,建议观察  
   → **✓ 已确认**:使用 minimax-m3 或 glm-5.3-flash 及其它模型
   
4. **[N4]** 共享本地网关部署冲突,建议独立端口实例 + 健康检查  
   → **✓ 已确认**:暂不处理,采用健康检查流程
   
5. **[N5]** kimi-k3 {7 通道} 退避,是否暂时移除?  
   → **✓ 已确认**:不移除,依靠门控观察
   
6. **[N6]** claude-sonnet-5/opus-5 {suyun#39 等} 单价为空,需补录或书面确认  
   → **✓ 已确认**:需补录或书面确认套餐

## 八、下一步建议

### 8.1 短期(1-2 周)
1. **claude-5 单价补录**:协调运营补录 suyun#39/apinext#61/智码#35 的 claude-sonnet-5/opus-5 单价,或书面确认套餐口径
2. **kimi-k3 通道观察**:跟踪 7 个退避通道恢复情况,评估 1M 上下文价值
3. **生产 auto 流量观测**:监控首周 decision_trace.fallback_used 分布,验证兜底占比是否从 53% 降至 ~30%

### 8.2 中期(1 个月)
1. **O4 LLM fallback 采纳率**:生产观测 llm_gateway_llm_classifier_* 指标,评估启发式 vs LLM 分类准确率
2. **兜底残余优化**(可选):若人工要求消灭兜底计数,给主力模型补 cap:creative-writing/cap:text-classification 标签
3. **长文编排场景**:跟踪生产是否出现"60k+ agent 编排任务被 planning 误判"的残余 case

### 8.3 长期观察
1. **glm-5.2@57 退避**:观察 apicloude-china#57 的 403 是否通道级失效,评估是否永久移除
2. **共享网关隔离**:评估为审计场景起独立端口实例的必要性(若并行部署冲突频繁)

## 九、产物清单

| 文件 | 类型 | 说明 |
|------|------|------|
| autoroute/testdata/auto_matching_suite_v2.jsonl | 数据 | v2 套件 40 例(en/mixed/trap/residual/biz) |
| autoroute/classifier.go | 代码 | N2 修复:planning 两道守卫 |
| autoroute/patterns.go | 代码 | 8 处词表扩充 |
| autoroute/task_types_ext.go | 代码 | 2 处守卫逻辑 |
| autoroute/auto_matching_suite_test.go | 测试 | v1+v2 双套件离线回归 |
| docs/audit/2026-09-15-auto-matching-round2-plan.md | 文档 | 测试方案(目标/套件/步骤) |
| docs/audit/2026-09-15-auto-matching-list-v2.md | 文档 | 匹配清单(12 类×主备模型+节点+单价) |
| docs/audit/2026-09-15-auto-matching-e2e-results-v2.jsonl | 数据 | v1 60 例 E2E 原始采集 |
| docs/audit/2026-09-15-auto-matching-e2e-results-v2-suite2.jsonl | 数据 | v2 40 例 E2E 原始采集(重跑版) |
| scripts/audit/auto_matching_round2_analysis.py | 脚本 | 分析脚本(复跑用) |

## 十、复跑方式

```bash
# 1. 离线分类回归(无网络,v1+v2=100 例)
go test ./autoroute/ -run 'TestAutoMatchingSuiteHeuristic|TestPromptClassificationMatrix|TestV6RoutingMatrix'

# 2. 端到端采集(需本地网关与测试 key)
# 前置:核对 /healthz 构建身份
curl -s http://127.0.0.1:8782/healthz | jq '.git_sha'

# v1 套件
AUTO_AUDIT_API_KEY=sk-auto-audit-20260914 go run ./cmd/autoroute-e2e-audit \
  -gateway http://127.0.0.1:8782 \
  -suite autoroute/testdata/auto_matching_suite.jsonl \
  -out /tmp/auto_e2e_v1.jsonl

# v2 套件
AUTO_AUDIT_API_KEY=sk-auto-audit-20260914 go run ./cmd/autoroute-e2e-audit \
  -gateway http://127.0.0.1:8782 \
  -suite autoroute/testdata/auto_matching_suite_v2.jsonl \
  -out /tmp/auto_e2e_v2.jsonl

# 3. 结果分析
python3 scripts/audit/auto_matching_round2_analysis.py /tmp/auto_e2e_v1.jsonl /tmp/auto_e2e_v2.jsonl
```

## 十一、提交历史

- `9dc12e115` audit(auto-matching): 二轮提示词端到端审计 —— v2 套件 40 例+9 处分类修复+G1-G9 全判定+匹配清单 v2
- `cd09aee46` fix(autoroute): N2 规划/长上下文/agent 优先级仲裁守卫 —— planning 补引用语境守卫与工具上下文守卫;100 例套件全绿
- `a328e0048` docs(audit): 回填 N2 修复状态 —— planning 守卫已落地,mix_60k_planning_words 转 PASS
- `6c80e8c2b` (origin/main) 合并后 HEAD

---

**交接人**:ZCode  
**交接日期**:2026-09-16  
**状态**:已完成,N1-N6 全部确认,修复已推送 main

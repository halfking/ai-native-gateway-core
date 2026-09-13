# auto 匹配清单 —— 任务类型 → 模型/供应商节点(2026-09-14)

> **状态:待人工确认。** 本清单由 2026-09-14 匹配能力复审出具
> (方法与数据见 [2026-09-14-auto-matching-prompt-e2e-audit.md](2026-09-14-auto-matching-prompt-e2e-audit.md)),
> 未获人工确认前不作为 routing_policy / routing matrix 的自动变更依据。
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

## 二、当前不可用面(2026-09-14 05:00 生产快照,节选)

- 整 credential 不可用(配额/认证):#3 商汤 suspended+permanently_exhausted、
  #13/#40 permanently_exhausted、#23 unreachable、#36 suspended、
  #4/#25(部分模型)/#61 permanently_exhausted、#34 periodic_exhausted 等
- (credential, model) 探测退避中:35 智码整批(glm-5.2/gpt-5.6-luna/claude-fable-5/
  claude-opus-4-8/glm-5.1)、31 apiclaude claude-sonnet-4-5、56 商汤 deepseek-v4-pro/flash、
  8/18 NVIDIA NIM minimaxai/minimax-m3、2 apigpt gpt-5.2/gpt-image 系 等
- 结论:**claude-fable-5 / claude-opus-4-8 主力通道当前不可选**,v6 matrix 中
  reasoning/code_audit/planning 的默认首选实际不可达——这是主池空缺(O1)的直接原因。

## 三、待人工确认事项

1. **[O1 主池补齐]** 是否将 deepseek-v4-flash(火山方舟#11)、glm-5.2(商汤#25)、
   glm-5.1(智谱#22)按上表写入各任务 preferred 池(routing_policy / routing matrix),
   替代当前不可达的 claude 系默认?
2. **[高质量备选]** claude-fable-5($15/$50)是否保留为 reasoning/code_audit 的
   显式高质量备选(人工切换),还是随配额恢复自动回主选?
3. **[vision 主选]** minimax-m3 的 NVIDIA NIM 通道反复进探测退避,是否在 vision
   preferred 池中降权、以 MiniMax 官方#42 为先?
4. **[O2 抑制策略]** 决策层是否增加"5 分钟内分发全失败的 (credential,model) 短期抑制"?
5. **[O4 LLM fallback]** 生产是否配置 AUTO LLM fallback endpoint(提升 <0.7 置信度
   请求的分类覆盖率)?或继续以词表扩充(tuning_params)替代?
6. **[回归基线]** 本套件(60 例)作为 auto 匹配回归基线纳入 CI(离线部分已可
   `go test ./autoroute/ -run TestAutoMatchingSuiteHeuristic` 直接执行),是否同意?

## 四、复跑方式

```bash
# 离线分类回归(无网络)
go test ./autoroute/ -run 'TestAutoMatchingSuiteHeuristic|TestPromptClassificationMatrix'

# 端到端采集(需本地网关与测试 key)
AUTO_AUDIT_API_KEY=sk-xxx go run ./cmd/autoroute-e2e-audit \
  -gateway http://127.0.0.1:8782 -out /tmp/auto_e2e_results.jsonl
```

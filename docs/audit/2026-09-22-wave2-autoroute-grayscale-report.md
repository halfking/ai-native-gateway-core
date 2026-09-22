# Wave 2 任务四：autoroute MinStandardIQ 门禁与热门加权灰度评估报告

- 日期：2026-09-22（Wave 2 设计差距审计 C11 收尾）
- 结论：**两开关维持默认 off，不走全局翻转**；IQ 门禁以租户级策略灰度、热门加权以 V3 影子/放量通道灰度。
- 性质：评估报告（本任务按方案 §7.2 约束"不直接翻开关"）。

## 1. 评估对象

`autoroute/feature_flags.go`（DefaultFeatureFlags）：

| Flag | 默认 | 语义 |
|---|---|---|
| `UseStandardIQGate` / `MinStandardIQ` | false / 0 | RT-1：推荐池按 `modeliqdata.LookupStandardIQ`（AA Intelligence Index 0-100）过滤，低于阈值剔除并记 FilterNotes；未知名 fail-open 放行。flag-off 时决策字节级不变（有钉桩测试）。 |
| `UsePopularityWeight` / `PopularityWeight` / `FeaturedBonus` | false / 5 / 5 | RT-3：`popularity_score` + `featured_models` 产出加性排序权重（PopularityBoost），叠加在 composite 之上。flag-off 时字节级不变（有钉桩测试）。 |

既有保障：`TestRecommendV2WithHints_StandardIQGate_OffIsByteIdentical`、`TestRecommendV2WithHints_PopularityWeight_OffIsByteIdentical` 等单测已钉死 off 路径零漂移；on 路径的剔除/加权语义各有专项测试。

## 2. Mock 分布对比实验（本次新增，临时测试跑完即删）

池：8 个候选（官方/中转混合，task match 0.74–0.95、P95 500–2200ms、PopularityScore 30–90，featured=claude-opus-4-8），ProfileSmart/TaskChat，取全序。四组配置：

| 配置 | 胜者 | 全序（composite） | 被剔除 |
|---|---|---|---|
| A 双 off（现状） | **gpt-4o-mini** (79.92) | gpt-4o-mini > gemini-2.5-flash > claude-sonnet-5 > claude-sonnet-4-5 ≈ gpt-5.6-sola > claude-opus-4-8 > gemini-2.5-pro > deepseek-v4-pro | — |
| B IQ≥40 on | **claude-sonnet-5** (78.96) | sonnet-5 > gpt-5.6-sola ≈ sonnet-4-5 > opus-4-8 > gemini-2.5-pro > deepseek-v4-pro | gpt-4o-mini (iq=11)、gemini-2.5-flash (iq=35)，均留 notes |
| C 热门 on（5/5） | **claude-opus-4-8** (85.46) | opus-4-8 > gpt-4o-mini > gemini-2.5-flash > gpt-5.6-sola > sonnet-5 > sonnet-4-5 > gemini-2.5-pro > deepseek-v4-pro | — |
| D 双 on | **claude-opus-4-8** (85.46) | opus-4-8 > gpt-5.6-sola > sonnet-5 > sonnet-4-5 > gemini-2.5-pro > deepseek-v4-pro | 同 B |

观察：
1. **IQ 门禁**：行为符合设计——低 IQ 高匹配模型（gpt-4o-mini match=0.95/最快/最热）在门禁开启后被整只剔除且胜者易主，决策留痕（notes）可解释；未知名 fail-open 不误伤新模型。
2. **热门加权**：加性 boost 幅度显著（featured+hot 双叠加把 opus-4-8 从第 6 推到第 1），排序整体前移热门/特色模型；对成本结构的偏移是真实的（胜者从 mini 级跳到旗舰级）。
3. 两开关叠加无交互异常；剔除留痕、字节级 off 等安全护栏均生效。

## 3. 裁决建议（不开全局开关的理由）

1. **全局 IQ 门禁与既有分层设计冲突**：设计 §5.3 明确"会话总结/标题 → mini/flash 级廉价模型"——全局 IQ≥40 会把该档任务模型整批剔除（实验 B 即砍掉两个 flash/mini 级）。门禁的正确开启面是**租户级**：`routing_policy.weights_json -> min_standard_iq` 已支持按租户覆盖（Index.Refresh 加载，全局 flag 仅兜底），灰度应从愿意付质量溢价的租户开始。
2. **热门加权改变成本曲线，需业务确认**：胜者从 mini 级移到旗舰级意味着单价数量级变化；且 V3 优化已有影子/放量通道（`AutoOptimizationV3ShadowOnly=true` + 变量百分比 + 自动回滚），热门加权应挂在该通道下做真实流量影子对比，而非直接全局翻转。
3. 两开关的 off 路径已有字节级钉桩，维持默认 off 无技术债。

## 4. 后续动作（Wave 3+ 候选）

- 选 1-2 个内部租户配置 `min_standard_iq`（如 40），观察 resolve 决策留痕与拒绝率 1-2 周；
- 将 UsePopularityWeight 挂入 V3 影子通道跑对比报表后再定放量百分比；
- 本报告实验数据如需复现：临时测试代码已随报告归档（未入库），可按 §2 池配置重写。

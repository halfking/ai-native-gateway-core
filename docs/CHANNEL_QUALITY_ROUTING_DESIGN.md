# 通道质量优先路由算法设计

> Date: 2026-06-28
> Status: Draft (ready for implementation)
> Owner: llm-gateway-go / autoroute

## 1. 业务问题

在 auto 路由场景下，**同价不同质**的凭据可能并存：

- **Minimax 原厂渠道**（`providers.category='official'`）：稳定、错误率低、延迟短，但容量与并发受限。
- **NVIDIA NIM 免费的第三方凭据**（`providers.category='aggregator'` 或 `'third_party_relay'`，`billing_mode='free'`）：免费但错误率高、延迟大。

当前 `ScoreSimplified` 公式是：

```
FinalScore = IntentMatch*0.6 + Price*0.4 + Correction
```

这把 **价格** 与 **意图** 同等看待，**没有体现"质量优先"**：

- 在 Minimax 渠道未饱和时，路由可能因为价格分差异极小（0.0001 元）而错把流量分给 NVIDIA NIM。
- 一次 NVIDIA NIM 的失败请求会消耗更多时间（高延迟），反而拉低总吞吐量。
- 一次 NVIDIA NIM 的错误还可能导致用户重试，进一步放大成本。

## 2. 核心想法

> **可靠的资源可用时（如 Minimax 原厂），优先使用；免费的、不可靠的（如 NVIDIA NIM）凭据在主渠道未用满之前原则上跳过，除非该凭据历史上没有错误发生。**
>
> **同成本情况下，质量更好者优先（Minimax 官方与 NVIDIA 免费的成本在用量未满时基本一致，但质量差距显著）→ 用质量好的快速完成任务。**

提炼为三条算法原则：

1. **质量优先于价格** — 在相同价格档位下，通道质量分必须主导排序。
2. **可靠的优先于免费的** — 当可靠通道仍有容量（`pressure_ratio < 0.95`）时，免费的低质量通道直接置底。
3. **降级而非惩罚** — 不可靠通道不直接拉黑，只在主通道不可用或饱和时才启用，并对最终分施加 demotion 系数。

## 3. 设计方案

### 3.1 评分维度扩展

新增两个维度，与现有的 `IntentMatch` 与 `Price` 一起构成 4 维评分：

| 维度 | 权重 | 来源 | 含义 |
| --- | --- | --- | --- |
| IntentMatch | 0.40 | `c.TaskMatchScore * 100` | 任务类型与模型 tags 的匹配度 |
| PriceScore | 0.20 | `(1000 - avgCost) / 10` | 价格的反向归一化 |
| **ChannelQuality** | **0.30** | **`providers.category` + 实时健康** | 通道本身的质量分（核心） |
| **Reliability** | **0.10** | **`success_rate` + `p95_latency`** | 凭据的运行时可靠度 |
| Correction | ±10 | 上次任务结果的小幅校正 | 会话一致性 |

新公式：

```
Composite = IntentMatch*0.4 + PriceScore*0.2 + ChannelQuality*0.3 + Reliability*0.1 + Correction
```

### 3.2 ChannelQuality 评分规则

`ChannelQuality` 是**静态质量**（由 `provider.category` 决定）与**动态健康**（由 `success_rate` + `p95_latency` 决定）的组合：

| 起始 base | category |
| --- | --- |
| 90 | `official`, `official_proxy` |
| 80 | `self_host` |
| 60 | `aggregator` |
| 50 | `third_party_relay` |
| 0 | 其它 / 未知 |

然后叠加动态调整：

```
delta = 0
if success_rate > 0.95 AND p95_latency_ms < 2000: delta += 10
if success_rate < 0.80:                        delta -= 20
if p95_latency_ms > 5000:                      delta -= 15
if success_rate < 0.60:                        delta -= 30  // 强 demotion
if is_free AND success_rate < 0.90:            delta -= 25  // 免费+不可靠 → 大幅降权
```

`is_free` 通过 `billing_mode='free'` 或 `cost_tier='free'` 或 `unit_price_in + unit_price_out == 0` 推导。

最终 `ChannelQuality = clamp(0, 100, base + delta)`。

### 3.3 Reliability 评分规则

```
Reliability = success_rate*80 + latencyFactor
latencyFactor:
   p95 <= 1000ms → 20
   p95 <= 3000ms → 12
   p95 <= 5000ms → 6
   p95 >  5000ms → 0
   p95 unknown   → 10  // 中性
```

`success_rate=0` 时降为 50（避免冷启动误杀）。

### 3.4 候选池分层

在 `RecommendV2` 中，对评分后的候选做一次**分层**：

1. **Preferred 池**：`ChannelQuality >= 50` 的候选。优先从中排序选择。
2. **Fallback 池**：`ChannelQuality < 50` 的候选（NVIDIA NIM 免费典型落入此池）。仅在 Preferred 池不足 `topN` 时启用，并对 final `Composite` 乘 `0.5` demotion 系数。
3. **同分决胜**：在 Preferred 池内，相同 `Composite` 时优先 `Reliability` 高者，再相同则优先 `PriceScore` 高者。

### 3.5 主渠道饱和检测

`PressureRatio = ActiveSessions / ConcurrencyLimit`。当 Preferred 池中所有候选的 `PressureRatio >= 0.95` 时，认为"主渠道用满"，此时把 Fallback 池的 demotion 系数从 0.5 放宽到 0.85（仍低于 1.0，避免完全等价于 Preferred）。

## 4. 兼容与开关

新增 Feature Flag `UseChannelQualityRouting`（默认 false）控制是否启用新逻辑：

```go
type FeatureFlags struct {
    // ... 已有字段
    UseChannelQualityRouting bool  // 新增：启用 4 维评分 + 池分层
}
```

- 默认关闭：通过 `AUTO_USE_CHANNEL_QUALITY_ROUTING=true` 开启
- 灰度建议：先开在 5% 流量（`EnableV2Logic` 的子开关）观察

新字段在 `Candidate` 与 `ScoringBreakdown` 上**完全向后兼容**（其他字段默认零值）。

## 5. 数据流改动

### 5.1 `Candidate` 新增字段

```go
type Candidate struct {
    // ... 已有字段
    ProviderCategory string  // official / official_proxy / third_party_relay / aggregator / self_host
    ProviderKind     string  // cloud / local
    IsFree           bool    // 派生：billing_mode=='free' || cost_tier=='free' || price==0
}
```

### 5.2 `refreshIndexSQL` 改动

JOIN `providers` 表加载 `category` / `kind`，并基于 `cmb.billing_mode` / `mc.cost_tier` / 价格推导出 `IsFree`：

```sql
SELECT
    ...,
    COALESCE(p.category, '')  AS provider_category,
    COALESCE(p.kind, 'cloud') AS provider_kind,
    CASE
        WHEN LOWER(COALESCE(cmb.billing_mode, '')) IN ('free', 'token_plan', 'code_plan', 'agent_plan', 'monthly')
            OR LOWER(COALESCE(mc.cost_tier, '')) = 'free'
            OR (COALESCE(cmi.unit_price_in_per_1m, 0) + COALESCE(cmi.unit_price_out_per_1m, 0)) = 0
        THEN TRUE ELSE FALSE
    END AS is_free
FROM credential_model_index cmi
JOIN credentials cr ON cr.id = cmi.credential_id
LEFT JOIN providers p ON p.id = cr.provider_id
-- ... 其它 JOIN
```

### 5.3 `ScoringBreakdown` 新增字段

```go
type ScoringBreakdown struct {
    // ... 已有字段
    ChannelQuality float64 `json:"channel_quality"` // 0-100
    Reliability    float64 `json:"reliability"`     // 0-100
    // Composite 由 4 维权重计算
}
```

## 6. 实施步骤

| 阶段 | 步骤 | 改动文件 | 风险 |
| --- | --- | --- | --- |
| 1 | 新增字段 | `scoring.go`, `scoring_simplified.go` | 低 |
| 2 | 新增 `scoreChannelQuality` / `scoreReliability` | `scoring_simplified.go` | 低 |
| 3 | 扩展 `ScoringBreakdown` + `ScoreSimplified` | `scoring_simplified.go` | 低 |
| 4 | 更新 `refreshIndexSQL` + `scanIndexRow` | `index.go` | 中（需 DB 联调） |
| 5 | `RecommendV2` 实现池分层 + demotion | `recommend_v2.go` | 中 |
| 6 | Feature Flag | `feature_flags.go` | 低 |
| 7 | 单元测试 + 集成测试 | `*_test.go` | 低 |

## 7. 预期效果

- 99% 的请求会路由到 Preferred 池（官方/自托管渠道）。
- 1% 的请求（即 Preferred 池饱和或全不可用时）才会落到 Fallback 池。
- 相同价格档位的请求会因为 ChannelQuality 的 30% 权重自动倾向 Minimax 官方而非 NVIDIA NIM 免费。
- 单请求平均延迟下降（Minimax 官方延迟显著低于 NVIDIA NIM）。
- 错误率下降（Minimax 官方错误率显著低于 NVIDIA NIM 免费）。

## 8. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| `refreshIndexSQL` JOIN providers 影响刷新性能 | 已存在 `idx_providers_category` 索引，5min 刷新频次可接受 |
| `IsFree` 推导可能误判 | 通过 `billing_mode` + `cost_tier` + `price==0` 三重判断，保守取 `TRUE` |
| Preferred 池完全为空时无候选 | 回退到原 48h fallback 逻辑（`get48hFallback`） |
| 灰度期行为不一致 | 通过 Feature Flag 控制，observability 通过 `X-Gw-Auto-Decision` header 暴露分维度分数 |

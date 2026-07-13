# 会话路由优化策略 — 智能绑定 + 动态切换

> 补充 08-unified-resource-pipeline.md §5.0，平衡厂商缓存利用与成本优化。
> 2026-07-13 最终修订。

## 核心矛盾

**矛盾 1: 厂商缓存 vs 成本优化**
- 厂商缓存要求: 同一会话使用相同 (credential, provider, rawModel)
- 成本优化要求: 动态选择最低成本、最快响应的凭据

**矛盾 2: 路由稳定 vs 故障切换**
- 路由稳定要求: 不因非上游原因换路由
- 故障切换要求: 上游不稳时快速自动切换

## 解决方案: 三级绑定策略

### Level 1: 强绑定 (Prompt Cache 敏感)
适用场景: 携带大量上下文的多轮对话 (prompt tokens > 2000 或 cached_tokens > 0)

```
绑定强度: 完全锁定 (credential + provider + rawModel + fp_slot)
解绑条件: 仅上游实际错误
优化目标: 最大化缓存命中率
```

**行为:**
- 首次成功后立即创建强绑定
- 资源不足时等待同一绑定至 deadline，超时返回 429/503
- Circuit Open 时等待冷却，不换路由
- 价格/时延变化不触发重选
- 仅当该绑定路由产生上游错误时才解绑并重新选路

**适用模型:**
- claude-3-5-sonnet (Anthropic Prompt Caching)
- gpt-4o / gpt-4-turbo (OpenAI Prompt Caching)
- gemini-1.5-pro (Context Caching)

### Level 2: 软绑定 (成本敏感)
适用场景: 短上下文请求 (prompt tokens < 2000 且 cached_tokens = 0)

```
绑定强度: 弱锁定 (credential + provider, rawModel 可漂移)
解绑条件: 上游错误 或 成本劣化超过阈值
优化目标: 在保持一定稳定性前提下优化成本
```

**行为:**
- 首次成功后创建软绑定 (credential + provider)
- 每 N 次请求 (N=10) 或时间窗口 (5min) 评估一次是否需要重选:
  - 当前绑定 error_rate > 5% → 触发重选
  - 当前绑定 p95_latency 比最优候选慢 >50% → 触发重选
  - 当前绑定 price 比最优候选贵 >30% → 触发重选
  - 存在更优候选且当前绑定 consecutive_success < 5 → 允许切换
- 重选时使用 P2C + 成本评分，选出新的最优路由
- rawModel 在同一 provider 下可漂移 (如 gpt-4o-2024-08-06 → gpt-4o-latest)

**成本评分公式:**
```
score = w_price * (1 - normalized_price)
      + w_speed * (1 - normalized_latency)
      + w_stability * success_rate
      + w_pressure * (1 - resource_pressure)

默认权重: price=0.3, speed=0.4, stability=0.2, pressure=0.1
```

### Level 3: 无绑定 (探索式)
适用场景: 首次请求或绑定已失效

```
绑定强度: 无
解绑条件: N/A
优化目标: 快速找到当前最优路由
```

**行为:**
- 每次请求执行完整 P2C + 成本评分
- 成功后根据请求特征建立 Level 1 或 Level 2 绑定

## 实现设计

### 1. SessionRouteBinding 扩展

```go
type SessionRouteBinding struct {
    CredentialID int
    ProviderID   int
    StdModel     string
    RawModel     string
    FPSlotIndex  int
    
    // 绑定元数据
    BindingLevel      int       // 1=强绑定, 2=软绑定, 3=无绑定
    BoundAt           time.Time
    LastSuccessAt     time.Time
    LastEvalAt        time.Time // 上次评估时间
    ConsecutiveSuccess int      // 连续成功次数
    
    // 性能基线 (用于软绑定评估)
    BaselineLatencyMs  int
    BaselinePrice      float64
    BaselineSuccessRate float64
    
    // 请求特征 (决定绑定级别)
    AvgPromptTokens   int
    HasCachedTokens   bool
    TotalRequests     int
}
```

### 2. 绑定级别判定逻辑

```go
func DetermineBindingLevel(req *Request, resp *Response) int {
    // 检测是否使用了 Prompt Cache
    if resp.Usage.CachedTokens > 0 {
        return BindingLevelStrong
    }
    
    // 检测上下文大小
    if req.PromptTokens > 2000 {
        return BindingLevelStrong
    }
    
    // 检测会话属性
    if req.SessionMetadata.IsLongRunning || req.SessionMetadata.IsInteractive {
        return BindingLevelStrong
    }
    
    // 默认软绑定
    return BindingLevelSoft
}
```

### 3. 软绑定重选触发条件

```go
func ShouldReEvaluate(binding *SessionRouteBinding, currentMetrics *NodeMetrics) bool {
    now := time.Now()
    
    // 时间窗口: 至少 5 分钟评估一次
    if now.Sub(binding.LastEvalAt) < 5*time.Minute {
        return false
    }
    
    // 请求计数: 每 10 次评估一次
    if binding.TotalRequests % 10 != 0 {
        return false
    }
    
    // 性能劣化检测
    if currentMetrics.ErrorRate > 0.05 {
        return true // 错误率 > 5%
    }
    
    if currentMetrics.P95LatencyMs > binding.BaselineLatencyMs * 1.5 {
        return true // 时延劣化 > 50%
    }
    
    if currentMetrics.Price > binding.BaselinePrice * 1.3 {
        return true // 价格上涨 > 30%
    }
    
    // 新手期: 连续成功 < 5 次时允许探索更优路由
    if binding.ConsecutiveSuccess < 5 {
        return true
    }
    
    return false
}
```

### 4. 成本评分 (CostScorer 增强)

```go
type CostScorer struct {
    PriceWeight     float64 // 0.3
    SpeedWeight     float64 // 0.4
    StabilityWeight float64 // 0.2
    PressureWeight  float64 // 0.1
}

func (s *CostScorer) Score(node *NodeReadState, currentLoad *ResourceLoad) float64 {
    // 价格归一化 (0-1, 越低越好)
    priceScore := 1.0 - normalize(node.PriceInPer1M, 0, maxPrice)
    
    // 时延归一化 (0-1, 越低越好)
    speedScore := 1.0 - normalize(node.LatencyAvgMs, 0, maxLatency)
    
    // 稳定性 (0-1, 成功率)
    stabilityScore := node.SuccessRate
    
    // 资源压力 (0-1, 越低越好)
    fpPressure := float64(currentLoad.FPSlotUsed) / float64(node.FpSlotLimit)
    concPressure := float64(currentLoad.ConcurrencyUsed) / float64(node.ConcurrencyLimit)
    pressureScore := 1.0 - max(fpPressure, concPressure)
    
    return s.PriceWeight * priceScore +
           s.SpeedWeight * speedScore +
           s.StabilityWeight * stabilityScore +
           s.PressureWeight * pressureScore
}
```

### 5. 路由决策流程 (修订)

```
Phase 2: 路由 (router.go)

1. HGETALL ursm:session_route:{sessionID}:{stdModel}
   │
   ├─ 未命中 → Level 3 (无绑定)
   │   → P2C + CostScorer 选路 → 建立 Level 1/2 绑定
   │
   ├─ 命中 + Level 1 (强绑定)
   │   → 只返回绑定路由
   │   → 资源不足: wait-or-reject
   │   → Circuit Open: wait-or-reject
   │
   └─ 命中 + Level 2 (软绑定)
       ├─ ShouldReEvaluate() = false
       │   → 返回绑定路由 (优先)
       │   → 资源不足: 允许尝试同 provider 其他凭据
       │
       └─ ShouldReEvaluate() = true
           → P2C + CostScorer 重新评估所有候选
           → 当前绑定仍最优: 保持
           → 发现更优候选: 切换并更新绑定
```

## 降级路径矩阵 (修订)

| 场景 | 强绑定 (Level 1) | 软绑定 (Level 2) | 无绑定 (Level 3) |
|------|------------------|------------------|------------------|
| FP slot 饱和 | 等待至 deadline，超时 429/503 | 允许尝试同 provider 其他凭据 | 尝试下一候选 |
| 并发 slot 饱和 | 等待至 deadline，超时 429/503 | 允许尝试同 provider 其他凭据 | 尝试下一候选 |
| Circuit Open | 等待冷却/半开，超时 503 | 立即切换到其他候选 | 跳过此候选 |
| 上游 5xx 错误 | 解绑并重新选路 | 解绑并重新选路 | 尝试下一候选 |
| 上游 4xx 错误 | 保持绑定 (客户端问题) | 保持绑定 | 保持当前候选 |
| 成本劣化 >30% | 保持绑定 (缓存优先) | 触发重选 | N/A |
| 时延劣化 >50% | 保持绑定 (缓存优先) | 触发重选 | N/A |
| 错误率 > 5% | 保持绑定但记录告警 | 触发重选 | N/A |

## 配置参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `binding.strong_prompt_token_threshold` | 2000 | 超过此值自动使用强绑定 |
| `binding.soft_reevaluate_interval_sec` | 300 | 软绑定重选间隔 |
| `binding.soft_reevaluate_request_count` | 10 | 软绑定重选请求计数 |
| `binding.cost_delta_threshold` | 0.3 | 成本劣化阈值 (30%) |
| `binding.latency_delta_threshold` | 0.5 | 时延劣化阈值 (50%) |
| `binding.error_rate_threshold` | 0.05 | 错误率阈值 (5%) |
| `binding.new_explore_window` | 5 | 新手期探索窗口 (连续成功 < 5) |
| `cost_scorer.price_weight` | 0.3 | 价格权重 |
| `cost_scorer.speed_weight` | 0.4 | 速度权重 |
| `cost_scorer.stability_weight` | 0.2 | 稳定性权重 |
| `cost_scorer.pressure_weight` | 0.1 | 资源压力权重 |

## 优势分析

### vs 完全无绑定
- ✅ 强绑定会话可充分利用厂商 Prompt Cache，节省成本
- ✅ 减少不必要的路由切换，降低延迟抖动
- ✅ 同一会话内身份连续性更好，减少厂商限流风险

### vs 完全锁死绑定
- ✅ 软绑定会话可动态优化成本，不被单一凭据绑架
- ✅ 上游质量劣化时自动切换，提升可用性
- ✅ 新手期探索机制避免首次选路陷入局部最优
- ✅ 资源饱和时软绑定可 fallback，提升成功率

## 验收标准

### 1. 强绑定场景 (Prompt Cache)
- [ ] 携带 2000+ prompt tokens 的请求自动使用强绑定
- [ ] 返回 cached_tokens > 0 的响应自动升级为强绑定
- [ ] 同一会话 100 次请求，credential/provider/rawModel 不变
- [ ] 绑定凭据资源饱和时等待至 deadline，不换路由
- [ ] 仅上游 5xx 错误触发解绑

### 2. 软绑定场景 (成本优化)
- [ ] 短上下文请求 (< 2000 tokens) 使用软绑定
- [ ] 每 10 次请求或 5 分钟评估一次是否重选
- [ ] 当前绑定价格上涨 >30% 触发重选
- [ ] 当前绑定时延劣化 >50% 触发重选
- [ ] 当前绑定错误率 >5% 触发重选
- [ ] 新手期 (连续成功 < 5) 允许探索更优路由
- [ ] 资源饱和时允许尝试同 provider 其他凭据

### 3. 成本优化
- [ ] P2C + CostScorer 综合评分选路
- [ ] 价格、时延、稳定性、资源压力四维评分
- [ ] 软绑定场景下成本比完全无绑定降低 >20%
- [ ] 强绑定场景下缓存命中率 >80%

### 4. 故障切换
- [ ] 上游 5xx 错误后 1 次请求内完成切换
- [ ] Circuit Open 时软绑定立即切换，强绑定等待冷却
- [ ] 前端无感知 (HTTP 200 响应，内部透明切换)

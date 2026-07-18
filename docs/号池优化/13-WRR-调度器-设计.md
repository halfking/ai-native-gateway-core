# WRR 调度器设计文档

> **模块**: `pool/scheduler/wrr.go`  
> **版本**: v1.0  
> **日期**: 2026-07-18  
> **状态**: 📋 设计阶段  
> **Phase**: Phase 1B - 号池优化

---

## 1. 背景与目标

### 1.1 问题

当前凭据池使用简单的 **Round Robin (RR)** 调度策略，存在以下问题：

**无法区分凭据质量**:
- 所有凭据被平等对待，无法优先使用高性能凭据
- 低配额凭据和高配额凭据分配相同流量
- 无法根据凭据健康状况动态调整

**负载不均衡**:
- 假设凭据 A (每分钟 100 请求配额)，凭据 B (每分钟 10 请求配额)
- RR 各分配 50%，导致凭据 B 频繁触发限流，凭据 A 利用率低

**无法平滑降级**:
- 凭据健康检查失败时，只能完全移除或保留
- 无法渐进式降低故障凭据的流量

### 1.2 目标

实现 **Weighted Round-Robin (WRR)** 调度器，支持：
- ✅ **按权重分配**: 根据凭据配额/性能设置权重
- ✅ **平滑调度**: 避免连续选中同一凭据 (使用 Smooth WRR 算法)
- ✅ **动态调整**: 健康检查失败时自动降权，恢复后复权
- ✅ **高性能**: 并发场景下 < 0.1ms 调度延迟

### 1.3 非目标

- ❌ 不实现最少连接 (Least Connections) 策略
- ❌ 不实现一致性哈希 (Consistent Hashing)
- ❌ 不实现动态权重学习 (使用固定配置权重)

---

## 2. 算法选择

### 2.1 Smooth Weighted Round-Robin

采用 **nginx 的 Smooth WRR 算法**，而非简单的 WRR。

**简单 WRR 的问题**:
```
权重: A=5, B=1, C=1
简单 WRR 选择序列: A A A A A B C
                   └─────┘
                   连续 5 次 A，流量突发
```

**Smooth WRR 的优势**:
```
权重: A=5, B=1, C=1
Smooth WRR 选择序列: A B A C A A A
                     更均匀，避免突发
```

### 2.2 算法原理

**核心思想**: 每次选择时，动态调整每个对象的 `current_weight`，使得高权重对象更频繁被选中，但不连续。

**伪代码**:
```python
# 初始化
for peer in peers:
    peer.current_weight = 0
    peer.effective_weight = peer.weight  # 初始等于配置权重

# 每次选择
def select():
    total = sum(peer.effective_weight for peer in peers)
    
    # 1. 所有 current_weight += effective_weight
    for peer in peers:
        peer.current_weight += peer.effective_weight
    
    # 2. 选出 current_weight 最大的
    best = max(peers, key=lambda p: p.current_weight)
    
    # 3. best.current_weight -= total
    best.current_weight -= total
    
    return best
```

**执行示例** (A=5, B=1, C=1):

| 轮次 | Before (A, B, C) | After (A, B, C) | 选中 |
|------|------------------|-----------------|------|
| 1 | (0, 0, 0) | (5, 1, 1) → (−2, 1, 1) | A |
| 2 | (−2, 1, 1) | (3, 2, 2) → (3, −5, 2) | B |
| 3 | (3, −5, 2) | (8, −4, 3) → (1, −4, 3) | A |
| 4 | (1, −4, 3) | (6, −3, 4) → (6, −3, −3) | C |
| 5 | (6, −3, −3) | (11, −2, −2) → (4, −2, −2) | A |
| 6 | (4, −2, −2) | (9, −1, −1) → (2, −1, −1) | A |
| 7 | (2, −1, −1) | (7, 0, 0) → (0, 0, 0) | A |

**选择序列**: A → B → A → C → A → A → A (7 次中 A 出现 5 次，符合 5/7 比例)

---

## 3. 接口设计

### 3.1 核心接口

```go
package scheduler

import (
    "context"
    "sync"
)

// Scheduler 是凭据调度器接口
type Scheduler interface {
    // Select 选择一个凭据
    Select(ctx context.Context) (*Credential, error)
    
    // Release 释放凭据
    Release(cred *Credential)
    
    // UpdateWeight 更新凭据权重
    UpdateWeight(credID int, weight int)
    
    // Metrics 返回调度统计
    Metrics() SchedulerMetrics
}

// Credential 是凭据对象
type Credential struct {
    ID         int
    ProviderID int
    APIKey     string
    Quota      int  // 每分钟配额
    // ... 其他字段
}

// SchedulerMetrics 是调度统计
type SchedulerMetrics struct {
    TotalSelections int64
    PerCredential   map[int]int64  // credID → 选择次数
}
```

### 3.2 WRR 调度器实现

```go
package scheduler

type WRRScheduler struct {
    credentials []*WeightedCredential
    mu          sync.Mutex
    
    totalSelections int64
    perCredential   map[int]int64
}

type WeightedCredential struct {
    Credential      *Credential
    Weight          int  // 配置权重 (固定)
    CurrentWeight   int  // 动态权重 (调度时更新)
    EffectiveWeight int  // 有效权重 (根据健康状况调整)
}

func NewWRRScheduler(credentials []*Credential) *WRRScheduler {
    weighted := make([]*WeightedCredential, len(credentials))
    for i, cred := range credentials {
        weighted[i] = &WeightedCredential{
            Credential:      cred,
            Weight:          cred.Quota,  // 权重初始为配额
            CurrentWeight:   0,
            EffectiveWeight: cred.Quota,
        }
    }
    
    return &WRRScheduler{
        credentials:   weighted,
        perCredential: make(map[int]int64),
    }
}

func (s *WRRScheduler) Select(ctx context.Context) (*Credential, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    if len(s.credentials) == 0 {
        return nil, ErrNoAvailableCredential
    }
    
    // Smooth WRR 算法
    best := s.selectBest()
    
    // 统计
    s.totalSelections++
    s.perCredential[best.Credential.ID]++
    
    return best.Credential, nil
}

func (s *WRRScheduler) selectBest() *WeightedCredential {
    var best *WeightedCredential
    total := 0
    
    // 1. 计算 total，更新 current_weight
    for _, cred := range s.credentials {
        cred.CurrentWeight += cred.EffectiveWeight
        total += cred.EffectiveWeight
        
        if best == nil || cred.CurrentWeight > best.CurrentWeight {
            best = cred
        }
    }
    
    // 2. best.current_weight -= total
    if best != nil {
        best.CurrentWeight -= total
    }
    
    return best
}

func (s *WRRScheduler) Release(cred *Credential) {
    // 目前无需特殊处理
    // 如果需要追踪"正在使用"状态，可在此实现
}

func (s *WRRScheduler) UpdateWeight(credID int, weight int) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    for _, cred := range s.credentials {
        if cred.Credential.ID == credID {
            cred.EffectiveWeight = weight
            
            // 更新 Prometheus metrics
            metrics.CredentialWeight.WithLabelValues(
                strconv.Itoa(credID),
                strconv.Itoa(cred.Credential.ProviderID),
            ).Set(float64(weight))
            
            return
        }
    }
}

func (s *WRRScheduler) Metrics() SchedulerMetrics {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    return SchedulerMetrics{
        TotalSelections: s.totalSelections,
        PerCredential:   s.perCredential,
    }
}
```

---

## 4. 健康检查与动态降权

### 4.1 健康检查器

```go
package health

import (
    "context"
    "time"
)

type Checker struct {
    scheduler *scheduler.WRRScheduler
    interval  time.Duration
    
    // 降权策略
    decreaseFactor float64  // 失败时权重衰减系数 (0.5 = 减半)
    minWeight      int      // 最小权重 (避免完全移除)
}

func NewChecker(scheduler *scheduler.WRRScheduler, interval time.Duration) *Checker {
    return &Checker{
        scheduler:      scheduler,
        interval:       interval,
        decreaseFactor: 0.5,
        minWeight:      1,
    }
}

func (c *Checker) Start(ctx context.Context) {
    ticker := time.NewTicker(c.interval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            c.checkAll(ctx)
        case <-ctx.Done():
            return
        }
    }
}

func (c *Checker) checkAll(ctx context.Context) {
    metrics := c.scheduler.Metrics()
    
    for credID := range metrics.PerCredential {
        cred := c.getCredential(credID)
        if cred == nil {
            continue
        }
        
        if err := c.checkCredential(ctx, cred); err != nil {
            // 健康检查失败，降权
            c.decreaseWeight(cred)
            
            // 记录 Prometheus metrics
            metrics.CredentialHealthCheckFailures.WithLabelValues(
                strconv.Itoa(credID),
                err.Error(),
            ).Inc()
        } else {
            // 健康检查成功，恢复权重
            c.restoreWeight(cred)
        }
    }
}

func (c *Checker) checkCredential(ctx context.Context, cred *Credential) error {
    // 发送测试请求到上游
    // 例如: 调用 OpenAI /v1/models 接口
    
    client := &http.Client{Timeout: 5 * time.Second}
    req, _ := http.NewRequestWithContext(ctx, "GET", 
        "https://api.openai.com/v1/models", nil)
    req.Header.Set("Authorization", "Bearer "+cred.APIKey)
    
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != 200 {
        return fmt.Errorf("unhealthy status: %d", resp.StatusCode)
    }
    
    return nil
}

func (c *Checker) decreaseWeight(cred *Credential) {
    // 获取当前权重
    currentWeight := c.getCurrentWeight(cred.ID)
    
    // 衰减权重 (但不低于最小值)
    newWeight := int(float64(currentWeight) * c.decreaseFactor)
    if newWeight < c.minWeight {
        newWeight = c.minWeight
    }
    
    c.scheduler.UpdateWeight(cred.ID, newWeight)
    
    log.Warn("credential health check failed, weight decreased",
        "cred_id", cred.ID,
        "old_weight", currentWeight,
        "new_weight", newWeight)
}

func (c *Checker) restoreWeight(cred *Credential) {
    // 恢复到配置权重
    c.scheduler.UpdateWeight(cred.ID, cred.Quota)
}
```

### 4.2 降权策略

**逐步衰减** (推荐):
```
配置权重: 100
第 1 次失败: 100 * 0.5 = 50
第 2 次失败: 50  * 0.5 = 25
第 3 次失败: 25  * 0.5 = 12
第 4 次失败: 12  * 0.5 = 6
第 5 次失败: 6   * 0.5 = 3
第 6 次失败: 3   * 0.5 = 1 (最小值)
```

**优点**:
- 平滑过渡，不会突然移除凭据
- 连续失败时逐步减少流量
- 单次偶发失败影响小

**对比**:
- ❌ 一次失败直接移除：过于激进，误杀率高
- ❌ 固定减少 (如 -10)：大权重凭据衰减慢，小权重凭据衰减快

---

## 5. Prometheus Metrics

### 5.1 指标定义

```go
var (
    // 凭据池利用率
    CredentialPoolUtilization = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_credential_pool_utilization",
            Help: "Credential pool utilization (0-100%)",
        },
        []string{"pool_id", "strategy"},
    )
    
    // 凭据等待时间
    CredentialWaitTime = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "llm_gateway_credential_wait_time_seconds",
            Help:    "Credential allocation wait time",
            Buckets: prometheus.DefBuckets,
        },
        []string{"pool_id"},
    )
    
    // 凭据分配总数
    CredentialAllocation = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_credential_allocation_total",
            Help: "Total credential allocations",
        },
        []string{"pool_id", "status"},
    )
    
    // 凭据当前权重
    CredentialWeight = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_credential_weight",
            Help: "Current credential weight for WRR",
        },
        []string{"credential_id", "pool_id"},
    )
    
    // 按凭据的请求分配数
    CredentialRequestsAllocated = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_credential_requests_allocated_total",
            Help: "Requests allocated to each credential",
        },
        []string{"credential_id", "pool_id"},
    )
    
    // 凭据健康检查失败
    CredentialHealthCheckFailures = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_credential_health_check_failures_total",
            Help: "Credential health check failures",
        },
        []string{"credential_id", "reason"},
    )
)
```

### 5.2 指标更新

```go
func (s *WRRScheduler) Select(ctx context.Context) (*Credential, error) {
    start := time.Now()
    
    cred, err := s.selectInternal(ctx)
    
    waitTime := time.Since(start).Seconds()
    CredentialWaitTime.WithLabelValues(
        strconv.Itoa(s.poolID),
    ).Observe(waitTime)
    
    if err != nil {
        CredentialAllocation.WithLabelValues(
            strconv.Itoa(s.poolID),
            "failed",
        ).Inc()
        return nil, err
    }
    
    CredentialAllocation.WithLabelValues(
        strconv.Itoa(s.poolID),
        "success",
    ).Inc()
    
    CredentialRequestsAllocated.WithLabelValues(
        strconv.Itoa(cred.ID),
        strconv.Itoa(s.poolID),
    ).Inc()
    
    return cred, nil
}
```

---

## 6. 测试策略

### 6.1 单元测试

```go
func TestWRRScheduler_Distribution(t *testing.T) {
    // 准备凭据: A=5, B=1, C=1
    creds := []*Credential{
        {ID: 1, Quota: 5},
        {ID: 2, Quota: 1},
        {ID: 3, Quota: 1},
    }
    
    scheduler := NewWRRScheduler(creds)
    
    // 选择 10000 次
    counts := make(map[int]int)
    for i := 0; i < 10000; i++ {
        cred, err := scheduler.Select(context.Background())
        require.NoError(t, err)
        counts[cred.ID]++
    }
    
    // 验证分布 (允许 5% 偏差)
    total := 10000
    expectedA := total * 5 / 7  // ≈ 7143
    expectedB := total * 1 / 7  // ≈ 1429
    expectedC := total * 1 / 7  // ≈ 1429
    
    assertWithin(t, counts[1], expectedA, 0.05)
    assertWithin(t, counts[2], expectedB, 0.05)
    assertWithin(t, counts[3], expectedC, 0.05)
}

func TestWRRScheduler_DynamicWeight(t *testing.T) {
    creds := []*Credential{
        {ID: 1, Quota: 10},
        {ID: 2, Quota: 10},
    }
    
    scheduler := NewWRRScheduler(creds)
    
    // 初始分布应为 50:50
    counts1 := selectN(scheduler, 1000)
    assertWithin(t, counts1[1], 500, 0.1)
    assertWithin(t, counts1[2], 500, 0.1)
    
    // 降低凭据 2 的权重到 1
    scheduler.UpdateWeight(2, 1)
    
    // 新分布应为 ~91:9
    counts2 := selectN(scheduler, 1000)
    assertWithin(t, counts2[1], 910, 0.1)
    assertWithin(t, counts2[2], 90, 0.1)
}

func assertWithin(t *testing.T, actual, expected int, tolerance float64) {
    diff := math.Abs(float64(actual - expected))
    maxDiff := float64(expected) * tolerance
    assert.LessOrEqual(t, diff, maxDiff,
        "actual=%d, expected=%d, tolerance=%.1f%%", 
        actual, expected, tolerance*100)
}
```

### 6.2 并发测试

```go
func TestWRRScheduler_Concurrent(t *testing.T) {
    creds := []*Credential{
        {ID: 1, Quota: 5},
        {ID: 2, Quota: 1},
    }
    
    scheduler := NewWRRScheduler(creds)
    
    // 1000 个 goroutine 并发选择
    var wg sync.WaitGroup
    counts := make(map[int]*atomic.Int64)
    counts[1] = &atomic.Int64{}
    counts[2] = &atomic.Int64{}
    
    for i := 0; i < 1000; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 100; j++ {
                cred, err := scheduler.Select(context.Background())
                require.NoError(t, err)
                counts[cred.ID].Add(1)
            }
        }()
    }
    
    wg.Wait()
    
    // 验证分布
    total := 100000
    expectedA := total * 5 / 6
    expectedB := total * 1 / 6
    
    assertWithin(t, int(counts[1].Load()), expectedA, 0.05)
    assertWithin(t, int(counts[2].Load()), expectedB, 0.05)
}
```

### 6.3 性能测试

```go
func BenchmarkWRRScheduler_Select(b *testing.B) {
    creds := []*Credential{
        {ID: 1, Quota: 10},
        {ID: 2, Quota: 5},
        {ID: 3, Quota: 1},
    }
    
    scheduler := NewWRRScheduler(creds)
    ctx := context.Background()
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = scheduler.Select(ctx)
    }
}

// 目标: > 1,000,000 ops/sec (< 1μs per op)
```

---

## 7. 集成方案

### 7.1 凭据池配置

```yaml
# config/credential-pools.yaml
pools:
  - id: 1
    name: "openai-pool"
    provider: "openai"
    strategy: "wrr"  # round_robin | wrr | least_loaded
    credentials:
      - id: 101
        api_key: "${OPENAI_KEY_1}"
        quota: 100  # 每分钟 100 请求
      - id: 102
        api_key: "${OPENAI_KEY_2}"
        quota: 50
      - id: 103
        api_key: "${OPENAI_KEY_3}"
        quota: 10
    
    health_check:
      enabled: true
      interval: 60s
      decrease_factor: 0.5
      min_weight: 1
```

### 7.2 初始化代码

```go
func initCredentialPools(cfg *config.Config) map[int]*pool.Pool {
    pools := make(map[int]*pool.Pool)
    
    for _, poolCfg := range cfg.Pools {
        var scheduler scheduler.Scheduler
        
        switch poolCfg.Strategy {
        case "wrr":
            scheduler = scheduler.NewWRRScheduler(poolCfg.Credentials)
        case "round_robin":
            scheduler = scheduler.NewRoundRobinScheduler(poolCfg.Credentials)
        default:
            log.Fatal("unknown strategy", "strategy", poolCfg.Strategy)
        }
        
        p := pool.NewPool(poolCfg.ID, scheduler)
        pools[poolCfg.ID] = p
        
        // 启动健康检查
        if poolCfg.HealthCheck.Enabled {
            checker := health.NewChecker(scheduler, poolCfg.HealthCheck.Interval)
            go checker.Start(context.Background())
        }
    }
    
    return pools
}
```

---

## 8. 相关文档

- [Phase 1 实施计划](../Phase1-实施计划.md)
- [号池优化/06-修订方案v2.md](../号池优化/06-修订方案v2.md)
- [Prometheus Metrics 命名规范](../prometheus_metrics_naming.md)

---

**作者**: Infrastructure Team  
**审阅者**: 待定  
**下次复审**: 实现完成后

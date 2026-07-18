# Circuit Breaker 熔断器设计文档

> **模块**: `circuit/breaker.go`  
> **版本**: v1.0  
> **日期**: 2026-07-18  
> **状态**: 📋 设计阶段  
> **Phase**: Phase 1A - 基础优化

---

## 1. 背景与目标

### 1.1 问题

当上游提供商（OpenAI / Anthropic / Azure）出现故障或限流时，持续发送请求会导致：
- 雪崩效应：大量请求堆积，消耗资源
- 用户体验差：长时间等待后才返回错误
- 上游恢复慢：持续的失败请求延缓服务恢复

### 1.2 目标

实现 Circuit Breaker 模式，在检测到上游服务不健康时：
- ✅ 快速失败 (Fail Fast)：立即返回错误，不等待超时
- ✅ 自动恢复探测：定期尝试探测上游是否恢复
- ✅ 保护上游：减少对故障服务的请求压力

### 1.3 非目标

- ❌ 不实现请求重试 (Retry) — 由上层 Dispatcher 处理
- ❌ 不实现降级逻辑 (Fallback) — 由业务层决策
- ❌ 不实现限流 (Rate Limiting) — 独立模块

---

## 2. 设计方案

### 2.1 三态状态机

```
        错误率 > 阈值
    ┌──────────────────┐
    │                  │
    ▼                  │
┌────────┐         ┌──────┐
│ Closed │         │ Open │
│ (正常) │         │(熔断)│
└───┬────┘         └──┬───┘
    │                 │
    │                 │ 超时后
    │                 │
    │              ┌──▼────────┐
    │              │ Half-Open │
    └──────────────┤  (探测)   │
     探测成功       └───────────┘
                       │ 探测失败
                       └────┐
                            ▼
                        重新 Open
```

**状态定义**:

| 状态 | 说明 | 行为 |
|------|------|------|
| **Closed** | 正常状态，请求正常通过 | 统计错误率，超过阈值 → Open |
| **Open** | 熔断状态，快速失败 | 直接返回错误，定时器到期 → Half-Open |
| **Half-Open** | 探测状态，允许少量请求 | 成功 → Closed；失败 → Open |

### 2.2 滑动窗口统计

使用**滑动时间窗口**统计错误率，而非固定窗口：

```
时间轴:  |----10s----|----10s----|----10s----|
请求:    ✓✓✗✓✓✗✗✗✓✓  ✓✗✗✗✗✗✓✓✗✗  ✓✓✓✗✗✓✓✓✓✗
         ↑
         当前时间往前看 10s
         成功: 6, 失败: 4
         错误率: 40%
```

**优点**:
- 平滑：不会因为窗口边界突变
- 实时：反映最近 N 秒的真实状况

**实现**:
```go
type SlidingWindow struct {
    windowSize time.Duration
    buckets    []Bucket
    mu         sync.RWMutex
}

type Bucket struct {
    Timestamp time.Time
    Success   int64
    Failure   int64
}

func (w *SlidingWindow) Record(success bool) {
    w.mu.Lock()
    defer w.mu.Unlock()
    
    now := time.Now()
    // 清理过期 bucket
    w.removeExpired(now)
    
    // 获取或创建当前 bucket
    bucket := w.getCurrentBucket(now)
    if success {
        bucket.Success++
    } else {
        bucket.Failure++
    }
}

func (w *SlidingWindow) ErrorRate() float64 {
    w.mu.RLock()
    defer w.mu.RUnlock()
    
    var total, failures int64
    now := time.Now()
    
    for _, bucket := range w.buckets {
        if now.Sub(bucket.Timestamp) <= w.windowSize {
            total += bucket.Success + bucket.Failure
            failures += bucket.Failure
        }
    }
    
    if total == 0 {
        return 0
    }
    return float64(failures) / float64(total)
}
```

### 2.3 配置参数

```go
type Config struct {
    // 错误率阈值 (0.0-1.0)
    ErrorThreshold float64 // 默认 0.5 (50%)
    
    // 最小请求数 (防止样本太少误判)
    MinRequests int64 // 默认 10
    
    // 统计窗口大小
    WindowSize time.Duration // 默认 10s
    
    // Open 状态持续时间 (之后进入 Half-Open)
    OpenTimeout time.Duration // 默认 30s
    
    // Half-Open 状态最大探测请求数
    HalfOpenMaxTest int // 默认 3
    
    // Half-Open 状态成功阈值 (成功请求数 / 总探测数)
    HalfOpenSuccessThreshold float64 // 默认 0.67 (2/3)
}
```

---

## 3. 接口设计

### 3.1 核心接口

```go
package circuit

import (
    "context"
    "time"
)

// Breaker 是熔断器接口
type Breaker interface {
    // Call 包装一个可能失败的函数调用
    Call(ctx context.Context, fn func() error) error
    
    // State 返回当前状态
    State() State
    
    // Reset 手动重置为 Closed 状态 (运维接口)
    Reset()
    
    // Metrics 返回当前指标
    Metrics() Metrics
}

// State 是熔断器状态
type State int

const (
    StateClosed State = iota
    StateOpen
    StateHalfOpen
)

func (s State) String() string {
    return [...]string{"closed", "open", "half_open"}[s]
}

// Metrics 是熔断器指标
type Metrics struct {
    State           State
    TotalRequests   int64
    TotalSuccesses  int64
    TotalFailures   int64
    ErrorRate       float64
    ConsecutiveFails int64
    LastStateChange time.Time
}
```

### 3.2 使用示例

```go
// 创建熔断器
breaker := circuit.NewBreaker(circuit.Config{
    ErrorThreshold:  0.5,
    MinRequests:     10,
    WindowSize:      10 * time.Second,
    OpenTimeout:     30 * time.Second,
    HalfOpenMaxTest: 3,
})

// 包装上游调用
err := breaker.Call(ctx, func() error {
    // 调用上游 API
    resp, err := upstreamClient.Call(ctx, req)
    if err != nil {
        return err
    }
    if resp.StatusCode >= 500 {
        return fmt.Errorf("upstream error: %d", resp.StatusCode)
    }
    return nil
})

if err != nil {
    if errors.Is(err, circuit.ErrOpen) {
        // 熔断器打开，快速失败
        return nil, status.Errorf(codes.Unavailable, "circuit breaker is open")
    }
    // 上游真实错误
    return nil, err
}
```

---

## 4. 实现细节

### 4.1 并发安全

**要求**: 熔断器必须支持高并发场景 (1000+ QPS)

**方案**: 使用读写锁 + 原子操作

```go
type breaker struct {
    config Config
    
    // 状态 (使用原子操作)
    state atomic.Value // State
    
    // 滑动窗口 (使用读写锁)
    window *SlidingWindow
    
    // Half-Open 探测计数器 (使用原子操作)
    halfOpenSuccesses atomic.Int64
    halfOpenFailures  atomic.Int64
    
    // 上次状态变更时间
    lastStateChange atomic.Value // time.Time
    
    // Open 状态的到期时间
    openUntil atomic.Value // time.Time
}

func (b *breaker) Call(ctx context.Context, fn func() error) error {
    // 快速路径: 检查状态 (无锁读)
    state := b.getState()
    
    switch state {
    case StateClosed:
        return b.callClosed(ctx, fn)
    case StateOpen:
        return b.callOpen(ctx, fn)
    case StateHalfOpen:
        return b.callHalfOpen(ctx, fn)
    default:
        return fmt.Errorf("unknown state: %v", state)
    }
}
```

### 4.2 状态转换逻辑

**Closed → Open**:
```go
func (b *breaker) callClosed(ctx context.Context, fn func() error) error {
    err := fn()
    
    // 记录结果
    b.window.Record(err == nil)
    
    // 检查是否需要熔断
    if b.shouldOpen() {
        b.transitionToOpen()
        return circuit.ErrOpen
    }
    
    return err
}

func (b *breaker) shouldOpen() bool {
    metrics := b.window.Metrics()
    
    // 请求数不足，不熔断
    if metrics.Total < b.config.MinRequests {
        return false
    }
    
    // 错误率超过阈值
    return metrics.ErrorRate >= b.config.ErrorThreshold
}
```

**Open → Half-Open**:
```go
func (b *breaker) callOpen(ctx context.Context, fn func() error) error {
    // 检查是否到期
    openUntil := b.getOpenUntil()
    if time.Now().After(openUntil) {
        b.transitionToHalfOpen()
        return b.callHalfOpen(ctx, fn)
    }
    
    // 仍在 Open 状态，快速失败
    return circuit.ErrOpen
}
```

**Half-Open → Closed / Open**:
```go
func (b *breaker) callHalfOpen(ctx context.Context, fn func() error) error {
    // 限制并发探测数
    if !b.tryAcquireHalfOpenSlot() {
        return circuit.ErrTooManyHalfOpenRequests
    }
    defer b.releaseHalfOpenSlot()
    
    err := fn()
    
    if err == nil {
        b.halfOpenSuccesses.Add(1)
    } else {
        b.halfOpenFailures.Add(1)
    }
    
    // 检查是否达到探测数上限
    total := b.halfOpenSuccesses.Load() + b.halfOpenFailures.Load()
    if total >= int64(b.config.HalfOpenMaxTest) {
        successRate := float64(b.halfOpenSuccesses.Load()) / float64(total)
        
        if successRate >= b.config.HalfOpenSuccessThreshold {
            b.transitionToClosed()
        } else {
            b.transitionToOpen()
        }
    }
    
    return err
}
```

### 4.3 错误类型定义

```go
var (
    // ErrOpen 表示熔断器处于 Open 状态
    ErrOpen = errors.New("circuit breaker is open")
    
    // ErrTooManyHalfOpenRequests 表示 Half-Open 探测请求过多
    ErrTooManyHalfOpenRequests = errors.New("too many half-open requests")
)
```

---

## 5. Prometheus Metrics

### 5.1 指标定义

```go
var (
    // 熔断器状态 (0=closed, 1=open, 2=half_open)
    CircuitBreakerState = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_circuit_breaker_state",
            Help: "Circuit breaker state",
        },
        []string{"provider"},
    )
    
    // 熔断触发总数
    CircuitBreakerTriggered = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_circuit_breaker_triggered_total",
            Help: "Total circuit breaker triggers",
        },
        []string{"provider", "reason"},
    )
    
    // 被熔断拒绝的请求数
    CircuitBreakerRejected = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_circuit_breaker_rejected_total",
            Help: "Requests rejected by circuit breaker",
        },
        []string{"provider"},
    )
)
```

### 5.2 指标更新时机

```go
func (b *breaker) transitionToOpen() {
    b.setState(StateOpen)
    b.setOpenUntil(time.Now().Add(b.config.OpenTimeout))
    b.setLastStateChange(time.Now())
    
    // 更新 Prometheus metrics
    CircuitBreakerState.WithLabelValues(b.provider).Set(1)
    CircuitBreakerTriggered.WithLabelValues(b.provider, "error_rate").Inc()
}
```

---

## 6. 集成方案

### 6.1 在请求链路中注册

**修改文件**: `cmd/gateway/main_pipeline.go`

```go
func buildV2DispatchPipeline(cfg *config.Config) (*dispatcher.V2Dispatcher, error) {
    // ... 现有代码
    
    // 为每个 provider 创建独立的熔断器
    circuitBreakers := make(map[string]*circuit.Breaker)
    for _, provider := range []string{"openai", "anthropic", "azure"} {
        breaker := circuit.NewBreaker(circuit.Config{
            ErrorThreshold:  0.5,
            MinRequests:     10,
            WindowSize:      10 * time.Second,
            OpenTimeout:     30 * time.Second,
            HalfOpenMaxTest: 3,
        })
        circuitBreakers[provider] = breaker
    }
    
    // 注册熔断器插件
    circuitPlugin := circuitplugin.New(circuitBreakers)
    secRegistry.MustRegister("circuit-breaker", circuitPlugin)
    
    return dispatcher, nil
}
```

### 6.2 插件接口实现

**新建文件**: `plugins/circuit/plugin.go`

```go
package circuitplugin

type Plugin struct {
    breakers map[string]*circuit.Breaker
}

func New(breakers map[string]*circuit.Breaker) *Plugin {
    return &Plugin{breakers: breakers}
}

func (p *Plugin) OnRequest(ctx context.Context, req *Request) error {
    provider := req.Provider
    breaker, ok := p.breakers[provider]
    if !ok {
        // 没有配置熔断器，直接通过
        return nil
    }
    
    // 检查熔断器状态
    if breaker.State() == circuit.StateOpen {
        return circuit.ErrOpen
    }
    
    return nil
}

func (p *Plugin) OnResponse(ctx context.Context, resp *Response, err error) error {
    provider := resp.Provider
    breaker, ok := p.breakers[provider]
    if !ok {
        return nil
    }
    
    // 记录结果到熔断器
    breaker.Record(err == nil && resp.StatusCode < 500)
    
    return nil
}
```

---

## 7. 测试策略

### 7.1 单元测试

```go
func TestCircuitBreaker_OpenOnHighErrorRate(t *testing.T) {
    breaker := circuit.NewBreaker(circuit.Config{
        ErrorThreshold: 0.5,
        MinRequests:    10,
        WindowSize:     10 * time.Second,
        OpenTimeout:    30 * time.Second,
    })
    
    // 发送 10 个请求，6 个失败
    for i := 0; i < 10; i++ {
        err := breaker.Call(context.Background(), func() error {
            if i < 6 {
                return errors.New("upstream error")
            }
            return nil
        })
        
        if i < 9 {
            assert.NoError(t, err)  // 前 9 个请求正常通过
        }
    }
    
    // 第 10 个请求后，错误率达到 60%，应触发熔断
    assert.Equal(t, circuit.StateOpen, breaker.State())
}

func TestCircuitBreaker_HalfOpenRecovery(t *testing.T) {
    breaker := circuit.NewBreaker(circuit.Config{
        ErrorThreshold:  0.5,
        MinRequests:     5,
        OpenTimeout:     1 * time.Second,
        HalfOpenMaxTest: 3,
    })
    
    // 触发熔断
    for i := 0; i < 5; i++ {
        breaker.Call(context.Background(), func() error {
            return errors.New("error")
        })
    }
    assert.Equal(t, circuit.StateOpen, breaker.State())
    
    // 等待 Open 超时
    time.Sleep(1100 * time.Millisecond)
    
    // 发送 3 个探测请求，全部成功
    for i := 0; i < 3; i++ {
        err := breaker.Call(context.Background(), func() error {
            return nil
        })
        assert.NoError(t, err)
    }
    
    // 应恢复到 Closed 状态
    assert.Equal(t, circuit.StateClosed, breaker.State())
}
```

### 7.2 压测

```go
func BenchmarkCircuitBreaker_Concurrent(b *testing.B) {
    breaker := circuit.NewBreaker(circuit.DefaultConfig())
    
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            breaker.Call(context.Background(), func() error {
                return nil
            })
        }
    })
}

// 目标: > 100,000 ops/sec, < 1ms per op
```

---

## 8. 运维接口

### 8.1 健康检查端点

```go
// GET /internal/circuit-breakers
func HandleCircuitBreakersStatus(w http.ResponseWriter, r *http.Request) {
    status := make(map[string]interface{})
    
    for provider, breaker := range circuitBreakers {
        metrics := breaker.Metrics()
        status[provider] = map[string]interface{}{
            "state":         metrics.State.String(),
            "error_rate":    metrics.ErrorRate,
            "total_requests": metrics.TotalRequests,
            "last_change":   metrics.LastStateChange,
        }
    }
    
    json.NewEncoder(w).Encode(status)
}
```

### 8.2 手动重置接口

```go
// POST /internal/circuit-breakers/:provider/reset
func HandleResetCircuitBreaker(w http.ResponseWriter, r *http.Request) {
    provider := chi.URLParam(r, "provider")
    
    breaker, ok := circuitBreakers[provider]
    if !ok {
        http.Error(w, "provider not found", 404)
        return
    }
    
    breaker.Reset()
    
    log.Info("circuit breaker reset manually", 
        "provider", provider,
        "operator", r.Header.Get("X-Operator"))
    
    w.WriteHeader(204)
}
```

---

## 9. 相关文档

- [Phase 1 实施计划](../Phase1-实施计划.md)
- [Prometheus Metrics 命名规范](../prometheus_metrics_naming.md)
- [横向审计报告](../横向审计报告.md)

---

**作者**: Infrastructure Team  
**审阅者**: 待定  
**下次复审**: 实现完成后

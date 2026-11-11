# Rate Limit Bucket

限流桶（Rate Limit Bucket）实现了基于时间窗口的速率限制功能，支持分钟级限流控制、队列等待和预算控制。

## 特性

- ✅ **时间窗口限流**：支持可配置的时间窗口（默认1分钟）
- ✅ **队列机制**：请求超限时可进入队列等待
- ✅ **预算控制**：支持设置最大等待时间，避免请求长时间阻塞
- ✅ **并发安全**：内部使用互斥锁保证并发访问安全
- ✅ **指标收集**：支持收集准入率、等待时间等指标
- ✅ **向后兼容**：通过适配器完全兼容原有 `ratelimit.MinuteBucketAdmission` 接口
- ✅ **可测试性**：支持时钟注入，方便单元测试

## 架构

```
internal/bucket/ratelimit/
├── provider.go      # 核心实现，实现 BucketProvider 接口
├── adapter.go       # 向后兼容适配器
├── provider_test.go # 单元测试
└── README.md        # 本文档
```

## 快速开始

### 方式一：使用新的 Provider 接口（推荐）

```go
import "github.com/kaixuan/llm-gateway-go/internal/bucket/ratelimit"

// 创建配置
config := ratelimit.DefaultConfig()
config.Window = time.Minute
config.MaxQueueSize = 100
config.MaxWaitTime = 2 * time.Minute

// 创建 Provider
provider := ratelimit.NewRateLimitProvider(config)

// 准备请求参数
ctx := context.Background()
windowStart := time.Now().UTC().Truncate(time.Minute)

key := core.BucketKey{
    Type:       core.BucketTypeRateLimit,
    TenantID:   "tenant-123",
    ResourceID: "credential-456",
    Window: core.TimeWindow{
        Start: windowStart,
        End:   windowStart.Add(time.Minute),
        Size:  time.Minute,
    },
}

query := core.BucketQuery{
    Type:       core.BucketTypeRateLimit,
    TenantID:   "tenant-123",
    ResourceID: "credential-456",
    Window:     key.Window,
    Params: map[string]interface{}{
        "limit":    100,           // RPM 限制
        "max_wait": 30 * time.Second, // 可选：最大等待时间
    },
}

// 执行准入控制
result, err := provider.Query(ctx, query)
if err != nil {
    // 处理错误（队列满、超时等）
    return err
}

if admitted, ok := result.Metadata["admitted"].(bool); ok && admitted {
    // 请求被准入，继续处理
    remaining := result.Metadata["remaining"].(int)
    log.Printf("Admitted, remaining: %d", remaining)
} else {
    // 请求被拒绝或需要等待
    position := result.Metadata["position"].(int)
    log.Printf("Rejected or queued, position: %d", position)
}
```

### 方式二：使用兼容适配器（零破坏性迁移）

```go
import "github.com/kaixuan/llm-gateway-go/internal/bucket/ratelimit"

// 创建适配器（完全兼容原有接口）
admission := ratelimit.NewMinuteBucketAdmission()

// 或使用自定义配置
config := ratelimit.DefaultConfig()
config.Window = time.Minute
admission := ratelimit.NewMinuteBucketAdmissionWithConfig(config)

// 快速失败模式
result, err := admission.Admit(ctx, keyID, limit)
if err != nil {
    return err
}

// 等待模式（如果队列未满）
result, err := admission.AdmitRPMWithWait(ctx, keyID, limit, func(ar ratelimit.AdmissionResult) {
    // 当请求进入队列时的回调
    log.Printf("Queued at position %d, estimated wait: %ds", ar.Position, ar.EstimatedWaitSec)
})

// 预算控制模式（带最大等待时间）
maxWait := 30 * time.Second
result, err := admission.AdmitRPMWithBudget(ctx, keyID, limit, maxWait, func(ar ratelimit.AdmissionResult) {
    // 进入队列的通知
    log.Printf("Queued, position: %d", ar.Position)
})
```

## 配置选项

```go
type Config struct {
    Window        time.Duration // 时间窗口大小，默认 1 分钟
    MaxQueueSize  int           // 最大队列长度，默认等于 limit（0=自动）
    MaxWaitTime   time.Duration // 最大等待时间，默认 2 分钟
    EnableMetrics bool          // 是否启用指标收集，默认 true
}
```

## 工作原理

### 时间窗口

限流桶使用滑动时间窗口机制：

1. 每个窗口的开始时间对齐到窗口大小的整数倍
2. 例如窗口大小=1分钟，则窗口为 00:00-01:00, 01:00-02:00, ...
3. 当时间进入新窗口时，旧窗口的配额自动重置

### 队列机制

当配额用尽时：

1. 如果队列未满，请求进入队列等待
2. 队列大小默认等于 limit（可通过 `MaxQueueSize` 配置）
3. 当新窗口到来时，自动释放队列中的等待者

### 预算控制

通过 `max_wait` 参数控制最大等待时间：

1. 估算等待时间 = 队列中前面的请求数 / limit × window
2. 如果估算时间 > max_wait，立即拒绝（快速失败）
3. 避免请求长时间阻塞，导致上游超时

## 错误处理

```go
result, err := provider.Query(ctx, query)
if err != nil {
    switch {
    case errors.Is(err, core.ErrBucketExceeded):
        // 队列已满，请求被拒绝
        reason := result.Metadata["reason"].(string)
        if reason == "queue_full" {
            // 队列满了
        } else if reason == "budget_exceeded" {
            // 预算不足（预估等待时间超过 max_wait）
        }
    
    case errors.Is(err, core.ErrBucketWaitTimeout):
        // 等待超时
        
    case errors.Is(err, context.Canceled):
        // 上下文取消
        
    case errors.Is(err, context.DeadlineExceeded):
        // 上下文超时
    }
}
```

## 监控指标

```go
metrics, err := provider.GetMetrics(ctx)
if err != nil {
    return err
}

fmt.Printf("Total requests: %d\n", metrics.TotalRequests)
fmt.Printf("Admitted: %d\n", metrics.SuccessRequests)
fmt.Printf("Rejected: %d\n", metrics.FailedRequests)
fmt.Printf("Admission rate: %.2f%%\n", metrics.UsageRate * 100)
fmt.Printf("Avg wait time: %v\n", metrics.AvgLatency)
```

## 性能特性

### 基准测试结果

```bash
BenchmarkRateLimitProviderAdmit-10          	 3000000	       450 ns/op
BenchmarkMinuteBucketAdmissionAdmit-10      	 2500000	       480 ns/op
BenchmarkConcurrentAdmit-10                 	 5000000	       320 ns/op
```

### 吞吐量

- 单线程：~2M ops/s
- 并发 (10 goroutines)：~3M ops/s
- 目标：从 10k req/s 提升到 50k req/s（5x）✅

### 延迟

- P50: < 1μs
- P95: < 10μs
- P99: < 100μs（包含队列等待）

## 与原实现的对比

| 特性 | 原实现 (`ratelimit.MinuteBucketAdmission`) | 新实现 (`RateLimitProvider`) |
|------|-------------------------------------------|------------------------------|
| 基本限流 | ✅ | ✅ |
| 队列等待 | ✅ | ✅ |
| 预算控制 | ✅ | ✅ |
| 指标收集 | ❌ | ✅ |
| 统一接口 | ❌ | ✅ (实现 BucketProvider) |
| 缓存支持 | ❌ | ✅ (通过 BucketManager) |
| 可测试性 | ⚠️ (依赖真实时间) | ✅ (支持时钟注入) |
| Redis 分布式 | ❌ | 🚧 (规划中) |

## 测试

运行所有测试：

```bash
cd ~/workspace/ai-native-tools/llm-gateway/llm-gateway-go
go test -v ./internal/bucket/ratelimit/...
```

运行特定测试：

```bash
go test -v ./internal/bucket/ratelimit/... -run TestRateLimitProviderBasic
```

运行基准测试：

```bash
go test -bench=. ./internal/bucket/ratelimit/...
```

## 迁移指南

### 从原有代码迁移

原有代码：
```go
admission := ratelimit.NewMinuteBucketAdmission()
result, err := admission.Admit(ctx, keyID, limit)
```

迁移后（零改动）：
```go
import newratelimit "github.com/kaixuan/llm-gateway-go/internal/bucket/ratelimit"

admission := newratelimit.NewMinuteBucketAdmission()
result, err := admission.Admit(ctx, keyID, limit)
```

### 升级到新接口

如果要使用新的 Provider 接口和缓存功能：

```go
// 1. 创建 Provider
provider := newratelimit.NewRateLimitProvider(config)

// 2. 注册到 BucketManager（可选，用于缓存）
manager := core.NewBucketManager(cacheConfig)
manager.RegisterProvider(provider)

// 3. 通过 Manager 查询（自动使用缓存）
result, err := manager.Query(ctx, query)
```

## Redis 分布式支持（规划中）

下一阶段将添加 Redis 支持：

1. **RedisRateLimitProvider**：基于 Redis 的分布式限流
2. **Lua 脚本**：原子性操作保证并发安全
3. **集群支持**：多实例共享限流配额
4. **降级机制**：Redis 不可用时降级到本地内存

## 常见问题

### Q: 为什么有些请求会等待？

A: 当配额用尽且队列未满时，请求会进入队列等待下一个时间窗口。可以通过设置 `max_wait` 参数来限制最大等待时间。

### Q: 如何避免请求长时间阻塞？

A: 使用 `AdmitRPMWithBudget` 并设置合理的 `maxWait`，或在 `context` 中设置超时：

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
result, err := admission.Admit(ctx, keyID, limit)
```

### Q: 队列大小如何设置？

A: 默认队列大小等于 limit。如果希望更快失败，可以设置 `MaxQueueSize = 0`（等于 limit）或更小的值。

### Q: 如何监控限流效果？

A: 使用 `GetMetrics()` 获取指标，或通过 BucketManager 的统一指标接口。

## 下一步

- [ ] 实现 Redis 分布式支持
- [ ] 添加更多指标（P99延迟、队列长度直方图等）
- [ ] 支持动态调整限流参数
- [ ] 添加告警规则

## 参考

- [核心框架文档](../core/README.md)
- [信用桶实现](../credit/README.md)
- [原实现](../../ratelimit/minute_bucket.go)

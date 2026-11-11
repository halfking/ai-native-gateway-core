# Bucket Management Framework

## 概述

这是 LLM Gateway Go 的统一桶管理框架，提供了一套通用的接口和实现来管理不同类型的桶（信用桶、限流桶、优先级桶）。

## 架构设计

```
internal/bucket/
├── core/                    # 核心框架（已完成）
│   ├── types.go            # 核心类型定义
│   ├── interface.go        # 接口定义
│   ├── manager.go          # 统一管理器
│   ├── cache.go            # 内存缓存实现
│   ├── metrics.go          # 指标收集器
│   └── manager_test.go     # 单元测试
├── credit/                  # 信用桶实现（待实现）
├── ratelimit/              # 限流桶实现（待实现）
├── priority/               # 优先级桶实现（待实现）
├── storage/                # 存储层实现（待实现）
└── monitor/                # 监控层实现（待实现）
```

## 核心组件

### 1. BucketProvider 接口

所有桶类型都需要实现此接口：

```go
type BucketProvider interface {
    Name() string
    Type() BucketType
    GetState(ctx context.Context, key BucketKey) (*BucketState, error)
    UpdateState(ctx context.Context, key BucketKey, delta BucketDelta) (*BucketState, error)
    Query(ctx context.Context, query BucketQuery) (*BucketResult, error)
    Reset(ctx context.Context, key BucketKey) error
    GetMetrics(ctx context.Context) (*BucketMetrics, error)
}
```

### 2. BucketManager 管理器

统一管理所有桶提供者，提供缓存、监控等功能：

```go
manager := core.NewBucketManager(core.DefaultManagerConfig())

// 设置缓存
cache := core.NewMemoryCache(10000)
manager.SetCache(cache)

// 设置指标收集器
metrics := core.NewSimpleMetricsCollector()
manager.SetMetrics(metrics)

// 注册 provider
creditProvider := credit.NewCreditBucketProvider(pool)
manager.RegisterProvider(creditProvider)

// 使用
key := core.BucketKey{
    Type:     core.BucketTypeCredit,
    TenantID: "tenant1",
    Window:   core.CurrentHourWindow(),
}

state, err := manager.GetState(ctx, key)
```

### 3. 桶类型

支持三种桶类型：

- **BucketTypeCredit**: 信用桶，用于 MaaS 计费的时间桶统计
- **BucketTypeRateLimit**: 限流桶，用于 RPM 限流控制
- **BucketTypePriority**: 优先级桶，用于路由层的优先级分配

### 4. 缓存机制

内置 LRU 缓存实现：

```go
cache := core.NewMemoryCache(10000) // 最多 10000 个条目

// 启动后台清理
stopChan := cache.StartCleanup(5 * time.Minute)
defer func() { stopChan <- struct{}{} }()

// 获取缓存统计
stats := cache.GetStats()
fmt.Printf("Hit rate: %.2f%%\n", cache.GetHitRate() * 100)
```

### 5. 监控指标

完整的监控指标收集：

```go
collector := core.NewSimpleMetricsCollector()

// 记录请求
collector.RecordRequest(core.BucketTypeCredit, true, 100*time.Millisecond)

// 记录缓存命中
collector.RecordCacheHit(core.BucketTypeCredit, true)

// 记录配额使用
collector.RecordQuotaUsage(core.BucketTypeCredit, "tenant1", 500, 1000)

// 获取指标
metrics, err := collector.GetMetrics(core.BucketTypeCredit)
fmt.Printf("Total requests: %d\n", metrics.TotalRequests)
fmt.Printf("Cache hit rate: %.2f%%\n", 
    float64(metrics.CacheHits) / float64(metrics.CacheHits + metrics.CacheMisses) * 100)
```

## 使用示例

### 基础使用

```go
package main

import (
    "context"
    "fmt"
    "time"
    
    "github.com/kaixuan/llm-gateway-go/internal/bucket/core"
)

func main() {
    // 1. 创建管理器
    config := core.DefaultManagerConfig()
    manager := core.NewBucketManager(config)
    
    // 2. 配置缓存和监控
    cache := core.NewMemoryCache(10000)
    manager.SetCache(cache)
    
    metrics := core.NewSimpleMetricsCollector()
    manager.SetMetrics(metrics)
    
    // 3. 注册 provider（示例：需要实现具体的 provider）
    // creditProvider := credit.NewCreditBucketProvider(dbPool)
    // manager.RegisterProvider(creditProvider)
    
    // 4. 使用桶
    ctx := context.Background()
    key := core.BucketKey{
        Type:       core.BucketTypeCredit,
        TenantID:   "tenant1",
        ResourceID: "model-gpt4",
        Window:     core.CurrentHourWindow(),
    }
    
    // 获取状态
    state, err := manager.GetState(ctx, key)
    if err != nil {
        panic(err)
    }
    fmt.Printf("Available: %d / %d\n", state.Available, state.Capacity)
    
    // 更新状态
    delta := core.BucketDelta{
        Amount:    100,
        Operation: core.DeltaAdd,
    }
    newState, err := manager.UpdateState(ctx, key, delta)
    if err != nil {
        panic(err)
    }
    fmt.Printf("New available: %d / %d\n", newState.Available, newState.Capacity)
}
```

### 实现自定义 Provider

```go
package myprovider

import (
    "context"
    "github.com/kaixuan/llm-gateway-go/internal/bucket/core"
)

type MyBucketProvider struct {
    // 你的实现字段
}

func (p *MyBucketProvider) Name() string {
    return "my-bucket-provider"
}

func (p *MyBucketProvider) Type() core.BucketType {
    return core.BucketTypeCredit
}

func (p *MyBucketProvider) GetState(ctx context.Context, key core.BucketKey) (*core.BucketState, error) {
    // 实现获取桶状态的逻辑
    // 1. 从数据库/Redis查询
    // 2. 构建 BucketState
    // 3. 返回
    return &core.BucketState{
        Capacity:  1000,
        Used:      100,
        Available: 900,
        UpdatedAt: time.Now(),
    }, nil
}

func (p *MyBucketProvider) UpdateState(ctx context.Context, key core.BucketKey, delta core.BucketDelta) (*core.BucketState, error) {
    // 实现更新桶状态的逻辑
    return nil, nil
}

// 实现其他接口方法...
```

## 配置选项

### ManagerConfig

```go
type ManagerConfig struct {
    // 缓存配置
    CacheTTL     time.Duration // 缓存有效期（默认 5 分钟）
    CacheMaxSize int           // 缓存最大条目数（默认 10000）
    
    // 存储配置
    EnableStorage    bool          // 是否启用持久化存储
    StorageTimeout   time.Duration // 存储操作超时（默认 5 秒）
    StorageBatchSize int           // 批量操作大小（默认 100）
    
    // 监控配置
    EnableMetrics bool // 是否启用指标收集
    EnableAlerts  bool // 是否启用告警
    
    // 性能配置
    MaxConcurrency int // 最大并发数（默认 100）
}
```

## 测试

运行单元测试：

```bash
cd ~/workspace/ai-native-tools/llm-gateway/llm-gateway-go
go test -v ./internal/bucket/core/...
```

测试覆盖率：

```bash
go test -cover ./internal/bucket/core/...
```

所有测试（9个）均已通过 ✅

## 性能特性

- **缓存**: LRU 缓存，减少数据库查询
- **并发**: 线程安全，支持高并发访问
- **监控**: 完整的性能指标收集
- **可扩展**: 插件化架构，易于添加新的桶类型

## 下一步工作

### Day 3-4: 信用桶迁移
- [ ] 创建 `internal/bucket/credit/` 包
- [ ] 实现 CreditBucketProvider
- [ ] 集成现有的 `maas/credit_buckets.go`
- [ ] 添加缓存层
- [ ] 测试和验证

### Day 5-7: 限流桶迁移
- [ ] 创建 `internal/bucket/ratelimit/` 包
- [ ] 实现 RateLimitBucketProvider
- [ ] 添加 Redis 分布式支持
- [ ] 迁移队列机制
- [ ] 测试高并发场景

### Day 8-9: 优先级桶迁移
- [ ] 创建 `internal/bucket/priority/` 包
- [ ] 实现 PriorityBucketProvider
- [ ] 集成路由器
- [ ] 测试路由逻辑

## 常见问题

### Q: 如何选择缓存大小？

A: 默认 10000 条目适合大多数场景。根据实际情况调整：
- 小型部署（<100 租户）: 1000-5000
- 中型部署（100-1000 租户）: 10000-50000
- 大型部署（>1000 租户）: 50000+

### Q: 缓存会不会导致数据不一致？

A: 缓存有 TTL（默认 5 分钟），且在更新时会主动刷新缓存。对于强一致性要求的场景，可以：
1. 设置较短的 TTL
2. 禁用缓存
3. 使用分布式锁

### Q: 如何监控桶的使用情况？

A: 使用 MetricsCollector：
```go
metrics, _ := manager.GetMetrics(core.BucketTypeCredit)
fmt.Printf("使用率: %.2f%%\n", metrics.UsageRate * 100)
```

## 相关文档

- [设计方案](/tmp/bucket_management_optimization.md)
- [迁移计划](/tmp/llm_gateway_bucket_migration_plan.md)
- [原有实现](../maas/credit_buckets.go)

---

**版本**: v1.0  
**创建时间**: 2026-11-11  
**状态**: Phase 1 完成 ✅

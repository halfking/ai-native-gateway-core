# FreeResource Domain

免费 LLM 资源配额追踪模块。

## 概述

本模块实现了本地配额计量功能，用于追踪免费 LLM 提供商的使用情况。由于大多数免费提供商没有 usage API，需要在网关侧进行本地计量，并从 429 响应头动态校准限制。

## 核心功能

### 1. QuotaTracker - 配额追踪器

**主要方法**：

- `Record()` - 记录一次请求的配额消耗（支持多窗口并发 UPSERT）
- `Preflight()` - 配额预检，过滤即将耗尽的凭据
- `CorrectFromHeaders()` - 从 429 响应头校准配额限制

**支持的窗口类型**：

- `hour-5` - 5 小时滚动窗口（短期 burst 限制）
- `day-1` - UTC 日历日（最常见）
- `day-7` - 7 日滚动窗口
- `month-1` - UTC 日历月

### 2. 数据类型

- `WindowType` - 配额窗口类型
- `FreeType` - 免费资源类型（recurring-daily/monthly/keyless等）
- `ToSVerdict` - ToS 合规判定（ok/caution/ambiguous/avoid）
- `QuotaWindow` - 配额窗口元数据

## 使用示例

### 记录配额消耗

```go
tracker := freeresource.NewQuotaTracker(db)

err := tracker.Record(ctx, freeresource.RecordRequest{
    CredentialID: 42,
    ProviderCode: "openrouter",
    ModelID:      "openai/gpt-3.5-turbo:free",
    TokenCount:   150,
    Success:      true,
    WindowTypes:  []freeresource.WindowType{
        freeresource.WindowTypeDay1,
        freeresource.WindowTypeMonth1,
    },
    TenantID:  1,
    Timestamp: time.Now(),
})
```

### 配额预检

```go
ok, err := tracker.Preflight(ctx, freeresource.PreflightRequest{
    CredentialID:    42,
    ProviderCode:    "openrouter",
    ModelID:         "openai/gpt-3.5-turbo:free",
    WindowType:      freeresource.WindowTypeDay1,
    DefaultLimit:    1000,
    MinRemainingPct: 0.1,  // 最少剩余 10%
    TenantID:        1,
})

if !ok {
    // 配额即将耗尽，跳过此凭据
}
```

### 429 响应校准

```go
if resp.StatusCode == 429 {
    headers := make(map[string]string)
    headers["Retry-After"] = resp.Header.Get("Retry-After")
    headers["X-RateLimit-Reset"] = resp.Header.Get("X-RateLimit-Reset")
    headers["X-RateLimit-Limit"] = resp.Header.Get("X-RateLimit-Limit")

    err := tracker.CorrectFromHeaders(ctx, freeresource.CorrectionRequest{
        CredentialID: 42,
        ProviderCode: "openrouter",
        ModelID:      "openai/gpt-3.5-turbo:free",
        Headers:      headers,
        TenantID:     1,
    })
}
```

## 数据库依赖

本模块依赖以下数据库表：

- `free_quota_tracker` - 配额追踪记录（包含 UPSERT 冲突键）
- `free_resource_catalog` - 免费资源目录（元数据）

## 集成点

### Streaming Executor

在 `domains/streaming/executors/` 中集成：

```go
// 请求前：预检
ok, _ := quotaTracker.Preflight(...)
if !ok {
    return errors.New("quota exhausted")
}

// 执行请求
resp, err := doRequest(...)

// 请求后：记录
quotaTracker.Record(...)

// 处理 429
if resp.StatusCode == 429 {
    quotaTracker.CorrectFromHeaders(...)
}
```

### Credential Selector

在凭据选择器中过滤配额耗尽的凭据：

```go
for _, cred := range candidates {
    if cred.IsFree {
        ok, _ := quotaTracker.Preflight(...)
        if !ok {
            continue  // 跳过耗尽的凭据
        }
    }
    filtered = append(filtered, cred)
}
```

## 测试

```bash
# 运行单元测试（快速）
go test ./domains/freeresource -v -short

# 运行集成测试（需要数据库）
go test ./domains/freeresource -v
```

## 设计文档

详细设计请参考：
- `docs/omnifree/02-QUOTA-TRACKING.md` - 配额追踪设计
- `docs/omnifree/01-DATA-MODEL.md` - 数据模型设计

## 后续扩展

- [ ] 后台 Worker（重置过期窗口、清理历史数据）
- [ ] Redis 缓存（减少数据库热点）
- [ ] 监控指标（Prometheus）
- [ ] 配额预测算法

## 维护

- 维护者：SI-LLM-Gateway 团队
- 最后更新：2026-08-07

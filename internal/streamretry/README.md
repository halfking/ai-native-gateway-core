# Stream Retry & Keepalive Module

## 概述

`internal/streamretry` 模块为流式请求提供智能重试和客户端保活机制，解决供应商端偶发故障导致的连接中断问题。

## 问题场景

**现象**：
- 供应商端偶发闪断/过载导致流式请求直接关闭
- 客户端感知到中断，需要人工重新发起
- 无重试机制，影响用户体验

**影响**：
- 用户会话频繁中断
- 需要人为介入恢复
- 降低系统可靠性

## 解决方案

### 1. 智能重试 (Smart Retry)

**特性**：
- 自动识别可重试错误（5xx、429、网络超时、连接中断）
- 指数退避 + 随机抖动（防止雷鸣效应）
- 最多重试 3 次（可配置）
- 与现有 rate limit 重试策略保持一致

**重试判定**：

| 错误类型 | 是否重试 | 原因 |
|---------|---------|------|
| HTTP 5xx | ✅ 重试 | 服务端临时故障 |
| HTTP 429 | ✅ 重试 | 速率限制，需退避 |
| HTTP 408/425 | ✅ 重试 | 超时/太早 |
| 网络超时 | ✅ 重试 | 网络闪断 |
| 连接拒绝/重置 | ✅ 重试 | TCP 连接问题 |
| 过早 EOF | ✅ 重试 | 连接意外关闭 |
| HTTP 4xx (非上述) | ❌ 不重试 | 客户端错误 |
| 认证失败 | ❌ 不重试 | 凭据问题 |
| Context Canceled | ❌ 不重试 | 用户/系统取消 |

**退避策略**：

```
attempt=0: 200ms ± 20% = 160-240ms
attempt=1: 400ms ± 20% = 320-480ms
attempt=2: 800ms ± 20% = 640-960ms
attempt=3: 1600ms ± 20% = 1280-1920ms (如果配置 MaxRetries=4)
```

### 2. 客户端保活 (Client Keepalive)

**特性**：
- 重试期间发送 SSE 注释事件保持连接
- 客户端不会因超时断开连接
- 透明：不触发客户端解析错误

**实现**：
```
SSE 注释格式: ": thinking: 正在重试连接上游服务...\n\n"
```

### 3. 透明重连 (Transparent Reconnection)

**特性**：
- 对客户端无感知
- SSE 连接保持
- 自动恢复后继续流式输出

## 使用方式

### 基础用法

```go
import (
	"context"
	"net/http"
	"github.com/kaixuan/llm-gateway-go/internal/streamretry"
)

func handleStream(w http.ResponseWriter, r *http.Request) {
	// 1. 创建配置
	config := streamretry.DefaultConfig()
	// 可选：自定义配置
	config.MaxRetries = 3
	config.BaseDelayMs = 200
	config.KeepaliveInterval = 10 * time.Second

	// 2. 创建包装器
	wrapper := streamretry.NewWrapper(config, slog.Default())

	// 3. 定义流处理函数
	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		// 你的流处理逻辑
		return executeUpstreamRequest(ctx, w)
	}

	// 4. 执行（自动重试 + 保活）
	if err := wrapper.Execute(r.Context(), w, streamFunc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 5. 可选：获取重试指标
	metrics := wrapper.Metrics()
	slog.Info("stream completed",
		"attempts", metrics.TotalAttempts,
		"retries", metrics.TotalRetries,
		"success_attempt", metrics.SuccessAttempt+1)
}
```

### 环境变量配置

可通过环境变量全局控制重试行为：

```bash
# 启用/禁用重试（默认：true）
export LLM_GATEWAY_STREAM_RETRY_ENABLED=true

# 最大重试次数（默认：3）
export LLM_GATEWAY_STREAM_RETRY_MAX_ATTEMPTS=3

# 基础退避延迟（毫秒，默认：200）
export LLM_GATEWAY_STREAM_RETRY_BASE_DELAY_MS=200

# 最大退避延迟（毫秒，默认：5000）
export LLM_GATEWAY_STREAM_RETRY_MAX_DELAY_MS=5000

# 保活间隔（秒，默认：10）
export LLM_GATEWAY_STREAM_RETRY_KEEPALIVE_INTERVAL=10
```

## API 文档

### Config

```go
type Config struct {
	MaxRetries        int           // 最大重试次数（默认：3）
	BaseDelayMs       int           // 基础退避延迟毫秒数（默认：200）
	MaxDelayMs        int           // 最大退避延迟毫秒数（默认：5000）
	KeepaliveInterval time.Duration // 保活间隔（默认：10s）
	Enabled           bool          // 全局启用/禁用（默认：true）
}
```

### Wrapper

```go
type Wrapper struct {
	// ...
}

func NewWrapper(config Config, logger *slog.Logger) *Wrapper
func (w *Wrapper) Execute(ctx context.Context, httpW http.ResponseWriter, streamFunc StreamFunc) error
func (w *Wrapper) Metrics() WrapperMetrics
```

### 错误分类

```go
func ClassifyError(err error) *RetryableError
func ClassifyHTTPError(statusCode int, err error) *RetryableError
func IsRetriable(err error) bool
```

### 保活

```go
type KeepaliveWriter struct {
	// ...
}

func NewKeepaliveWriter(w http.ResponseWriter, interval time.Duration) *KeepaliveWriter
func (kw *KeepaliveWriter) Start(ctx context.Context)
func (kw *KeepaliveWriter) Stop()
func (kw *KeepaliveWriter) SendRetryMessage(attempt int, delay time.Duration)
```

## 测试

运行测试：

```bash
go test -v ./internal/streamretry/...
```

测试覆盖：
- ✅ 错误分类（网络、HTTP、系统）
- ✅ 指数退避计算
- ✅ 重试判定逻辑
- ✅ Context 取消处理
- ✅ 保活机制
- ✅ 集成场景

## 性能影响

- **无重试场景**：零性能损耗（快速路径）
- **重试场景**：增加延迟 = 退避时间总和（200ms-5s）
- **内存开销**：每请求 ~1KB（重试上下文）

## 限制

1. **不适用于非幂等操作**：如果流处理有副作用（如写数据库），需要在业务层保证幂等性
2. **Header 已发送后无法重试**：如果已经写入响应 header，则无法透明重试
3. **最大重试次数固定**：避免无限重试消耗资源

## 监控指标

建议监控以下指标：

1. **重试率**：`stream_retry_total / stream_requests_total`
2. **重试成功率**：`stream_retry_success / stream_retry_total`
3. **平均重试次数**：`avg(retry_attempts_per_request)`
4. **客户端保活事件数**：`keepalive_events_sent_total`

## 兼容性

- ✅ 与现有 `domains/streaming/executors` 兼容
- ✅ 与 `internal/probeutil/retry.go` 策略对齐
- ✅ 不影响非流式请求
- ✅ 可通过配置完全禁用

## 后续优化

- [ ] 支持自适应重试（根据历史成功率动态调整）
- [ ] 集成分布式追踪（OpenTelemetry）
- [ ] 支持断点续传（部分流已发送时）
- [ ] 支持重试预算（Circuit Breaker 集成）

## 参考

- Rule 11: 执行协议
- Rule 03: 部署安全
- Rule 37: LLM 编码四原则
- `internal/probeutil/retry.go`: 探测重试策略
- `domains/streaming/handler.go`: 流处理主逻辑

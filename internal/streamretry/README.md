# Stream Retry & Keepalive Module

> **v1.1 · 2026-08-12**: 增加 `DefaultStreamExecutor` 入口（http.Handler 包装），在 `cmd/gateway/main.go` 通过 `streamretry.NewDefaultStreamExecutor(chatHandler, cfg)` 接入。完整 API 与集成示例见下文。

## 概述

`internal/streamretry` 模块为流式请求提供**服务端透明重试 + 客户端保活**机制，
解决供应商端偶发故障（pre-stream 5xx、429、TCP RST、连接重置）导致的请求级中断问题。
对客户端而言，重试期间通过 SSE 注释帧保活，恢复后继续接收响应，业务无感。

## 问题场景

**现象**：

- 供应商端偶发闪断 / 过载 / 限流，导致流式请求**首字节前**返回 5xx / 429
- 客户端感知到 503 / 502 / 504，必须整轮对话重发
- LLM 网关层没有重试，影响用户体验 + 浪费已完成的部分工作（system prompt / tool 解析）

**影响**：

- 用户会话频繁中断
- 需要人为介入恢复
- 降低系统可靠性 + 增加单位请求成本

## 解决方案

### 1. 智能重试 (Smart Retry)

**特性**：

- 自动识别可重试错误（5xx、429、网络超时、连接中断）
- 指数退避 + ±20% 随机抖动（防止雷鸣效应）
- 默认最多 3 次尝试（可配）
- 4xx（非 408/425/429）、Context Canceled、Auth Failure 不重试

**重试判定**：

| 错误类型                     | 是否重试 | 原因                  |
|----------------------------|--------|---------------------|
| HTTP 5xx（500/502/503/504） | ✅ 重试  | 服务端临时故障            |
| HTTP 429                   | ✅ 重试  | 速率限制，需退避            |
| HTTP 408 / 425             | ✅ 重试  | 超时 / 太早              |
| 网络超时（`net.Error.Timeout()`） | ✅ 重试  | 网络闪断                |
| TCP RST / 连接拒绝            | ✅ 重试  | 瞬时网络问题              |
| Premature EOF              | ✅ 重试  | 连接意外关闭              |
| HTTP 4xx（除 408/425/429）    | ❌ 不重试 | 客户端错误               |
| 认证失败（401/403）             | ❌ 不重试 | 凭据问题                |
| Context Canceled           | ❌ 不重试 | 用户 / 系统主动取消         |

**退避策略**（`MaxRetries=3, BaseDelayMs=200, MaxDelayMs=5000`）：

```
attempt=0: 200ms ± 20% = 160-240ms
attempt=1: 400ms ± 20% = 320-480ms
attempt=2: 800ms ± 20% = 640-960ms
```

### 2. 客户端保活 (Client Keepalive)

**特性**：

- 重试期间发送 SSE 注释帧保持连接
- 客户端不会因 idle timeout 断开
- 透明：不触发客户端解析错误（注释帧以 `:` 开头，OpenAI / Anthropic SDK 都会忽略）

**SSE 注释格式**：

```
: thinking: 正在重试连接上游服务...
: thinking: Upstream service temporarily unavailable, retrying (attempt 1, waiting 200ms)...
```

### 3. 透明重连 (Transparent Reconnection)

- 对客户端无感知：SSE 注释帧不参与内容解析
- 服务端重试成功后，正常数据流接续发送
- 客户端 connection: keep-alive 持续，无需重新建链

## API 文档

### 主入口：`DefaultStreamExecutor`（推荐）

`DefaultStreamExecutor` 把任意 `http.Handler` 包成"带重试 + 保活"的流式处理器，
**直接实现 `http.Handler`**，可挂到 `http.ServeMux.Handle(...)` 替代原 handler。
包装器会在首个 attempt 前快照 JSON body，并在每次 retry 前恢复 `Request.Body`，
避免第二次 attempt 读到 EOF。只有 body 中 `stream: true` 的请求重试；非流式请求保持单次执行，
避免放大非幂等副作用。

```go
type DefaultStreamExecutor struct {
    // unexported
}

func NewDefaultStreamExecutor(handler http.Handler, config Config) *DefaultStreamExecutor
func (e *DefaultStreamExecutor) ServeHTTP(w http.ResponseWriter, req *http.Request)
func (e *DefaultStreamExecutor) ExecuteStream(ctx context.Context, w http.ResponseWriter, req *http.Request) error
```

| 方法                | 用途                                                                              |
|-------------------|---------------------------------------------------------------------------------|
| `NewDefaultStreamExecutor` | 构造器；`handler` 是被包装的 `http.Handler`（如 `*streaming.ChatHandler`），`config` 注入重试/退避/保活参数 |
| `ServeHTTP`       | 实现 `http.Handler`；从 `req.Context()` 拿到 context，转发给 `ExecuteStream`                              |
| `ExecuteStream`   | 暴露的入口；`ctx` 由调用方控制（caller 负责生命周期管理）                                                  |
| `ExecuteStreamWithMetrics` | 返回当前请求的独立指标快照，适合并发请求的请求级观测 |
| `Metrics()`       | 线程安全地读取最近一次已完成执行的指标；并发场景下不要将其当作当前请求指标 |

### 底层 API：`Wrapper`（直接写 `StreamFunc` 时使用）

`Wrapper` 是 `DefaultStreamExecutor` 内部使用的重试循环。当业务方没有 `http.Handler`
抽象、而是要把一段"写流 + 出错"的代码包起来时，直接用 `Wrapper`：

```go
type StreamFunc func(ctx context.Context, w http.ResponseWriter) error

func NewWrapper(config Config, logger *slog.Logger) *Wrapper
func (w *Wrapper) Execute(ctx context.Context, httpW http.ResponseWriter, streamFunc StreamFunc) error
func (w *Wrapper) ExecuteWithMetrics(ctx context.Context, httpW http.ResponseWriter, streamFunc StreamFunc) (WrapperMetrics, error)
func (w *Wrapper) Metrics() WrapperMetrics
```

### `Config`

```go
type Config struct {
    MaxRetries        int           // 最大重试次数（默认 3）
    BaseDelayMs       int           // 基础退避延迟毫秒数（默认 200）
    MaxDelayMs        int           // 最大退避延迟毫秒数（默认 5000）
    KeepaliveInterval time.Duration // 保活间隔（默认 10s）
    Enabled           bool          // 全局开关（默认 true）
}

func DefaultConfig() Config
```

### 错误分类工具

```go
func ClassifyError(err error) *RetryableError
func ClassifyHTTPError(statusCode int, err error) *RetryableError
func IsRetriable(err error) bool

// 用于把 *http.Response 错误包成 streamretry 认识的 HTTPError
func WrapHTTPError(resp *http.Response, baseErr error) error

// 从错误链里反查 HTTP 状态码（0 = 无）
func ExtractHTTPStatus(err error) int
```

### 保活写入器（内部用）

```go
type KeepaliveWriter struct{ /* ... */ }

func NewKeepaliveWriter(w http.ResponseWriter, interval time.Duration) *KeepaliveWriter
func (kw *KeepaliveWriter) Start(ctx context.Context)        // 启 goroutine
func (kw *KeepaliveWriter) Stop()                            // 同步停
func (kw *KeepaliveWriter) SendRetryMessage(attempt int, delay time.Duration) // 一次性 retry 提示
```

## 在 ChatHandler 中接入（v1 chat 入口）

> **2026-08-12 起**：v1 `/v1/chat/completions` 入口默认**不**开启 pre-stream 重试；
> 通过 `LLM_GATEWAY_STREAM_RETRY_ENABLED=true` 启用。建议先在 staging / 245 跑一周观察指标，再上 154 生产。

### Step 1：在 `config.Config` 增加配置字段

`config/config.go`：

```go
// StreamRetryEnabled (2026-08-12): when true, internal/streamretry wraps
// the v1 chat handler at the mux layer so upstream 5xx / 429 / connection
// drops BEFORE the first byte triggers exponential-backoff retry on the
// server side. ... Disabled by default.
StreamRetryEnabled          bool          `yaml:"stream_retry_enabled"          env:"LLM_GATEWAY_STREAM_RETRY_ENABLED"`
StreamRetryMaxRetries       int           `yaml:"stream_retry_max_retries"      env:"LLM_GATEWAY_STREAM_RETRY_MAX_RETRIES"`
StreamRetryBaseDelayMs      int           `yaml:"stream_retry_base_delay_ms"    env:"LLM_GATEWAY_STREAM_RETRY_BASE_DELAY_MS"`
StreamRetryMaxDelayMs       int           `yaml:"stream_retry_max_delay_ms"     env:"LLM_GATEWAY_STREAM_RETRY_MAX_DELAY_MS"`
StreamRetryKeepaliveSecs    int           `yaml:"stream_retry_keepalive_secs"   env:"LLM_GATEWAY_STREAM_RETRY_KEEPALIVE_SECS"`
```

### Step 2：在 mux 入口包装 `chatHandler`

`cmd/gateway/main.go`：

```go
import (
    "github.com/kaixuan/llm-gateway-go/internal/streamretry"
)

// ... 在 chatHandler 已构造之后、mux.Handle(...) 之前 ...

// 2026-08-12: 把流式 handler 包到 streamretry.NewDefaultStreamExecutor(handler, cfg) 即可接入。
// 配置（重试上限、退避、保活间隔）通过 Config 注入。
chatRouteHandler := http.Handler(chatHandler)
if cfg.StreamRetryEnabled {
    srCfg := streamretry.Config{
        MaxRetries:        cfg.StreamRetryMaxRetries,      // 默认 3 次重试
        BaseDelayMs:       cfg.StreamRetryBaseDelayMs,     // 默认 200
        MaxDelayMs:        cfg.StreamRetryMaxDelayMs,      // 默认 5000
        KeepaliveInterval: time.Duration(cfg.StreamRetryKeepaliveSecs) * time.Second, // 默认 10s
        Enabled:           true,
    }
    chatRouteHandler = streamretry.NewDefaultStreamExecutor(chatHandler, srCfg)
    slog.Info("stream_retry_enabled: chat handler wrapped with NewDefaultStreamExecutor",
        "max_attempts", srCfg.MaxRetries,
        "base_delay_ms", srCfg.BaseDelayMs,
        "max_delay_ms", srCfg.MaxDelayMs,
        "keepalive_interval", srCfg.KeepaliveInterval)
}

mux.Handle("/v1/chat/completions", chatRouteHandler)
mux.Handle("/v1/completions", chatRouteHandler)   // legacy 文本补全也走同一份

// Anthropic Messages / OpenAI Responses 也可以按同一策略接入；
// wrapper 会根据各自 JSON body 的 stream 字段决定是否 retry。
mux.Handle("/v1/messages", streamretry.NewDefaultStreamExecutor(messagesHandler, srCfg))
mux.Handle("/v1/responses", streamretry.NewDefaultStreamExecutor(responsesHandler, srCfg))
```

### Step 3：通过环境变量 / YAML 调参

```bash
# .env.dev
LLM_GATEWAY_STREAM_RETRY_ENABLED=true
LLM_GATEWAY_STREAM_RETRY_MAX_RETRIES=3
LLM_GATEWAY_STREAM_RETRY_BASE_DELAY_MS=200
LLM_GATEWAY_STREAM_RETRY_MAX_DELAY_MS=5000
LLM_GATEWAY_STREAM_RETRY_KEEPALIVE_SECS=10
```

或 `config.yaml`：

```yaml
stream_retry_enabled: true
stream_retry_max_attempts: 3
stream_retry_base_delay_ms: 200
stream_retry_max_delay_ms: 5000
stream_retry_keepalive_secs: 10
```

### Step 4：观察指标

`streamretry.DefaultStreamExecutor` 通过 per-call API 返回当前请求指标；`Metrics()` 只表示最近一次已完成执行的快照。Prometheus counters 由 `metrics.go` 统一记录。

| 字段                | 含义                                |
|-------------------|-----------------------------------|
| `TotalAttempts`   | 实际执行的尝试总数（含首次）                |
| `TotalRetries`    | 触发重试的次数                           |
| `SuccessAttempt`  | 首次成功的尝试序号（-1 = 全部失败）            |
| `LastError`       | 最近一次失败的底层错误（按 `errors.Is/As` 解包） |

请求级日志可直接使用返回值：

```go
metrics, err := wrapped.ExecuteStreamWithMetrics(ctx, w, req)
slog.Info("stream completed",
    "attempts", metrics.TotalAttempts,
    "retries", metrics.TotalRetries,
    "success_attempt", metrics.SuccessAttempt,
    "error", err)
```

## 基础用法（直接使用 Wrapper）

不通过 `http.Handler` 抽象、而是写一段"写流 + 出错"的函数时，用 `Wrapper`：

```go
import (
    "context"
    "net/http"
    "github.com/kaixuan/llm-gateway-go/internal/streamretry"
)

func handleStream(w http.ResponseWriter, r *http.Request) {
    cfg := streamretry.DefaultConfig()
    cfg.MaxRetries = 3
    cfg.BaseDelayMs = 200
    cfg.KeepaliveInterval = 10 * time.Second

    wrapper := streamretry.NewWrapper(cfg, slog.Default())

    streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
        return executeUpstreamRequest(ctx, w)
    }

    if err := wrapper.Execute(r.Context(), w, streamFunc); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    m := wrapper.Metrics()
    slog.Info("stream completed",
        "attempts", m.TotalAttempts,
        "retries", m.TotalRetries,
        "success_attempt", m.SuccessAttempt+1)
}
```

## 测试

```bash
go test -v ./internal/streamretry/...
```

覆盖：

- ✅ 错误分类（网络、HTTP、系统）
- ✅ 指数退避计算
- ✅ 重试判定逻辑
- ✅ Context 取消处理
- ✅ 保活机制
- ✅ `DefaultStreamExecutor` 集成（成功 / 5xx 重试 / 4xx 不重试 / 关闭 fast-path / 取消）

## 性能影响

- **无重试场景**：零额外开销（单次执行，wrap 即前/后置零成本）
- **重试场景**：增加延迟 = 退避时间总和（200ms-5s）+ 重复执行上游调用
- **内存开销**：每请求 ~1KB（retry context + keepalive 句柄）

## 限制

1. **不适用于非幂等操作**：如果流处理有副作用（如写数据库），需要在业务层保证幂等性
2. **Header 已发送后无法重试**：如果内层 handler 已经写入 4xx/5xx 响应头，executor 仅更新内部指标 + 错误日志，**不会**重发到客户端（首次 WriteHeader 已 commit，response writer 不支持覆写）
3. **最大重试次数固定**：避免无限重试消耗资源
4. **pre-stream 友好**：当前实现**重点解决 pre-stream 失败**（首字节前的 5xx / 429 / 网络中断）。mid-stream 失败由 `domains/streaming/executors` 的 `StreamRetryThreshold` + candidate failover 处理
5. **只重试 `stream: true`**：非流式请求即使被包装也只执行一次；请求 body 无法解析为 JSON 时同样不进入 retry loop

## 监控指标建议

1. **重试率**：`stream_retry_total / stream_requests_total`
2. **重试成功率**：`stream_retry_success / stream_retry_total`
3. **平均重试次数**：`avg(retry_attempts_per_request)`
4. **客户端保活事件数**：`keepalive_events_sent_total`
5. **重试预算耗尽率**：`stream_retry_exhausted_total / stream_retry_total`

## 兼容性

- ✅ 与现有 `domains/streaming/executors` 兼容（独立的两层重试 — 内部 credential failover 由 executor 负责，pre-stream 整体重试由 streamretry 负责）
- ✅ 与 `internal/probeutil/retry.go` 策略对齐（同样的指数退避 + ±20% 抖动）
- ✅ 不影响非流式请求（`/v1/completions` `/v1/embeddings` 走 `chatHandler` 内的非流路径，不触发重试）
- ✅ 可通过 `Enabled=false` 完全禁用

## 后续优化

- [ ] 内置 Prometheus 指标导出（替代手写 `Metrics()` 采样 goroutine）
- [ ] 支持自适应重试（根据历史成功率动态调整）
- [ ] 集成分布式追踪（OpenTelemetry）
- [ ] 支持断点续传（mid-stream 失败也能透明重连）
- [ ] 支持重试预算（Circuit Breaker 集成）

## 参考

- Rule 11: 执行协议
- Rule 03: 部署安全
- Rule 37: LLM 编码四原则
- `internal/probeutil/retry.go`: 探测重试策略
- `domains/streaming/handler.go`: 流处理主逻辑
- `cmd/gateway/main.go` `mux.Handle("/v1/chat/completions", ...)` 区域：当前 v1 接入点

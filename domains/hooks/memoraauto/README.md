# Memora 自动沉淀 Hook

## 概述

Memora 自动沉淀 Hook 是一个用于自动检测空闲会话并将其数据沉淀到 kxmemory 服务的 Pipeline Hook。

## 功能特性

- ✅ **空闲检测**: 自动检测满足条件的空闲会话（最后活动 > 1小时 且 请求数 ≥ 3）
- ✅ **异步处理**: 在 PhasePostResponse 阶段异步执行，不阻塞主请求流程
- ✅ **HTTP 调用**: 通过 HTTP POST 调用 kxmemory 的会话接收 API
- ✅ **重试机制**: 支持指数退避重试策略，最多重试 3 次
- ✅ **线程安全**: 所有组件都是并发安全的
- ✅ **可配置**: 支持通过配置文件或代码配置各项参数

## 架构设计

### 核心组件

```
MemoraAutoHook
├── IdleDetector        # 会话空闲检测器
├── KxmemoryClient      # HTTP 客户端
└── RetryManager        # 重试管理器
```

### 工作流程

```
1. 请求到达 → Execute()
2. 检查会话是否存在
   - 不存在 → Track() → 结束
   - 存在 → 继续
3. 检查是否空闲
   - 未空闲 → Track() → 结束
   - 空闲 → 异步沉淀
4. 异步沉淀流程:
   - 构造请求
   - 带重试的 HTTP 调用
   - 成功后标记为已处理
```

## 使用方法

### 基本用法

```go
import (
    "log/slog"
    "github.com/kaixuan/llm-gateway-go/domains/hooks/memoraauto"
)

// 使用默认配置创建 Hook
hook := memoraauto.NewMemoraAutoHook(nil, slog.Default())

// 或使用自定义配置
config := &memoraauto.Config{
    Enabled:         true,
    KxmemoryURL:     "http://kxmemory:8000/api/sessions/ingest",
    Timeout:         10 * time.Second,
    IdleThreshold:   1 * time.Hour,
    MinRequestCount: 3,
    MaxRetries:      3,
    RetryBackoff:    1 * time.Second,
}
hook := memoraauto.NewMemoraAutoHook(config, slog.Default())
```

### 集成到 Pipeline

```go
import (
    "github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// 创建 Pipeline
p := pipeline.NewRequestPipeline()

// 添加 PostResponse 阶段
postResponseStage := &pipeline.PipelineStage{
    Name:  "PostResponse",
    Phase: pipeline.PhasePostResponse,
    Hooks: []pipeline.Hook{
        hook, // 添加 Memora 自动沉淀 Hook
    },
    Mode: pipeline.ModeSequential,
}
p.AddStage(postResponseStage)
```

## 配置说明

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `Enabled` | bool | `true` | 是否启用 Hook |
| `KxmemoryURL` | string | `http://localhost:8000/api/sessions/ingest` | kxmemory API 地址 |
| `Timeout` | time.Duration | `10s` | HTTP 请求超时时间 |
| `IdleThreshold` | time.Duration | `1h` | 空闲时间阈值 |
| `MinRequestCount` | int | `3` | 最小请求数阈值 |
| `MaxRetries` | int | `3` | 最大重试次数 |
| `RetryBackoff` | time.Duration | `1s` | 重试退避基础时间 |

## API 接口

### Hook 接口

```go
type Hook interface {
    Name() string                                         // 返回 "memora.auto"
    Priority() int                                        // 返回 200
    Enabled(ctx context.Context, env *domain.PipelineRequest) bool
    Execute(ctx context.Context, env *domain.PipelineRequest) error
    OnError(ctx context.Context, env *domain.PipelineRequest, err error) error
}
```

### 空闲检测条件

会话被认为空闲需要同时满足：
1. 请求数 ≥ `MinRequestCount`（默认 3）
2. 距离最后活动时间 > `IdleThreshold`（默认 1 小时）

### kxmemory API 请求格式

```json
POST /api/sessions/ingest
Content-Type: application/json

{
  "session_key": "session-123",
  "task_id": "task-456",
  "tenant_id": "tenant-789",
  "metadata": {
    "request_count": 5,
    "last_active": "2026-07-03T12:00:00Z",
    "created_at": "2026-07-03T10:00:00Z"
  }
}
```

### kxmemory API 响应格式

```json
{
  "success": true,
  "message": "Session ingested successfully",
  "job_id": "job-abc123"
}
```

## 测试

### 运行单元测试

```bash
go test ./domains/hooks/memoraauto/... -v
```

### 运行测试并查看覆盖率

```bash
go test ./domains/hooks/memoraauto/... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

当前测试覆盖率：**88.2%**

## 性能考虑

- **异步处理**: 沉淀操作在 goroutine 中异步执行，不阻塞主请求
- **内存占用**: 每个会话在内存中维护约 100 字节的统计信息
- **并发安全**: 使用 `sync.RWMutex` 保护共享状态，读写分离提高并发性能
- **重试策略**: 指数退避避免对下游服务造成过大压力

## 错误处理

- Hook 执行错误不会影响主请求流程（通过 `OnError` 吞掉错误）
- 错误信息会记录到日志和 `PipelineRequest.Metadata`
- HTTP 调用失败会自动重试，最多重试 3 次

## 监控建议

建议监控以下指标：
- 会话跟踪数量（`IdleDetector.Size()`）
- 空闲检测触发次数
- kxmemory 调用成功/失败次数
- 重试次数分布
- 平均沉淀耗时

## 依赖

- **Task A1**: Hook 框架（`domains/pipeline`）
- **Task A3**: kxmemory 会话接收 API

## 维护者

- 架构组
- 创建时间: 2026-07-04
- Go 版本: 1.25+

## License

Internal use only.

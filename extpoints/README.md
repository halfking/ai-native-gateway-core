# ExtensionPoint Plugin Architecture

## 概述

ExtensionPoint 架构为 llm-gateway 提供了 16-Phase 插件系统，允许开发者在请求处理的各个阶段注入自定义逻辑。

## 架构集成 (2026-11-19)

### 桥接设计

ExtensionPoint 架构通过 `ExtensionPointHook` 桥接到现有的 `domains/pipeline` 架构：

```
┌─────────────────────────────────────────┐
│   domains/pipeline (生产 Pipeline)      │
│   ├─ PhaseAuthentication               │
│   ├─ PhasePreRouting                   │
│   ├─ PhaseRouting                      │
│   └─ PhasePostRouting                  │
└──────────────┬──────────────────────────┘
               │
        ExtensionPointHook (桥接适配器)
               │
┌──────────────▼──────────────────────────┐
│   extpoints (插件架构)                  │
│   ├─ PhaseAuth                         │
│   ├─ PhasePreRoute                     │
│   ├─ PhaseRoute                        │
│   └─ PhasePostRoute                    │
└─────────────────────────────────────────┘
```

### Phase 映射关系

| domains/pipeline Phase | extpoints Phase | 说明 |
|------------------------|-----------------|------|
| `pre_authentication` | `PhasePreAuth` | 认证前处理 |
| `authentication` | `PhaseAuth` | 认证执行 |
| `post_authentication` | `PhasePostAuth` | 认证后处理 |
| `pre_routing` | `PhasePreRoute` | 路由前处理 |
| `routing` | `PhaseRoute` | 路由决策 |
| `post_routing` | `PhasePostRoute` | 路由后处理 |
| `pre_upstream` | `PhasePreUpstream` | 上游调用前 |
| `upstream` | `PhaseUpstream` | 上游调用 |
| `post_upstream` | `PhasePostUpstream` | 上游调用后 |
| `pre_response` | `PhasePreResponse` | 响应前处理 |
| `response` | `PhaseResponse` | 响应处理 |
| `post_response` | `PhasePostResponse` | 响应后处理 |

**注意**：`governance` 和 `transform` 系列 Phase 在 extpoints 中无对应映射。

## 快速开始

### 1. 创建插件

```go
package myplugin

import (
    "context"
    "github.com/kaixuan/llm-gateway-go/extpoints"
)

// MyAuthPlugin 是一个简单的认证插件示例
type MyAuthPlugin struct {
    *extpoints.BaseExtensionPoint
}

func NewMyAuthPlugin() *MyAuthPlugin {
    metadata := &extpoints.PluginMetadata{
        PluginID:   "my-auth-plugin",
        Version:    "1.0.0",
        Phase:      extpoints.PhaseAuth,
        Priority:   extpoints.PriorityNormal,
    }
    
    return &MyAuthPlugin{
        BaseExtensionPoint: extpoints.NewBaseExtensionPoint(metadata),
    }
}

// Execute 实现插件逻辑
func (p *MyAuthPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
    // 从 State 中读取请求信息
    // ctx.State 在同一请求的所有插件间共享
    
    // 执行认证逻辑
    // ...
    
    return &extpoints.ExecutionResult{
        Success: true,
        Metadata: map[string]interface{}{
            "authenticated": true,
            "user_id": "user-123",
        },
    }, nil
}
```

### 2. 注册插件

在 `cmd/gateway/main.go` 或相应的初始化代码中：

```go
// 获取 ExtensionPointRegistry（从 v2DispatchDeps）
registry := deps.ExtensionPointRegistry

// 注册插件
plugin := myplugin.NewMyAuthPlugin()
err := registry.Register(extpoints.PhaseAuth, plugin)
if err != nil {
    log.Fatalf("Failed to register plugin: %v", err)
}
```

### 3. 插件自动执行

插件注册后，会在对应的 Phase 自动执行：

1. 请求到达 Gateway
2. Pipeline 执行到 `PhaseAuthentication`
3. `ExtensionPointHook` 被调用
4. 获取 `PhaseAuth` 的所有插件（按优先级排序）
5. 依次执行每个插件
6. 将结果同步到 `PipelineRequest.Metadata`

## 插件开发指南

### 插件接口

所有插件必须实现 `PhaseExecutor` 接口：

```go
type PhaseExecutor interface {
    ExtensionPoint
    Execute(ctx *ExecutionContext) (*ExecutionResult, error)
}
```

其中 `ExtensionPoint` 包含生命周期方法：

```go
type ExtensionPoint interface {
    Metadata() *PluginMetadata
    Initialize(ctx context.Context) error
    Shutdown(ctx context.Context) error
    HealthCheck(ctx context.Context) error
}
```

### 使用 BaseExtensionPoint

推荐继承 `BaseExtensionPoint` 以获得默认实现：

```go
type MyPlugin struct {
    *extpoints.BaseExtensionPoint
    // 自定义字段
}
```

这样只需实现 `Execute` 方法。

### ExecutionContext

`ExecutionContext` 提供请求上下文：

```go
type ExecutionContext struct {
    Context     context.Context      // Go context
    Phase       Phase                // 当前 Phase
    PluginID    string               // 插件 ID
    State       map[string]interface{} // 跨插件共享状态
    RequestID   string               // 请求 ID
    RequestTime time.Time            // 请求时间
    Timeout     time.Duration        // 超时限制
}
```

**重要**：`State` 在同一请求的所有插件间共享，可用于插件间通信。

### ExecutionResult

返回执行结果：

```go
type ExecutionResult struct {
    Success  bool                   // 执行是否成功
    Error    error                  // 错误信息（如果有）
    Modified bool                   // 是否修改了请求/响应
    Latency  time.Duration          // 执行耗时
    Metadata map[string]interface{} // 自定义元数据
}
```

**元数据同步**：`Metadata` 中的数据会同步到 `PipelineRequest.Metadata`，前缀为 `plugin_{pluginID}_`。

## 执行特性

### 优先级

插件按 `Priority` 值升序执行（小值先执行）：

```go
const (
    PriorityHighest Priority = 0
    PriorityHigh    Priority = 100
    PriorityNormal  Priority = 500
    PriorityLow     Priority = 900
    PriorityLowest  Priority = 1000
)
```

### Fail-Open 策略

插件失败**不会阻塞请求**：

- 单个插件失败 → 记录错误 → 继续执行后续插件
- 所有插件失败 → 请求仍然通过 Pipeline

这确保了插件的可靠性不影响核心网关功能。

### 性能

- ExtensionPointHook 的 Priority = 500，在标准 Hook 之后执行
- `Enabled()` 检查：没有插件注册时跳过执行
- 每个插件调用开销 < 1µs（基于单元测试）

## 测试

### 单元测试

测试插件逻辑：

```go
func TestMyPlugin_Execute(t *testing.T) {
    plugin := NewMyAuthPlugin()
    
    ctx := &extpoints.ExecutionContext{
        Context:   context.Background(),
        Phase:     extpoints.PhaseAuth,
        PluginID:  "my-auth-plugin",
        State:     make(map[string]interface{}),
        RequestID: "test-123",
    }
    
    result, err := plugin.Execute(ctx)
    if err != nil {
        t.Fatalf("Execute failed: %v", err)
    }
    
    if !result.Success {
        t.Error("Expected success")
    }
}
```

### 集成测试

参考 `extpoints/integration_test.go`：

```go
func TestMyPlugin_Integration(t *testing.T) {
    // 1. 创建注册表
    registry := extpoints.NewExtensionPointRegistry()
    
    // 2. 注册插件
    plugin := NewMyAuthPlugin()
    registry.Register(extpoints.PhaseAuth, plugin)
    
    // 3. 创建 Pipeline
    p := pipeline.NewRequestPipeline()
    p.AddStage(&pipeline.PipelineStage{
        Name:  "authentication",
        Phase: pipeline.PhaseAuthentication,
        Mode:  pipeline.ModeSequential,
        Hooks: []pipeline.Hook{
            extpoints.NewExtensionPointHook(registry, "authentication"),
        },
    })
    
    // 4. 执行并验证
    envelope := &domain.PipelineRequest{...}
    err := p.Execute(context.Background(), envelope)
    // 断言...
}
```

## 最佳实践

### 1. 插件职责单一

每个插件只做一件事：

```go
// ✅ 好：只做认证
type AuthPlugin struct { ... }

// ❌ 坏：认证+路由+日志
type MegaPlugin struct { ... }
```

### 2. 使用 Metadata 传递数据

```go
func (p *MyPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
    // 执行逻辑
    userID := "user-123"
    
    // 通过 Metadata 传递给下游
    return &extpoints.ExecutionResult{
        Success: true,
        Metadata: map[string]interface{}{
            "user_id": userID,
        },
    }, nil
}
```

### 3. 错误处理

```go
func (p *MyPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
    // 非致命错误：返回 Success=false，不返回 error
    if validationFailed {
        return &extpoints.ExecutionResult{
            Success: false,
            Metadata: map[string]interface{}{
                "validation_error": "invalid input",
            },
        }, nil
    }
    
    // 致命错误：返回 error（但因为 fail-open，请求仍会继续）
    if criticalError {
        return nil, fmt.Errorf("critical: %w", err)
    }
    
    return &extpoints.ExecutionResult{Success: true}, nil
}
```

### 4. 使用 Context 超时

```go
func (p *MyPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
    // 使用提供的 Context
    select {
    case result := <-doWork(ctx.Context):
        return result, nil
    case <-ctx.Context.Done():
        return nil, ctx.Context.Err()
    }
}
```

## 故障排查

### 插件未执行

检查：
1. 插件是否正确注册到 registry
2. Phase 是否有映射（参考 Phase 映射表）
3. `Enabled()` 是否返回 true
4. Pipeline 是否执行到对应的 Stage

### 数据未同步

检查：
1. 是否在 `ExecutionResult.Metadata` 中返回数据
2. 下游代码是否检查 `envelope.Metadata["plugin_{id}_{key}"]`
3. 插件是否成功执行（Success=true）

### 性能问题

检查：
1. 插件逻辑是否有阻塞调用
2. 是否设置了合理的 Timeout
3. 是否有太多插件注册在同一 Phase

## 参考

- **示例插件**：`plugin-runtime/demo/demo_plugins.go`
- **集成测试**：`extpoints/integration_test.go`
- **桥接实现**：`extpoints/bridge.go`
- **生产集成**：`cmd/gateway/main_pipeline.go`

## 版本历史

- **2026-11-19**: 集成到生产 Pipeline，添加 ExtensionPointHook 桥接
- **2026-11-17**: 完成 ExecutionEngine 和 demo 插件
- **2026-11-16**: 初始设计和接口定义

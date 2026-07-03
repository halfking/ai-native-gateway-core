# Hook框架

## 概述

Hook框架是llm-gateway-go的核心基础设施，提供统一的插件化架构。所有企业功能都可以通过Hook形式动态加载，支持配置驱动、优先级控制、热更新等特性。

## 架构设计

### 核心组件

1. **Hook接口** (`types.go`)
   - 定义了所有Hook必须实现的接口
   - 支持可配置的Hook（ConfigurableHook）
   - 提供Environment用于Hook间数据共享

2. **HookRegistry** (`registry.go`)
   - 统一的Hook注册中心
   - 按Phase和Priority管理Hook执行顺序
   - 支持超时控制和错误处理
   - 区分关键阶段和非关键阶段

3. **ConfigManager** (`config_manager.go`)
   - 基于YAML的配置管理
   - 支持配置热更新（3秒轮询）
   - 线程安全的配置读写

4. **MetricsCollector** (`metrics.go`)
   - 基于Prometheus的指标收集
   - 记录Hook执行次数、耗时、失败率等

## 使用示例

### 1. 实现一个Hook

```go
package myhook

import (
    "context"
    "github.com/kaixuan/llm-gateway-go/domains/hooks"
)

type MyHook struct {
    enabled bool
    config  map[string]interface{}
}

func NewMyHook() *MyHook {
    return &MyHook{enabled: true}
}

func (h *MyHook) Name() string {
    return "my-hook"
}

func (h *MyHook) Priority() int {
    return 50
}

func (h *MyHook) Enabled() bool {
    return h.enabled
}

func (h *MyHook) Phase() hooks.Phase {
    return hooks.PhasePostUpstream
}

func (h *MyHook) Execute(ctx context.Context, env *hooks.Environment) error {
    // 实现Hook逻辑
    env.Metadata["my-key"] = "my-value"
    return nil
}

// 可选：支持配置热更新
func (h *MyHook) OnConfigChange(config map[string]interface{}) error {
    h.config = config
    if enabled, ok := config["enabled"].(bool); ok {
        h.enabled = enabled
    }
    return nil
}
```

### 2. 注册Hook

```go
package main

import (
    "github.com/kaixuan/llm-gateway-go/domains/hooks"
    "github.com/kaixuan/llm-gateway-go/domains/hooks/myhook"
)

func main() {
    // 创建ConfigManager
    configManager, err := hooks.NewConfigManager("config/hooks.yaml")
    if err != nil {
        log.Fatal(err)
    }

    // 创建HookRegistry
    registry := hooks.NewHookRegistry(
        configManager,
        hooks.NewMetricsCollector(),
        logger,
    )

    // 注册Hook
    registry.Register(myhook.NewMyHook())
    
    // 启动配置热更新监控
    configManager.Watch(func() {
        registry.ReloadConfig()
    })
}
```

### 3. 执行Hook

```go
func HandleRequest(ctx context.Context, req *Request) (*Response, error) {
    // 构建Environment
    env := hooks.NewEnvironment(req.ID)
    env.Request = req
    env.Session = session
    
    // 执行PreRouting阶段的Hook
    if err := registry.Execute(ctx, hooks.PhasePreRouting, env); err != nil {
        return nil, err
    }
    
    // 检查是否中止
    if env.Abort {
        return abortResponse(env.AbortReason), nil
    }
    
    // 继续处理...
}
```

## 执行阶段

Hook框架定义了5个执行阶段：

1. **PhasePreRouting** - 路由前（认证、审计）
2. **PhaseRouting** - 路由中（模型选择）
3. **PhasePreUpstream** - 上游前（压缩、转换）
4. **PhasePostUpstream** - 上游后（缓存、脱敏）
5. **PhasePostResponse** - 响应后（异步处理）

### 阶段特性

- **关键阶段**（PreRouting, Routing）：Hook失败会中止请求
- **非关键阶段**（PreUpstream, PostUpstream, PostResponse）：Hook失败记录错误但继续执行

## 配置示例

```yaml
# config/hooks.yaml
hooks:
  - name: my-hook
    enabled: true
    priority: 50
    phase: post_upstream
    timeout: 5s
    config:
      custom_key: custom_value
```

## 性能指标

- **Hook链执行延迟**: P95 < 50ms
- **单个Hook执行**: P95 < 10ms
- **配置热更新延迟**: < 3s

## 监控指标

```
# Hook执行次数
llmgw_hook_executions_total{hook="hook-name", phase="pre_routing", status="success"}

# Hook执行时长
llmgw_hook_duration_seconds{hook="hook-name", phase="pre_routing"}

# Hook失败次数
llmgw_hook_failures_total{hook="hook-name", phase="pre_routing", error_type="timeout"}

# Hook跳过次数
llmgw_hook_skipped_total{hook="hook-name", phase="pre_routing"}

# Hook超时次数
llmgw_hook_timeout_total{hook="hook-name", phase="pre_routing"}
```

## 测试

```bash
# 运行测试
go test ./domains/hooks/...

# 运行测试并查看覆盖率
go test -coverprofile=coverage.out ./domains/hooks/
go tool cover -html=coverage.out

# 当前测试覆盖率: 99.0%
```

## 最佳实践

1. **Hook命名**: 使用描述性名称，全局唯一
2. **优先级**: 0-1000，越小越先执行，建议间隔10
3. **超时控制**: 设置合理的timeout，避免阻塞
4. **错误处理**: 关键操作放在PreRouting/Routing阶段
5. **数据共享**: 使用Environment.Metadata在Hook间传递数据
6. **配置热更新**: 实现ConfigurableHook接口支持动态配置

## 依赖关系

- `domains/session`: Session结构体
- `gopkg.in/yaml.v3`: YAML配置解析
- `github.com/prometheus/client_golang`: Prometheus指标
- `log/slog`: 日志记录

## 下游任务

以下任务依赖Hook框架：

- **Task B1**: Memora自动沉淀
- **Task B2**: 输出脱敏插件
- **Task B3**: 会话编辑器
- **Task B4**: Vibe Coding评估

## 维护者

- **负责人**: Go后端团队
- **创建时间**: 2026-07-03
- **版本**: v1.0

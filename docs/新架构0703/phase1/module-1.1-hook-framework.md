# Module 1.1: Hook框架增强

> **模块ID**: 1.1  
> **负责人**: 开发者A  
> **工期**: 1周  
> **优先级**: P0 (最高)  
> **依赖**: 无  
> **状态**: 待开始

---

## 📋 模块概述

建立统一的Hook插件框架，支持插件注册、配置管理、优先级控制、执行编排和可观测性。

---

## 🎯 目标

1. **统一接口**: 定义标准Hook接口，所有插件遵循统一规范
2. **注册机制**: HookRegistry支持动态注册和卸载
3. **配置管理**: 支持YAML配置文件和热更新
4. **执行编排**: 按Phase和Priority顺序执行Hook链
5. **可观测性**: Prometheus指标、结构化日志、分布式追踪

---

## 🏗️ 架构设计

### 1. 核心组件

```
┌─────────────────────────────────────────────────────┐
│              HookRegistry (注册中心)                 │
│  ┌────────────────────────────────────────────────┐ │
│  │  hooks map[Phase][]Hook                        │ │
│  │  config *HookConfig                            │ │
│  │  metrics *HookMetrics                          │ │
│  └────────────────────────────────────────────────┘ │
└────────────────┬────────────────────────────────────┘
                 │
    ┌────────────┼────────────┐
    ▼            ▼            ▼
┌─────────┐ ┌─────────┐ ┌─────────┐
│ Hook 1  │ │ Hook 2  │ │ Hook N  │
│Priority │ │Priority │ │Priority │
│   10    │ │   20    │ │   100   │
└─────────┘ └─────────┘ └─────────┘
```

### 2. 数据流

```
请求到达
    ↓
HookRegistry.Execute(PhasePreRouting, env)
    ├─ 1. 获取该Phase的所有Hook
    ├─ 2. 按Priority排序
    ├─ 3. 遍历执行
    │     ├─ 检查Enabled状态
    │     ├─ 调用Hook.Execute()
    │     ├─ 记录metrics
    │     ├─ 记录日志
    │     └─ 处理错误
    └─ 4. 返回结果
    ↓
继续下一阶段
```

---

## 📊 数据结构

### 1. Hook接口

```go
// domains/hooks/types.go

package hooks

import (
    "context"
    "time"
)

// Hook 统一插件接口
type Hook interface {
    // Name 返回插件名称（全局唯一）
    Name() string
    
    // Priority 返回执行优先级（0-1000，越小越先执行）
    Priority() int
    
    // Enabled 返回是否启用
    Enabled() bool
    
    // Phase 返回执行阶段
    Phase() Phase
    
    // Execute 执行Hook逻辑
    Execute(ctx context.Context, env *Environment) error
    
    // OnConfigChange 配置变更回调（可选）
    OnConfigChange(config map[string]interface{}) error
}

// Phase Hook执行阶段
type Phase string

const (
    PhasePreRouting    Phase = "pre_routing"      // 路由前：认证、审计、检测
    PhaseRouting       Phase = "routing"          // 路由中：模型选择、凭据选择
    PhasePreUpstream   Phase = "pre_upstream"     // 上游前：压缩、转换
    PhasePostUpstream  Phase = "post_upstream"    // 上游后：缓存、脱敏
    PhasePostResponse  Phase = "post_response"    // 响应后：异步处理
)

// Environment Hook执行环境（请求上下文）
type Environment struct {
    // 请求标识
    RequestID       string
    TenantID        string
    SessionKey      string
    TaskID          string
    
    // 请求数据
    Request         *Request          // 客户端请求
    Response        *Response         // 客户端响应
    UpstreamRequest *UpstreamRequest  // 上游请求
    UpstreamResponse *UpstreamResponse // 上游响应
    
    // 会话信息
    Session         *Session
    
    // 元数据（Hook间共享数据）
    Metadata        map[string]interface{}
    
    // 时间戳
    StartTime       time.Time
    
    // 上下文控制
    Skip            bool              // 跳过后续Hook
    Abort           bool              // 中止请求
    AbortReason     string
}

// Request 客户端请求
type Request struct {
    Method      string
    Path        string
    Headers     map[string]string
    Body        []byte
    QueryParams map[string]string
}

// Response 客户端响应
type Response struct {
    StatusCode int
    Headers    map[string]string
    Body       []byte
    IsStream   bool
}
```

### 2. HookRegistry注册中心

```go
// domains/hooks/registry.go

package hooks

import (
    "context"
    "fmt"
    "sort"
    "sync"
    "time"
)

// HookRegistry Hook注册中心
type HookRegistry struct {
    // hooks 按Phase分组的Hook列表
    hooks map[Phase][]Hook
    
    // config 配置管理器
    config *ConfigManager
    
    // metrics 指标收集器
    metrics *MetricsCollector
    
    // logger 日志记录器
    logger Logger
    
    // mu 读写锁
    mu sync.RWMutex
    
    // errorHandler 错误处理器
    errorHandler ErrorHandler
}

// NewHookRegistry 创建注册中心
func NewHookRegistry(
    config *ConfigManager,
    metrics *MetricsCollector,
    logger Logger,
) *HookRegistry {
    return &HookRegistry{
        hooks:        make(map[Phase][]Hook),
        config:       config,
        metrics:      metrics,
        logger:       logger,
        errorHandler: DefaultErrorHandler(),
    }
}

// Register 注册Hook
func (r *HookRegistry) Register(hook Hook) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    // 验证
    if hook.Name() == "" {
        return fmt.Errorf("hook name cannot be empty")
    }
    
    // 检查重复
    phase := hook.Phase()
    for _, h := range r.hooks[phase] {
        if h.Name() == hook.Name() {
            return fmt.Errorf("hook %s already registered", hook.Name())
        }
    }
    
    // 添加
    r.hooks[phase] = append(r.hooks[phase], hook)
    
    // 排序（按优先级）
    sort.Slice(r.hooks[phase], func(i, j int) bool {
        return r.hooks[phase][i].Priority() < r.hooks[phase][j].Priority()
    })
    
    r.logger.Info("hook registered",
        "name", hook.Name(),
        "phase", phase,
        "priority", hook.Priority())
    
    return nil
}

// Unregister 卸载Hook
func (r *HookRegistry) Unregister(name string, phase Phase) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    hooks := r.hooks[phase]
    for i, h := range hooks {
        if h.Name() == name {
            r.hooks[phase] = append(hooks[:i], hooks[i+1:]...)
            r.logger.Info("hook unregistered", "name", name, "phase", phase)
            return nil
        }
    }
    
    return fmt.Errorf("hook %s not found in phase %s", name, phase)
}

// Execute 执行指定Phase的Hook链
func (r *HookRegistry) Execute(
    ctx context.Context,
    phase Phase,
    env *Environment,
) error {
    r.mu.RLock()
    hooks := r.hooks[phase]
    r.mu.RUnlock()
    
    if len(hooks) == 0 {
        return nil
    }
    
    r.logger.Debug("executing hook chain",
        "phase", phase,
        "hook_count", len(hooks),
        "request_id", env.RequestID)
    
    for _, hook := range hooks {
        // 检查启用状态
        if !hook.Enabled() {
            r.logger.Debug("hook skipped (disabled)",
                "name", hook.Name(),
                "phase", phase)
            continue
        }
        
        // 检查Skip标志
        if env.Skip {
            r.logger.Debug("hook skipped (skip flag)",
                "name", hook.Name(),
                "phase", phase)
            continue
        }
        
        // 检查Abort标志
        if env.Abort {
            r.logger.Warn("hook chain aborted",
                "phase", phase,
                "reason", env.AbortReason)
            return fmt.Errorf("hook chain aborted: %s", env.AbortReason)
        }
        
        // 执行Hook
        start := time.Now()
        err := r.executeHook(ctx, hook, env)
        duration := time.Since(start)
        
        // 记录指标
        r.metrics.RecordHookExecution(
            hook.Name(),
            string(phase),
            err == nil,
            duration,
        )
        
        // 处理错误
        if err != nil {
            r.logger.Error("hook execution failed",
                "name", hook.Name(),
                "phase", phase,
                "error", err,
                "duration", duration)
            
            // 错误处理
            action := r.errorHandler.Handle(hook, err)
            switch action {
            case ErrorActionAbort:
                return fmt.Errorf("hook %s failed: %w", hook.Name(), err)
            case ErrorActionSkip:
                continue
            case ErrorActionRetry:
                // TODO: 实现重试逻辑
                continue
            }
        }
        
        r.logger.Debug("hook executed successfully",
            "name", hook.Name(),
            "phase", phase,
            "duration", duration)
    }
    
    return nil
}

// executeHook 执行单个Hook（带超时控制）
func (r *HookRegistry) executeHook(
    ctx context.Context,
    hook Hook,
    env *Environment,
) error {
    // 超时控制
    timeout := r.config.GetHookTimeout(hook.Name())
    if timeout > 0 {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeout(ctx, timeout)
        defer cancel()
    }
    
    // 执行
    return hook.Execute(ctx, env)
}

// GetHooks 获取指定Phase的所有Hook
func (r *HookRegistry) GetHooks(phase Phase) []Hook {
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    hooks := make([]Hook, len(r.hooks[phase]))
    copy(hooks, r.hooks[phase])
    return hooks
}

// ReloadConfig 重新加载配置
func (r *HookRegistry) ReloadConfig() error {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    if err := r.config.Reload(); err != nil {
        return fmt.Errorf("failed to reload config: %w", err)
    }
    
    // 通知所有Hook配置变更
    for _, hooks := range r.hooks {
        for _, hook := range hooks {
            if configurable, ok := hook.(ConfigurableHook); ok {
                config := r.config.GetHookConfig(hook.Name())
                if err := configurable.OnConfigChange(config); err != nil {
                    r.logger.Error("hook config change failed",
                        "name", hook.Name(),
                        "error", err)
                }
            }
        }
    }
    
    r.logger.Info("hook config reloaded")
    return nil
}
```

### 3. 配置管理器

```go
// domains/hooks/config_manager.go

package hooks

import (
    "fmt"
    "os"
    "sync"
    "time"
    
    "gopkg.in/yaml.v3"
)

// HookConfig Hook配置
type HookConfig struct {
    Name     string                 `yaml:"name"`
    Enabled  bool                   `yaml:"enabled"`
    Priority int                    `yaml:"priority"`
    Phase    string                 `yaml:"phase"`
    Timeout  time.Duration          `yaml:"timeout"`
    Config   map[string]interface{} `yaml:"config"`
}

// ConfigManager 配置管理器
type ConfigManager struct {
    // configFile 配置文件路径
    configFile string
    
    // hooks Hook配置列表
    hooks map[string]*HookConfig
    
    // mu 读写锁
    mu sync.RWMutex
    
    // lastModTime 最后修改时间
    lastModTime time.Time
    
    // watcher 文件监控
    watcher *FileWatcher
}

// NewConfigManager 创建配置管理器
func NewConfigManager(configFile string) (*ConfigManager, error) {
    cm := &ConfigManager{
        configFile: configFile,
        hooks:      make(map[string]*HookConfig),
    }
    
    // 加载配置
    if err := cm.Load(); err != nil {
        return nil, err
    }
    
    // 启动文件监控
    cm.startWatcher()
    
    return cm, nil
}

// Load 加载配置文件
func (cm *ConfigManager) Load() error {
    data, err := os.ReadFile(cm.configFile)
    if err != nil {
        return fmt.Errorf("failed to read config file: %w", err)
    }
    
    var config struct {
        Hooks []*HookConfig `yaml:"hooks"`
    }
    
    if err := yaml.Unmarshal(data, &config); err != nil {
        return fmt.Errorf("failed to parse config: %w", err)
    }
    
    cm.mu.Lock()
    defer cm.mu.Unlock()
    
    // 更新配置
    cm.hooks = make(map[string]*HookConfig)
    for _, hc := range config.Hooks {
        cm.hooks[hc.Name] = hc
    }
    
    // 更新修改时间
    if info, err := os.Stat(cm.configFile); err == nil {
        cm.lastModTime = info.ModTime()
    }
    
    return nil
}

// Reload 重新加载配置
func (cm *ConfigManager) Reload() error {
    return cm.Load()
}

// GetHookConfig 获取Hook配置
func (cm *ConfigManager) GetHookConfig(name string) map[string]interface{} {
    cm.mu.RLock()
    defer cm.mu.RUnlock()
    
    if hc, ok := cm.hooks[name]; ok {
        return hc.Config
    }
    return nil
}

// IsEnabled 检查Hook是否启用
func (cm *ConfigManager) IsEnabled(name string) bool {
    cm.mu.RLock()
    defer cm.mu.RUnlock()
    
    if hc, ok := cm.hooks[name]; ok {
        return hc.Enabled
    }
    return false
}

// GetHookTimeout 获取Hook超时时间
func (cm *ConfigManager) GetHookTimeout(name string) time.Duration {
    cm.mu.RLock()
    defer cm.mu.RUnlock()
    
    if hc, ok := cm.hooks[name]; ok && hc.Timeout > 0 {
        return hc.Timeout
    }
    return 30 * time.Second // 默认30秒
}

// startWatcher 启动文件监控
func (cm *ConfigManager) startWatcher() {
    cm.watcher = NewFileWatcher(cm.configFile, func() {
        if err := cm.Reload(); err != nil {
            // 日志记录错误
            fmt.Printf("failed to reload config: %v\n", err)
        }
    })
    cm.watcher.Start()
}
```

### 4. 指标收集器

```go
// domains/hooks/metrics.go

package hooks

import (
    "time"
    
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

// MetricsCollector 指标收集器
type MetricsCollector struct {
    // Hook执行次数
    executionCount *prometheus.CounterVec
    
    // Hook执行时长
    executionDuration *prometheus.HistogramVec
    
    // Hook失败次数
    failureCount *prometheus.CounterVec
}

// NewMetricsCollector 创建指标收集器
func NewMetricsCollector() *MetricsCollector {
    return &MetricsCollector{
        executionCount: promauto.NewCounterVec(
            prometheus.CounterOpts{
                Name: "llmgw_hook_executions_total",
                Help: "Total number of hook executions",
            },
            []string{"hook", "phase", "status"},
        ),
        executionDuration: promauto.NewHistogramVec(
            prometheus.HistogramOpts{
                Name:    "llmgw_hook_duration_seconds",
                Help:    "Hook execution duration in seconds",
                Buckets: prometheus.DefBuckets,
            },
            []string{"hook", "phase"},
        ),
        failureCount: promauto.NewCounterVec(
            prometheus.CounterOpts{
                Name: "llmgw_hook_failures_total",
                Help: "Total number of hook failures",
            },
            []string{"hook", "phase", "error_type"},
        ),
    }
}

// RecordHookExecution 记录Hook执行
func (mc *MetricsCollector) RecordHookExecution(
    hookName, phase string,
    success bool,
    duration time.Duration,
) {
    status := "success"
    if !success {
        status = "failure"
    }
    
    mc.executionCount.WithLabelValues(hookName, phase, status).Inc()
    mc.executionDuration.WithLabelValues(hookName, phase).Observe(duration.Seconds())
    
    if !success {
        mc.failureCount.WithLabelValues(hookName, phase, "unknown").Inc()
    }
}
```

---

## 🔌 API接口

### 1. 管理API

```go
// admin/hook_admin_api.go

// GET /api/admin/hooks - 获取所有Hook状态
func (h *Handler) handleGetHooks(w http.ResponseWriter, r *http.Request) {
    hooks := h.hookRegistry.GetAllHooks()
    
    response := make([]map[string]interface{}, 0)
    for _, hook := range hooks {
        response = append(response, map[string]interface{}{
            "name":     hook.Name(),
            "phase":    hook.Phase(),
            "priority": hook.Priority(),
            "enabled":  hook.Enabled(),
        })
    }
    
    writeJSON(w, http.StatusOK, response)
}

// POST /api/admin/hooks/reload - 重新加载配置
func (h *Handler) handleReloadHooks(w http.ResponseWriter, r *http.Request) {
    if err := h.hookRegistry.ReloadConfig(); err != nil {
        writeError(w, http.StatusInternalServerError, err.Error())
        return
    }
    
    writeJSON(w, http.StatusOK, map[string]interface{}{
        "status": "ok",
        "message": "hooks config reloaded",
    })
}

// PUT /api/admin/hooks/{name}/enable - 启用Hook
// PUT /api/admin/hooks/{name}/disable - 禁用Hook
```

---

## ✅ 验收标准

### 功能验收
- [ ] Hook可以成功注册和卸载
- [ ] 配置文件变更后3秒内生效
- [ ] Hook按Priority正确排序执行
- [ ] 禁用的Hook不会被执行
- [ ] Environment数据在Hook间正确传递
- [ ] 错误处理机制正常工作

### 性能验收
- [ ] Hook链执行延迟 < 50ms (无实际业务逻辑)
- [ ] 配置热更新不影响正在处理的请求
- [ ] 1000个Hook注册无性能问题

### 测试验收
- [ ] 单元测试覆盖率 > 90%
- [ ] 集成测试通过
- [ ] 压力测试通过 (10000 req/s)

---

## 🧪 测试用例

### 1. 单元测试

```go
// domains/hooks/registry_test.go

func TestHookRegistry_Register(t *testing.T) {
    registry := NewHookRegistry(nil, nil, nil)
    
    hook := &MockHook{
        name:     "test-hook",
        priority: 10,
        phase:    PhasePreRouting,
    }
    
    err := registry.Register(hook)
    assert.NoError(t, err)
    
    hooks := registry.GetHooks(PhasePreRouting)
    assert.Len(t, hooks, 1)
    assert.Equal(t, "test-hook", hooks[0].Name())
}

func TestHookRegistry_Execute(t *testing.T) {
    registry := NewHookRegistry(nil, NewMetricsCollector(), NewTestLogger())
    
    executed := false
    hook := &MockHook{
        name:     "test-hook",
        priority: 10,
        phase:    PhasePreRouting,
        execFunc: func(ctx context.Context, env *Environment) error {
            executed = true
            return nil
        },
    }
    
    registry.Register(hook)
    
    env := &Environment{RequestID: "test-123"}
    err := registry.Execute(context.Background(), PhasePreRouting, env)
    
    assert.NoError(t, err)
    assert.True(t, executed)
}

func TestHookRegistry_Priority(t *testing.T) {
    registry := NewHookRegistry(nil, nil, nil)
    
    var executionOrder []string
    
    hook1 := &MockHook{
        name:     "hook-20",
        priority: 20,
        phase:    PhasePreRouting,
        execFunc: func(ctx context.Context, env *Environment) error {
            executionOrder = append(executionOrder, "hook-20")
            return nil
        },
    }
    
    hook2 := &MockHook{
        name:     "hook-10",
        priority: 10,
        phase:    PhasePreRouting,
        execFunc: func(ctx context.Context, env *Environment) error {
            executionOrder = append(executionOrder, "hook-10")
            return nil
        },
    }
    
    registry.Register(hook1)
    registry.Register(hook2)
    
    env := &Environment{}
    registry.Execute(context.Background(), PhasePreRouting, env)
    
    assert.Equal(t, []string{"hook-10", "hook-20"}, executionOrder)
}
```

### 2. 集成测试

```go
// domains/hooks/integration_test.go

func TestHookChain_EndToEnd(t *testing.T) {
    // 准备
    registry := setupTestRegistry()
    
    // 注册多个Hook
    registry.Register(NewAuthHook())
    registry.Register(NewAuditHook())
    registry.Register(NewCompressionHook())
    
    // 模拟请求
    env := &Environment{
        RequestID: "req-123",
        Request: &Request{
            Method: "POST",
            Path:   "/v1/chat/completions",
            Body:   loadTestRequest(),
        },
    }
    
    ctx := context.Background()
    
    // 执行Pre-Routing阶段
    err := registry.Execute(ctx, PhasePreRouting, env)
    assert.NoError(t, err)
    
    // 验证结果
    assert.NotNil(t, env.Metadata["auth_user"])
    assert.NotNil(t, env.Metadata["audit_id"])
}
```

---

## 📝 开发提示词

```
你是一个资深的Go后端工程师，负责开发llm-gateway-go的Hook插件框架。

## 背景
llm-gateway-go是一个企业级LLM网关，需要实现插件化架构，支持各种企业功能以Hook形式动态加载。

## 技术栈
- Go 1.25+
- Prometheus (指标)
- YAML (配置)
- Context (上下文控制)

## 任务
实现统一的Hook插件框架，包括：
1. Hook接口定义 (domains/hooks/types.go)
2. HookRegistry注册中心 (domains/hooks/registry.go)
3. ConfigManager配置管理 (domains/hooks/config_manager.go)
4. MetricsCollector指标收集 (domains/hooks/metrics.go)

## 核心要求
1. Hook按Phase分组，按Priority排序执行
2. 支持配置文件热更新（3秒内生效）
3. Environment在Hook间传递数据
4. 完善的错误处理和降级
5. Prometheus指标埋点
6. 单元测试覆盖率>90%

## 数据结构
参考上文的详细数据结构定义。

## 验收标准
参考上文的验收标准。

## 测试用例
参考上文的测试用例。

现在请开始开发，先实现types.go定义基础接口。
```

---

## 📚 参考资料

- [Go Context最佳实践](https://go.dev/blog/context)
- [Prometheus Go客户端](https://github.com/prometheus/client_golang)
- [YAML配置解析](https://github.com/go-yaml/yaml)

---

**文档维护**: 开发者A  
**最后更新**: 2026-07-03

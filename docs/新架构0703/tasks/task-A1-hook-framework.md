# Task A1: Hook框架增强

> **任务ID**: A1  
> **任务类型**: 基础设施（P0）  
> **负责团队**: Go后端  
> **预计工期**: 1周  
> **依赖**: 无  
> **状态**: 可立即开始

---

## 📋 任务概述

实现统一的Hook插件框架，作为llm-gateway-go插件化架构的核心基础设施。所有企业功能将以Hook形式动态加载，支持配置驱动、优先级控制、热更新等特性。

---

## 🎯 任务目标

1. 实现HookRegistry统一注册中心
2. 实现ConfigManager配置管理器（支持热更新）
3. 实现MetricsCollector指标收集器
4. 实现Hook执行编排逻辑
5. 单元测试覆盖率>90%

---

## 🔧 技术栈

- **语言**: Go 1.25+
- **配置**: YAML (gopkg.in/yaml.v3)
- **指标**: Prometheus (github.com/prometheus/client_golang)
- **日志**: slog (标准库)
- **测试**: testing (标准库)

---

## 📂 项目上下文

### 当前代码结构
```
llm-gateway-go/
├── domains/
│   └── hooks/              ← 你将在这里工作
│       ├── audit/          ✅ 已有：会话审计Hook
│       ├── compression/    ✅ 已有：会话压缩Hook
│       ├── promptinjection/✅ 已有：提示词注入Hook
│       ├── cache/          ✅ 已有：语义缓存Hook
│       └── [你要创建的新文件]
├── config/
│   └── hooks.yaml          ← 配置文件
└── cmd/gateway/
    └── main.go             ← 启动入口
```

### 现有Hook示例
查看现有实现参考：
- `domains/hooks/compression/hook.go` - 压缩Hook实现
- `domains/hooks/audit/hook.go` - 审计Hook实现

---

## 📊 详细需求

### Requirement 1: Hook接口定义

创建文件: `domains/hooks/types.go`

```go
package hooks

import (
    "context"
    "time"
)

// Hook 统一插件接口
// 所有Hook插件必须实现此接口
type Hook interface {
    // Name 返回Hook名称（全局唯一）
    // 示例: "session-audit", "output-sanitizer"
    Name() string
    
    // Priority 返回执行优先级（0-1000）
    // 数值越小越先执行
    // 建议: 认证10, 审计20, 业务逻辑50-100, 异步处理200+
    Priority() int
    
    // Enabled 返回是否启用
    // 从配置文件读取或环境变量控制
    Enabled() bool
    
    // Phase 返回执行阶段
    Phase() Phase
    
    // Execute 执行Hook逻辑
    // ctx: 上下文控制（超时、取消）
    // env: 请求环境（包含请求、响应、会话等）
    // 返回error表示Hook执行失败
    Execute(ctx context.Context, env *Environment) error
}

// ConfigurableHook 可配置Hook接口（可选）
// 实现此接口的Hook可以接收配置变更通知
type ConfigurableHook interface {
    Hook
    OnConfigChange(config map[string]interface{}) error
}

// Phase Hook执行阶段
type Phase string

const (
    // PhasePreRouting 路由前：认证、限流、审计、检测
    PhasePreRouting Phase = "pre_routing"
    
    // PhaseRouting 路由中：模型选择、凭据选择
    PhaseRouting Phase = "routing"
    
    // PhasePreUpstream 上游前：会话压缩、请求转换
    PhasePreUpstream Phase = "pre_upstream"
    
    // PhasePostUpstream 上游后：响应缓存、输出脱敏
    PhasePostUpstream Phase = "post_upstream"
    
    // PhasePostResponse 响应后：异步处理（Memora、总结等）
    PhasePostResponse Phase = "post_response"
)

// Environment Hook执行环境
// 包含请求生命周期的所有数据
type Environment struct {
    // 请求标识
    RequestID  string
    TenantID   string
    SessionKey string
    TaskID     string
    
    // 请求响应数据
    Request          *Request
    Response         *Response
    UpstreamRequest  *UpstreamRequest
    UpstreamResponse *UpstreamResponse
    
    // 会话信息
    Session *Session
    
    // 元数据（Hook间共享数据）
    Metadata map[string]interface{}
    
    // 时间戳
    StartTime time.Time
    
    // 控制标志
    Skip        bool   // 跳过后续Hook
    Abort       bool   // 中止请求
    AbortReason string // 中止原因
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

// UpstreamRequest 上游请求（可能被Hook修改）
type UpstreamRequest struct {
    Model    string
    Messages []Message
    Options  map[string]interface{}
}

// UpstreamResponse 上游响应
type UpstreamResponse struct {
    Content  string
    Metadata map[string]interface{}
}

// Message 消息结构
type Message struct {
    Role    string
    Content string
}

// Session 会话信息（简化版，实际从domains/session导入）
type Session struct {
    SessionKey string
    TaskID     string
    CreatedAt  time.Time
}
```

---

### Requirement 2: HookRegistry注册中心

创建文件: `domains/hooks/registry.go`

**核心功能**:
1. 注册/卸载Hook
2. 按Phase分组存储
3. 按Priority排序
4. 执行Hook链
5. 错误处理

**关键代码框架**:
```go
package hooks

import (
    "context"
    "fmt"
    "sort"
    "sync"
    "time"
)

type HookRegistry struct {
    hooks   map[Phase][]Hook
    config  *ConfigManager
    metrics *MetricsCollector
    logger  Logger
    mu      sync.RWMutex
}

func NewHookRegistry(
    config *ConfigManager,
    metrics *MetricsCollector,
    logger Logger,
) *HookRegistry {
    return &HookRegistry{
        hooks:   make(map[Phase][]Hook),
        config:  config,
        metrics: metrics,
        logger:  logger,
    }
}

// Register 注册Hook
func (r *HookRegistry) Register(hook Hook) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    // 1. 验证Hook name非空
    // 2. 检查是否重复注册
    // 3. 添加到对应Phase
    // 4. 按Priority排序
    // 5. 记录日志
    
    phase := hook.Phase()
    r.hooks[phase] = append(r.hooks[phase], hook)
    
    // 排序
    sort.Slice(r.hooks[phase], func(i, j int) bool {
        return r.hooks[phase][i].Priority() < r.hooks[phase][j].Priority()
    })
    
    r.logger.Info("hook registered",
        "name", hook.Name(),
        "phase", phase,
        "priority", hook.Priority())
    
    return nil
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
    
    for _, hook := range hooks {
        // 1. 检查Enabled
        if !hook.Enabled() {
            continue
        }
        
        // 2. 检查Skip/Abort标志
        if env.Skip || env.Abort {
            break
        }
        
        // 3. 执行Hook（带超时控制）
        start := time.Now()
        err := r.executeHook(ctx, hook, env)
        duration := time.Since(start)
        
        // 4. 记录指标
        r.metrics.RecordHookExecution(
            hook.Name(),
            string(phase),
            err == nil,
            duration,
        )
        
        // 5. 错误处理
        if err != nil {
            r.logger.Error("hook execution failed",
                "name", hook.Name(),
                "phase", phase,
                "error", err,
                "duration", duration)
            return fmt.Errorf("hook %s failed: %w", hook.Name(), err)
        }
    }
    
    return nil
}

// executeHook 执行单个Hook（带超时控制）
func (r *HookRegistry) executeHook(
    ctx context.Context,
    hook Hook,
    env *Environment,
) error {
    // 从配置获取超时时间
    timeout := r.config.GetHookTimeout(hook.Name())
    if timeout > 0 {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeout(ctx, timeout)
        defer cancel()
    }
    
    return hook.Execute(ctx, env)
}

// ReloadConfig 重新加载配置
func (r *HookRegistry) ReloadConfig() error {
    // 1. 重新加载配置文件
    // 2. 通知所有ConfigurableHook
    
    return r.config.Reload()
}
```

**必须实现的方法**:
- `Register(hook Hook) error`
- `Unregister(name string, phase Phase) error`
- `Execute(ctx, phase, env) error`
- `GetHooks(phase Phase) []Hook`
- `ReloadConfig() error`

---

### Requirement 3: ConfigManager配置管理器

创建文件: `domains/hooks/config_manager.go`

**核心功能**:
1. 加载YAML配置文件
2. 解析Hook配置
3. 文件监控（热更新）
4. 配置查询

**配置文件格式** (`config/hooks.yaml`):
```yaml
hooks:
  - name: session-audit
    enabled: true
    priority: 10
    phase: pre_routing
    timeout: 5s
    config:
      async: true
      batch_size: 50
  
  - name: output-sanitizer
    enabled: true
    priority: 30
    phase: post_upstream
    timeout: 10s
    config:
      pii_detection: true
      sensitive_words: true
```

**关键代码框架**:
```go
package hooks

import (
    "fmt"
    "os"
    "sync"
    "time"
    
    "gopkg.in/yaml.v3"
)

type HookConfig struct {
    Name     string                 `yaml:"name"`
    Enabled  bool                   `yaml:"enabled"`
    Priority int                    `yaml:"priority"`
    Phase    string                 `yaml:"phase"`
    Timeout  time.Duration          `yaml:"timeout"`
    Config   map[string]interface{} `yaml:"config"`
}

type ConfigManager struct {
    configFile  string
    hooks       map[string]*HookConfig
    mu          sync.RWMutex
    lastModTime time.Time
    watcher     *FileWatcher
}

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

func (cm *ConfigManager) Load() error {
    data, err := os.ReadFile(cm.configFile)
    if err != nil {
        return fmt.Errorf("failed to read config: %w", err)
    }
    
    var config struct {
        Hooks []*HookConfig `yaml:"hooks"`
    }
    
    if err := yaml.Unmarshal(data, &config); err != nil {
        return fmt.Errorf("failed to parse config: %w", err)
    }
    
    cm.mu.Lock()
    defer cm.mu.Unlock()
    
    cm.hooks = make(map[string]*HookConfig)
    for _, hc := range config.Hooks {
        cm.hooks[hc.Name] = hc
    }
    
    return nil
}

func (cm *ConfigManager) GetHookConfig(name string) map[string]interface{} {
    cm.mu.RLock()
    defer cm.mu.RUnlock()
    
    if hc, ok := cm.hooks[name]; ok {
        return hc.Config
    }
    return nil
}

func (cm *ConfigManager) IsEnabled(name string) bool {
    cm.mu.RLock()
    defer cm.mu.RUnlock()
    
    if hc, ok := cm.hooks[name]; ok {
        return hc.Enabled
    }
    return false
}

func (cm *ConfigManager) GetHookTimeout(name string) time.Duration {
    cm.mu.RLock()
    defer cm.mu.RUnlock()
    
    if hc, ok := cm.hooks[name]; ok && hc.Timeout > 0 {
        return hc.Timeout
    }
    return 30 * time.Second // 默认30秒
}
```

**文件监控实现** (简化版，可用第三方库如fsnotify):
```go
type FileWatcher struct {
    file     string
    callback func()
    stop     chan struct{}
}

func NewFileWatcher(file string, callback func()) *FileWatcher {
    return &FileWatcher{
        file:     file,
        callback: callback,
        stop:     make(chan struct{}),
    }
}

func (fw *FileWatcher) Start() {
    go func() {
        ticker := time.NewTicker(3 * time.Second)
        defer ticker.Stop()
        
        var lastModTime time.Time
        info, _ := os.Stat(fw.file)
        if info != nil {
            lastModTime = info.ModTime()
        }
        
        for {
            select {
            case <-ticker.C:
                info, err := os.Stat(fw.file)
                if err != nil {
                    continue
                }
                
                if info.ModTime().After(lastModTime) {
                    lastModTime = info.ModTime()
                    fw.callback()
                }
            case <-fw.stop:
                return
            }
        }
    }()
}
```

---

### Requirement 4: MetricsCollector指标收集器

创建文件: `domains/hooks/metrics.go`

**Prometheus指标**:
```go
package hooks

import (
    "time"
    
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

type MetricsCollector struct {
    executionCount    *prometheus.CounterVec
    executionDuration *prometheus.HistogramVec
    failureCount      *prometheus.CounterVec
}

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
                Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
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

## ✅ 验收标准

### 功能验收
- [ ] Hook可以成功注册和卸载
- [ ] 配置文件变更后3秒内生效
- [ ] Hook按Priority正确排序执行
- [ ] 禁用的Hook不会被执行
- [ ] Environment数据在Hook间正确传递
- [ ] 超时控制正常工作

### 性能验收
- [ ] Hook链执行延迟 < 50ms (P95, 无实际业务逻辑)
- [ ] 配置热更新不影响正在处理的请求
- [ ] 1000个Hook注册无性能问题

### 测试验收
- [ ] 单元测试覆盖率 > 90%
- [ ] 所有公开方法有测试
- [ ] 并发测试通过

---

## 🧪 测试用例

### 1. 单元测试示例

创建文件: `domains/hooks/registry_test.go`

```go
package hooks

import (
    "context"
    "testing"
    "time"
)

// MockHook 测试用Mock Hook
type MockHook struct {
    name     string
    priority int
    phase    Phase
    enabled  bool
    execFunc func(context.Context, *Environment) error
}

func (m *MockHook) Name() string                                         { return m.name }
func (m *MockHook) Priority() int                                        { return m.priority }
func (m *MockHook) Enabled() bool                                        { return m.enabled }
func (m *MockHook) Phase() Phase                                         { return m.phase }
func (m *MockHook) Execute(ctx context.Context, env *Environment) error { 
    if m.execFunc != nil {
        return m.execFunc(ctx, env)
    }
    return nil 
}

func TestHookRegistry_Register(t *testing.T) {
    registry := NewHookRegistry(nil, NewMetricsCollector(), &TestLogger{})
    
    hook := &MockHook{
        name:     "test-hook",
        priority: 10,
        phase:    PhasePreRouting,
        enabled:  true,
    }
    
    err := registry.Register(hook)
    if err != nil {
        t.Fatalf("Register failed: %v", err)
    }
    
    hooks := registry.GetHooks(PhasePreRouting)
    if len(hooks) != 1 {
        t.Fatalf("Expected 1 hook, got %d", len(hooks))
    }
    
    if hooks[0].Name() != "test-hook" {
        t.Fatalf("Expected hook name 'test-hook', got %s", hooks[0].Name())
    }
}

func TestHookRegistry_Execute(t *testing.T) {
    registry := NewHookRegistry(nil, NewMetricsCollector(), &TestLogger{})
    
    executed := false
    hook := &MockHook{
        name:     "test-hook",
        priority: 10,
        phase:    PhasePreRouting,
        enabled:  true,
        execFunc: func(ctx context.Context, env *Environment) error {
            executed = true
            return nil
        },
    }
    
    registry.Register(hook)
    
    env := &Environment{
        RequestID: "test-123",
        StartTime: time.Now(),
    }
    
    err := registry.Execute(context.Background(), PhasePreRouting, env)
    if err != nil {
        t.Fatalf("Execute failed: %v", err)
    }
    
    if !executed {
        t.Fatal("Hook was not executed")
    }
}

func TestHookRegistry_Priority(t *testing.T) {
    registry := NewHookRegistry(nil, NewMetricsCollector(), &TestLogger{})
    
    var executionOrder []string
    
    hook1 := &MockHook{
        name:     "hook-20",
        priority: 20,
        phase:    PhasePreRouting,
        enabled:  true,
        execFunc: func(ctx context.Context, env *Environment) error {
            executionOrder = append(executionOrder, "hook-20")
            return nil
        },
    }
    
    hook2 := &MockHook{
        name:     "hook-10",
        priority: 10,
        phase:    PhasePreRouting,
        enabled:  true,
        execFunc: func(ctx context.Context, env *Environment) error {
            executionOrder = append(executionOrder, "hook-10")
            return nil
        },
    }
    
    registry.Register(hook1)
    registry.Register(hook2)
    
    env := &Environment{}
    registry.Execute(context.Background(), PhasePreRouting, env)
    
    if len(executionOrder) != 2 {
        t.Fatalf("Expected 2 executions, got %d", len(executionOrder))
    }
    
    if executionOrder[0] != "hook-10" || executionOrder[1] != "hook-20" {
        t.Fatalf("Wrong execution order: %v", executionOrder)
    }
}

func TestHookRegistry_Disabled(t *testing.T) {
    registry := NewHookRegistry(nil, NewMetricsCollector(), &TestLogger{})
    
    executed := false
    hook := &MockHook{
        name:     "test-hook",
        priority: 10,
        phase:    PhasePreRouting,
        enabled:  false, // 禁用
        execFunc: func(ctx context.Context, env *Environment) error {
            executed = true
            return nil
        },
    }
    
    registry.Register(hook)
    
    env := &Environment{}
    registry.Execute(context.Background(), PhasePreRouting, env)
    
    if executed {
        t.Fatal("Disabled hook should not be executed")
    }
}

// TestLogger 测试用Logger
type TestLogger struct{}

func (l *TestLogger) Info(msg string, args ...interface{})  {}
func (l *TestLogger) Error(msg string, args ...interface{}) {}
func (l *TestLogger) Debug(msg string, args ...interface{}) {}
```

---

## 📚 参考资料

### 现有代码参考
```bash
# 查看现有Hook实现
cat domains/hooks/compression/hook.go
cat domains/hooks/audit/hook.go
cat domains/hooks/promptinjection/hook.go

# 查看会话管理
cat domains/session/session.go
cat domains/session/context.go
```

### 技术文档
- [Go Context最佳实践](https://go.dev/blog/context)
- [Prometheus Go客户端](https://github.com/prometheus/client_golang)
- [YAML解析库](https://github.com/go-yaml/yaml)

---

## 🚀 开始开发

### Step 1: 创建文件
```bash
cd __LOCAL_PATH_1__

# 创建核心文件
touch domains/hooks/types.go
touch domains/hooks/registry.go
touch domains/hooks/config_manager.go
touch domains/hooks/metrics.go

# 创建测试文件
touch domains/hooks/registry_test.go
touch domains/hooks/config_manager_test.go

# 创建配置文件
mkdir -p config
touch config/hooks.yaml
```

### Step 2: 实现接口
按照上述要求实现每个文件

### Step 3: 编写测试
确保测试覆盖率>90%

### Step 4: 集成到主程序
在 `cmd/gateway/main.go` 中集成

### Step 5: 验证
运行所有测试和验收检查

---

## 💡 开发提示

1. **先实现types.go**: 定义好接口后，其他组件会更清晰
2. **参考现有Hook**: 查看已有Hook的实现模式
3. **测试驱动开发**: 先写测试，再实现功能
4. **日志详细**: 每个关键步骤都记录日志
5. **错误处理**: 所有错误都要有上下文信息

---

## 📞 需要帮助？

- **架构问题**: 查看 `docs/新架构0703/00-总体架构设计.md`
- **现有代码**: 参考 `domains/hooks/*/hook.go`
- **测试问题**: 参考 `domains/hooks/*_test.go`
- **紧急问题**: 联系架构组

---

**任务创建人**: 架构组  
**创建时间**: 2026-07-03  
**预计完成**: 2026-07-10

---

## ✅ 完成检查清单

提交前确认：
- [ ] 所有接口实现完整
- [ ] 单元测试覆盖率>90%
- [ ] 代码通过 `go vet` 和 `golint`
- [ ] 配置文件热更新工作正常
- [ ] Prometheus指标正确暴露
- [ ] 文档注释完整
- [ ] README更新（如需要）

**准备好了就开始吧！Good luck! 🚀**

# Plugin Supervisor 资源限制器

资源限制器为插件进程提供内存和 goroutine 数量的监控与限制，支持软限制（监控告警）和硬限制（强制终止）两种模式。

## 功能特性

- **内存限制**：支持多种单位格式（Mi、MB、Gi、GB、Ki）
- **Goroutine 限制**：限制插件可创建的 goroutine 数量
- **CPU 限制**：按百分比限制 CPU 使用（仅 Linux cgroup）
- **周期性监控**：可配置的检查间隔，默认 5 秒
- **平台适配**：
  - **Linux**：使用 cgroup v2 实现硬限制
  - **macOS/其他**：软限制模式，仅监控和日志告警
- **优雅关闭**：通过 `Stop()` 方法停止监控循环

## 配置格式

### 内存限制

支持以下格式：

```yaml
# 1. 带单位（推荐）
memory_limit: "256Mi"   # 256 MiB (1024^2 字节)
memory_limit: "512MB"   # 512 MB (1000^2 字节)
memory_limit: "1Gi"     # 1 GiB (1024^3 字节)
memory_limit: "2GB"     # 2 GB (1000^3 字节)
memory_limit: "1024Ki"  # 1024 KiB (1024 字节)

# 2. 纯数字（字节）
memory_limit: "268435456"  # 256 MB

# 3. 空值或 "0" 表示不限制
memory_limit: ""
memory_limit: "0"
```

**单位说明**：
- `Ki`, `Mi`, `Gi`: 二进制单位（1024 进制）
- `KB`, `MB`, `GB`: 十进制单位（1000 进制）
- 纯数字：按字节计算

### Goroutine 限制

```yaml
# 限制最大 goroutine 数量
max_goroutines: 1000

# 不限制
max_goroutines: 0
```

### CPU 限制（仅 Linux）

```yaml
# 百分比格式（1-100）
cpu_limit: "50"    # 限制 50% CPU
cpu_limit: "100"   # 限制 100% CPU（一个核心）

# 不限制
cpu_limit: "0"
cpu_limit: ""
```

### 检查间隔

```yaml
# 监控检查间隔，默认 5 秒
check_interval: 5s
check_interval: 10s
check_interval: 30s
```

## 使用示例

### 1. 基本用法

```go
import (
    "context"
    "time"
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor"
)

// 创建资源限制器
limiter := supervisor.NewResourceLimiter(
    "my-plugin",           // 插件名称
    "512Mi",              // 内存限制 512 MiB
    1000,                 // 最大 1000 个 goroutine
    "50",                 // CPU 限制 50%（仅 Linux）
    5*time.Second,        // 每 5 秒检查一次
)

// 启动监控
ctx := context.Background()
limiter.Start(ctx)

// 应用限制到进程（需要 PID）
pid := 12345
if err := limiter.Apply(pid); err != nil {
    log.Printf("Failed to apply limits: %v", err)
}

// 程序退出时停止监控
defer limiter.Stop()
```

### 2. 集成到 Supervisor

资源限制器已集成到 `Supervisor` 中，通过配置自动启用：

```go
import (
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor"
)

// Supervisor 会自动创建和管理资源限制器
sup := supervisor.NewSupervisor(
    "my-plugin",
    "/path/to/plugin/binary",
    []string{"--arg1", "--arg2"},
    supervisor.WithResourceLimits(
        "512Mi",         // 内存限制
        1000,            // goroutine 限制
        "50",            // CPU 限制
        5*time.Second,   // 检查间隔
    ),
)

// 启动插件（自动启用资源监控）
if err := sup.Start(ctx); err != nil {
    return err
}

// 停止时自动清理资源监控
sup.Stop()
```

### 3. 动态配置示例

```go
// 从配置文件加载
type PluginConfig struct {
    Name          string        `yaml:"name"`
    MemoryLimit   string        `yaml:"memory_limit"`
    MaxGoroutines int           `yaml:"max_goroutines"`
    CPULimit      string        `yaml:"cpu_limit"`
    CheckInterval time.Duration `yaml:"check_interval"`
}

func createLimiter(cfg *PluginConfig) *supervisor.ResourceLimiter {
    return supervisor.NewResourceLimiter(
        cfg.Name,
        cfg.MemoryLimit,
        cfg.MaxGoroutines,
        cfg.CPULimit,
        cfg.CheckInterval,
    )
}
```

## 完整配置示例

### plugins.yaml

```yaml
plugins:
  - name: auth-plugin
    binary: /opt/plugins/auth/auth-plugin
    args: ["--config", "/etc/auth.yaml"]
    resources:
      memory_limit: "256Mi"
      max_goroutines: 500
      cpu_limit: "25"
      check_interval: 5s

  - name: rate-limiter
    binary: /opt/plugins/ratelimit/ratelimit
    args: []
    resources:
      memory_limit: "512MB"
      max_goroutines: 1000
      cpu_limit: "50"
      check_interval: 10s

  - name: logger
    binary: /opt/plugins/logger/logger
    args: ["--verbose"]
    resources:
      # 不限制内存和 CPU
      memory_limit: "0"
      max_goroutines: 100
      cpu_limit: "0"
      check_interval: 30s
```

## 监控行为

### Linux 平台（硬限制）

使用 cgroup v2 实现真正的资源隔离：

1. **内存超限**：进程被 OOM killer 终止
2. **CPU 超限**：进程被限流，不会被终止
3. **日志记录**：资源使用情况会记录到日志

```
INFO: plugin started: my-plugin (PID 12345)
INFO: applying cgroup limits: memory=512Mi, cpu=50%, cgroup=/sys/fs/cgroup/llm-gateway/my-plugin
WARN: plugin my-plugin approaching memory limit: 480Mi/512Mi (93.75%)
ERROR: plugin my-plugin terminated by OOM killer
```

### macOS / 其他平台（软限制）

仅监控和告警，不强制终止：

1. **内存超限**：记录警告日志，不终止进程
2. **Goroutine 超限**：记录警告日志，不终止进程
3. **CPU 限制**：不支持，配置被忽略

```
INFO: resource monitoring enabled for plugin my-plugin (OS: darwin, no hard limits)
WARN: plugin my-plugin memory usage: 520Mi (limit: 512Mi)
WARN: plugin my-plugin goroutine count: 1050 (limit: 1000)
```

## 最佳实践

### 1. 内存限制设置

根据插件类型设置合理的内存限制：

- **轻量级插件**（日志、指标）：128-256 MiB
- **中等插件**（认证、限流）：256-512 MiB
- **重量级插件**（缓存、数据处理）：512 MiB - 2 GiB

**经验法则**：
- 开发环境：设置为预期使用量的 2-3 倍
- 生产环境：设置为预期使用量的 1.5-2 倍
- 始终为内存峰值预留 20-30% 缓冲

### 2. Goroutine 限制设置

```go
// 根据插件并发特性设置
switch pluginType {
case "io-bound":
    maxGoroutines = 1000  // I/O 密集型可以多一些
case "cpu-bound":
    maxGoroutines = 100   // CPU 密集型控制并发
case "mixed":
    maxGoroutines = 500   // 混合型取中间值
}
```

### 3. 检查间隔设置

- **频繁检查**（1-5s）：适用于资源敏感的生产环境
- **常规检查**（5-10s）：适用于大多数场景
- **低频检查**（30s+）：适用于资源充足或开发环境

**权衡**：
- 检查间隔越短，越早发现问题，但监控开销越大
- 检查间隔越长，监控开销越小，但问题发现延迟

### 4. 分层限制策略

```yaml
# 不同环境使用不同限制
environments:
  development:
    memory_limit: "1Gi"      # 宽松限制
    max_goroutines: 2000
    check_interval: 30s
    
  staging:
    memory_limit: "512Mi"    # 接近生产
    max_goroutines: 1000
    check_interval: 10s
    
  production:
    memory_limit: "256Mi"    # 严格限制
    max_goroutines: 500
    check_interval: 5s
```

### 5. 避免的常见错误

❌ **过于严格的限制**
```yaml
# 会导致插件频繁 OOM
memory_limit: "64Mi"
max_goroutines: 10
```

❌ **忘记考虑峰值**
```yaml
# 平均 200Mi，峰值 400Mi -> 会 OOM
memory_limit: "256Mi"
```

❌ **检查间隔过短**
```yaml
# 1 秒检查一次，监控开销过大
check_interval: 1s
```

✅ **推荐配置**
```yaml
# 合理的生产配置
memory_limit: "512Mi"        # 平均 300Mi，峰值 450Mi
max_goroutines: 1000         # 平均 200，峰值 600
cpu_limit: "50"              # 不超过半个核心
check_interval: 5s           # 5 秒检查一次
```

## 监控与告警

### 日志级别

- **INFO**：正常启动和停止
- **WARN**：接近限制（90% 以上）
- **ERROR**：超出限制或强制终止

### 集成监控系统

```go
// 示例：导出 Prometheus 指标
import "github.com/prometheus/client_golang/prometheus"

var (
    pluginMemoryUsage = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "plugin_memory_bytes",
            Help: "Plugin memory usage in bytes",
        },
        []string{"plugin"},
    )
    
    pluginGoroutineCount = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "plugin_goroutines",
            Help: "Plugin goroutine count",
        },
        []string{"plugin"},
    )
)

// 在监控循环中更新指标
func (rl *ResourceLimiter) monitor() {
    stats := rl.getStats()
    pluginMemoryUsage.WithLabelValues(rl.pluginName).Set(float64(stats.Memory))
    pluginGoroutineCount.WithLabelValues(rl.pluginName).Set(float64(stats.Goroutines))
}
```

## 故障排查

### 问题：插件频繁 OOM

**症状**：
```
ERROR: plugin my-plugin terminated by OOM killer
INFO: plugin restarted: my-plugin (attempt 3/5)
```

**解决方案**：
1. 检查内存限制是否过小：`memory_limit` 应大于实际使用峰值
2. 排查内存泄漏：使用 `pprof` 分析插件内存分配
3. 增加内存限制或优化插件代码

### 问题：Goroutine 泄漏

**症状**：
```
WARN: plugin my-plugin goroutine count: 5000 (limit: 1000)
```

**解决方案**：
1. 使用 `pprof` 查看 goroutine 堆栈：
   ```bash
   curl http://localhost:6060/debug/pprof/goroutine?debug=2
   ```
2. 排查未关闭的 channel 或未取消的 context
3. 确保 goroutine 正常退出

### 问题：监控未启动

**症状**：
```
# 没有任何监控日志
```

**解决方案**：
1. 确认 `Start()` 已调用
2. 检查 context 是否已取消
3. 查看是否配置了限制（全为 0 时不启动监控）

## API 参考

### ResourceLimiter

```go
type ResourceLimiter struct {
    // 字段为内部实现，不可直接访问
}

// 创建新的资源限制器
func NewResourceLimiter(
    pluginName string,
    memoryLimit string,
    maxGoroutines int,
    cpuLimit string,
    checkInterval time.Duration,
) *ResourceLimiter

// 启动资源监控（非阻塞）
func (rl *ResourceLimiter) Start(ctx context.Context)

// 应用资源限制到进程
func (rl *ResourceLimiter) Apply(pid int) error

// 停止资源监控（阻塞直到监控循环退出）
func (rl *ResourceLimiter) Stop()

// 解析内存限制字符串
func ParseMemoryLimit(limit string) (int64, error)

// 解析 CPU 限制字符串
func ParseCPULimit(limit string) (int, error)
```

### 配置选项

| 参数 | 类型 | 说明 | 默认值 |
|-----|------|------|--------|
| `pluginName` | string | 插件名称，用于日志和 cgroup 路径 | **必填** |
| `memoryLimit` | string | 内存限制（如 "512Mi"） | 无限制 |
| `maxGoroutines` | int | 最大 goroutine 数量 | 无限制 |
| `cpuLimit` | string | CPU 限制百分比（如 "50"） | 无限制 |
| `checkInterval` | time.Duration | 监控检查间隔 | 5s |

## 版本兼容性

- **Go 版本**：1.27.4+
- **Linux 内核**：5.0+ (cgroup v2 支持)
- **macOS**：10.15+ (仅软限制)
- **其他平台**：仅软限制模式

## 相关文档

- [Supervisor 使用指南](../README.md)
- [Plugin Controller](../controller/README.md)
- [Plugin Config](../config/README.md)
- [Plugin IPC](../ipc/README.md)

## 许可证

Copyright © 2024 Kaixuan Technology. All rights reserved.

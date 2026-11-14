# Plugin Runtime Reconciler

插件状态协调器，实现 Reconciliation Loop 模式，自动检测和修复插件状态漂移。

## 功能特性

- ✅ **自动重启**：检测插件崩溃并自动重启
- ✅ **崩溃保护**：连续崩溃超过阈值自动禁用插件
- ✅ **版本升级**：检测版本不一致并自动升级
- ✅ **状态同步**：周期性协调期望状态与实际状态
- ✅ **崩溃追踪**：跟踪崩溃次数，支持时间窗口重置

## 架构设计

Reconciler 采用 Kubernetes Controller 的 Reconciliation Loop 模式：

```
┌─────────────┐         ┌──────────────┐
│   Registry  │────────▶│ Desired State│
│  (期望状态)  │         └──────────────┘
└─────────────┘                │
                               │ Compare
┌─────────────┐                │
│ Supervisor  │────────▶┌──────▼───────┐         ┌──────────┐
│ (实际状态)  │         │  Reconciler  │────────▶│  Actions │
└─────────────┘         └──────────────┘         └──────────┘
                               │
                               │ Execute
                               ▼
                        ┌──────────────┐
                        │   Supervisor │
                        └──────────────┘
```

## 使用示例

```go
import (
    "context"
    "time"

    "github.com/kaixuan/llm-gateway-go/plugin-runtime/reconciler"
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor"
)

// 创建配置
config := &reconciler.Config{
    Interval:         30 * time.Second,  // 协调间隔
    MaxCrashCount:    10,                 // 最大崩溃次数
    CrashResetWindow: 1 * time.Hour,     // 崩溃计数重置窗口
}

// 创建 Reconciler
r := reconciler.NewReconciler(config, supervisorInstance, registryInstance)

// 启动协调循环
ctx := context.Background()
if err := r.Start(ctx); err != nil {
    log.Fatalf("Failed to start reconciler: %v", err)
}

// 运行中...

// 停止协调循环
if err := r.Stop(); err != nil {
    log.Fatalf("Failed to stop reconciler: %v", err)
}
```

## 协调逻辑

### 1. 插件未运行 → 启动

如果插件在 Registry 中标记为启用，但 Supervisor 中未运行：

```
Action: START
Reason: Plugin not running but desired
```

### 2. 插件崩溃 → 重启

如果插件状态为 FAILED/CRASHED：

```
Action: RESTART
Reason: Plugin in failed state
```

同时记录崩溃次数到 CrashTracker。

### 3. 崩溃次数超限 → 禁用

如果崩溃次数 ≥ MaxCrashCount：

```
Action: DISABLE
Reason: Crash count exceeded: N
```

插件被标记为 DISABLED，不再自动重启。

### 4. 版本不一致 → 升级

如果 Registry 中的版本 ≠ Supervisor 中的版本：

```
Action: UPGRADE
Reason: Version mismatch: 1.0.0 -> 2.0.0
```

实际版本来自 Supervisor 已启动进程记录。支持 Upgrade 适配接口时交由运行时执行；否则停止旧版本 → 更新注册表 → 启动新版本。此流程会中断服务，不保证零停机。

### 5. 不应运行 → 停止

如果插件在 Supervisor 中运行，但 Registry 中未启用：

```
Action: STOP
Reason: Plugin running but not desired
```

## 状态机

插件生命周期状态转换：

```
PENDING ──start──▶ STARTING ──▶ RUNNING
                                   │
                                   │ crash
                                   ▼
                                 FAILED ──restart──▶ STARTING
                                   │
                                   │ max_crash
                                   ▼
                                DISABLED
```

支持的状态转换：

- `PENDING → STARTING` (start)
- `STARTING → RUNNING` (自动)
- `RUNNING → FAILED` (崩溃)
- `FAILED → STARTING` (restart)
- `FAILED → DISABLED` (disable)
- `RUNNING → STOPPING` (stop)
- `STOPPING → STOPPED` (自动)

## 崩溃追踪

CrashTracker 维护每个插件的崩溃历史：

- **时间窗口**：只统计窗口内的崩溃（默认 1 小时）
- **自动重置**：超过窗口后计数清零
- **并发安全**：使用 RWMutex 保护

```go
tracker := reconciler.NewCrashTracker(1 * time.Hour)

// 记录崩溃
tracker.RecordCrash("plugin1")

// 获取崩溃次数
count := tracker.GetCount("plugin1")

// 手动重置
tracker.Reset("plugin1")

// 清理过期记录
tracker.Cleanup()
```

## 接口定义

### SupervisorInterface

Reconciler 需要 Supervisor 实现以下接口：

```go
type SupervisorInterface interface {
    Start(ctx context.Context, manifest *discovery.Manifest) error
    Stop(ctx context.Context, pluginID string) error
    Restart(ctx context.Context, pluginID string) error
    StateOf(pluginID string) (*supervisor.ProcessState, error)
    ListAll() []*supervisor.ProcessState
}
```

### PluginRegistry

Reconciler 需要 Registry 实现以下接口：

```go
type PluginRegistry interface {
    List(ctx context.Context, filter RegistryFilter) ([]*RegistryPlugin, error)
    UpdateVersion(ctx context.Context, pluginID string, version string) error
    UpdateStatus(ctx context.Context, pluginID string, status PluginStatus) error
    GetManifest(ctx context.Context, pluginID string) (*discovery.Manifest, error)
}
```

## 测试

运行单元测试：

```bash
go test -v -cover
```

使用 `go test -race -count=1 -cover ./...` 获取当前覆盖率。协调动作失败会汇总返回，其他插件仍继续处理；循环启动失败也会返回调用方。

测试用例包括：
- 插件未运行自动启动
- 插件崩溃自动重启
- 版本不一致自动升级
- 连续崩溃自动禁用
- 崩溃计数时间窗口重置
- 状态转换验证
- 错误处理

## 性能考量

- **协调间隔**：默认 30 秒，建议不低于 10 秒
- **内存占用**：CrashTracker 仅保存活跃插件的崩溃记录
- **并发安全**：所有公开方法都是并发安全的
- **串行执行**：Reconcile 方法串行执行，避免状态竞争

## 依赖

```
github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery
github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor
```

## 参考资料

- [Kubernetes Controller Runtime](https://github.com/kubernetes-sigs/controller-runtime)
- [Reconciliation Pattern](https://kubernetes.io/docs/concepts/architecture/controller/)

## License

与主项目相同

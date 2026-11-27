# ResourceLimiter 生产接线方案

## 文档信息

- **创建日期**: 2026-11-27
- **版本**: v1.4
- **状态**: 阶段 1 已实施并通过审计（2026-11-27，见 §3.5/§3.6）；§5.3 监控埋点已落地（v1.3，见 §3.7）；Linux cgroup 硬限制 e2e 已在容器真机验证 PASS（v1.4，见 §3.8）；OOM-kill 断言待裸金属补验；§8.2 性能基线与 §5.1 灰度推进未开始
- **优先级**: P0
- **相关审计**: [插件化框架审计报告 2026-11-26](../../audit/llm-gateway-plugin-framework-audit-2026-11-26.html)

## 1. 背景与问题

### 1.1 当前状况

**问题描述**（来自审计报告 §2.1）：
> supervisor/resource_limiter.go 已实现内存/goroutine/CPU 限制，支持 Linux cgroup v2 和 macOS 软限制。但生产环境使用的是**根包** plugin-runtime/supervisor.go，**未集成**资源限制器。

**影响**：
- 插件进程无 cgroup 保护
- OOM 或 goroutine 泄漏无硬限制
- 恶意或故障插件可能耗尽系统资源

### 1.2 子模块实现现状

**已实现的功能**（plugin-runtime/supervisor/resource_limiter.go）：

| 功能 | Linux (cgroup v2) | macOS |
|------|-------------------|-------|
| 内存限制 | ✅ 硬限制 (memory.max) | ⚠️ 软限制（监控+警告） |
| CPU 限制 | ✅ 硬限制 (cpu.max) | ⚠️ 软限制（监控+警告） |
| PID 限制 | ✅ 硬限制 (pids.max) | ⚠️ 软限制（监控+警告） |
| 自动降级 | ✅ cgroup 不可用时降级到监控模式 | N/A |

**关键接口**：
```go
// NewResourceLimiter 创建资源限制器
func NewResourceLimiter(pluginName string, limits discovery.ResourceLimits) (*ResourceLimiter, error)

// Apply 在启动前应用限制（设置 cmd.SysProcAttr）
func (r *ResourceLimiter) Apply(cmd *exec.Cmd) error

// AssignProcess 在启动后分配进程到 cgroup
func (r *ResourceLimiter) AssignProcess(pid int) error

// Stop 停止监控（macOS）或清理 cgroup（Linux）
func (r *ResourceLimiter) Stop() error
```

### 1.3 根包与子模块差异

| 维度 | 根包 (plugin-runtime/supervisor.go) | 子模块 (plugin-runtime/supervisor/supervisor.go) |
|------|-------------------------------------|--------------------------------------------------|
| **使用场景** | 生产环境（cmd/gateway 装配） | 未使用（实验性实现） |
| **进程管理** | command 接口 + execCommand | exec.Cmd 直接使用 |
| **Manifest** | plugin-runtime/types.go (Manifest) | plugin-runtime/discovery (discovery.Manifest) |
| **ResourceLimiter** | ❌ 未集成 | ✅ 已集成 |
| **健康检查** | ❌ 未实现 | ✅ 已实现 |
| **自动重启** | ✅ Restart() 方法 | ✅ RestartPolicy |

---

## 2. 技术方案

### 2.1 方案选择

#### 方案 A：增量接线（推荐）

**描述**：在根包 Supervisor 中集成 ResourceLimiter，保持现有架构不变。

**优点**：
- ✅ 风险最小，不影响现有功能
- ✅ 可以逐步推进，先在开发环境验证
- ✅ 向后兼容，未配置 limits 的插件不受影响

**缺点**：
- ⚠️ 两套 Supervisor 继续并存
- ⚠️ 需要手动同步功能（如未来的健康检查）

#### 方案 B：归并重构（长期目标）

**描述**：将子模块 Supervisor 功能完全迁移到根包，统一实现。

**优点**：
- ✅ 彻底解决双 Supervisor 问题
- ✅ 子模块的健康检查、RestartPolicy 等功能可统一使用

**缺点**：
- ❌ 需要大规模重构，影响生产代码
- ❌ 需要更长的测试周期
- ❌ 风险较高

**决策**：
- **阶段 1**：采用方案 A，增量接线 ResourceLimiter
- **阶段 2**（长期）：规划方案 B，归并两套 Supervisor

---

## 3. 阶段 1：增量接线实施方案

### 3.1 Manifest 扩展

#### 添加 ResourceLimits 配置

在 `plugin-runtime/types.go` 的 `Manifest` 结构中添加：

```go
// Manifest 是插件与网关的线协议合同
type Manifest struct {
    // ... 现有字段 ...
    
    // ResourceLimits 定义插件进程的资源限制（可选）
    ResourceLimits *ResourceLimits `json:"resource_limits,omitempty"`
}

// ResourceLimits 定义资源限制配置
type ResourceLimits struct {
    // Memory 内存限制
    Memory *MemoryLimit `json:"memory,omitempty"`
    
    // CPU 限制
    CPU *CPULimit `json:"cpu,omitempty"`
    
    // MaxProcesses 最大进程/线程数（默认 10）
    MaxProcesses int `json:"max_processes,omitempty"`
}

// MemoryLimit 定义内存限制
type MemoryLimit struct {
    // Limit 内存上限，格式：100M, 1G, 512MB
    Limit string `json:"limit"`
    
    // Swap 是否允许使用 swap（Linux only，默认 false）
    Swap bool `json:"swap,omitempty"`
}

// CPULimit 定义 CPU 限制
type CPULimit struct {
    // Limit CPU 百分比限制，格式：25%, 50%, 100%
    Limit string `json:"limit"`
}
```

#### Manifest 示例

```json
{
  "schema_version": 2,
  "plugin_id": "my-plugin",
  "plugin_version": "1.0.0",
  "runtime": {
    "entrypoint": "my-plugin"
  },
  "resource_limits": {
    "memory": {
      "limit": "256M"
    },
    "cpu": {
      "limit": "50%"
    },
    "max_processes": 5
  }
}
```

### 3.2 移植 ResourceLimiter

#### 步骤 1：复制 ResourceLimiter 到根包

```bash
# 创建新目录
mkdir -p plugin-runtime/limiter

# 复制文件（作为独立模块）
cp plugin-runtime/supervisor/resource_limiter.go plugin-runtime/limiter/
```

#### 步骤 2：调整导入路径

将 `plugin-runtime/limiter/resource_limiter.go` 中的：
```go
"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
```

改为：
```go
pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
```

并将 `discovery.ResourceLimits` 改为 `pluginruntime.ResourceLimits`。

### 3.3 修改根包 Supervisor

#### 在 Supervisor 结构中添加 limiter 管理

```go
// Supervisor 管理所有已启动的插件进程。
type Supervisor struct {
    cfg            SupervisorConfig
    mu             sync.Mutex
    startMu        sync.Mutex
    procs          map[string]command
    states         map[string]*PluginState
    manifests      map[string]*Manifest
    limiters       map[string]*limiter.ResourceLimiter  // 新增
    commandFactory func(socketPath, entrypoint string, env []string) command
}
```

#### 修改 Start() 方法

在 `Start()` 方法中创建和应用 ResourceLimiter：

```go
func (s *Supervisor) Start(ctx context.Context, m *Manifest) (*PluginState, error) {
    // ... 现有的互斥检查、签名校验等 ...
    
    // 创建 ResourceLimiter（如果配置了 resource_limits）
    var rl *limiter.ResourceLimiter
    if m.ResourceLimits != nil {
        var err error
        rl, err = limiter.NewResourceLimiter(m.PluginID, *m.ResourceLimits)
        if err != nil {
            return nil, fmt.Errorf("create resource limiter: %w", err)
        }
    }
    
    // 构造环境变量
    socketPath := filepath.Join(s.cfg.SocketDir, m.PluginID+".sock")
    env := []string{
        "AI_SESSION_MANAGER_PLUGIN_SOCKET=" + socketPath,
        "GATEWAY_PLUGIN_CONTRACT=" + m.GatewayCompatibility.APIContract,
        "AI_SESSION_MANAGER_MANIFEST=" + m.ManifestPath,
    }
    if s.cfg.PluginEnv != nil {
        env = append(env, s.cfg.PluginEnv(m.PluginID)...)
    }
    
    cmd := s.commandFactory(socketPath, m.Runtime.Entrypoint, env)
    
    // 应用资源限制（在启动前）
    if rl != nil {
        // 需要访问底层 exec.Cmd
        if execCmd, ok := cmd.(*execCommand); ok {
            if err := rl.Apply(execCmd.cmd); err != nil {
                return nil, fmt.Errorf("apply resource limits: %w", err)
            }
        }
    }
    
    // 启动进程
    if err := cmd.Start(ctx); err != nil {
        return nil, fmt.Errorf("start plugin %s: %w", m.PluginID, err)
    }
    
    // 分配进程到 cgroup（在启动后）
    if rl != nil {
        if err := rl.AssignProcess(cmd.Pid()); err != nil {
            // 仅警告，不阻止启动
            fmt.Printf("WARN: failed to assign process to resource limits: %v\n", err)
        }
    }
    
    // 保存状态
    st := &PluginState{
        PluginID:      m.PluginID,
        PluginVersion: m.PluginVersion,
        Status:        "starting",
        SocketPath:    socketPath,
        Pid:           cmd.Pid(),
    }
    
    s.mu.Lock()
    s.procs[m.PluginID] = cmd
    s.states[m.PluginID] = st
    s.manifests[m.PluginID] = m
    if rl != nil {
        s.limiters[m.PluginID] = rl  // 保存 limiter
    }
    s.mu.Unlock()
    
    // ... 现有的事件发布 ...
    
    return st, nil
}
```

#### 修改 Stop() 方法

在 `Stop()` 方法中清理 ResourceLimiter：

```go
func (s *Supervisor) Stop(pluginID string) error {
    s.mu.Lock()
    cmd, ok := s.procs[pluginID]
    rl := s.limiters[pluginID]
    delete(s.procs, pluginID)
    delete(s.limiters, pluginID)
    if st := s.states[pluginID]; st != nil {
        st.Status = "stopped"
        st.SocketPath = ""
        st.Pid = 0
    }
    s.mu.Unlock()
    
    if !ok {
        return fmt.Errorf("plugin %s not running", pluginID)
    }
    
    // 停止 ResourceLimiter
    if rl != nil {
        if err := rl.Stop(); err != nil {
            fmt.Printf("WARN: failed to stop resource limiter: %v\n", err)
        }
    }
    
    // 发布事件
    s.publishEvent(context.Background(), "plugin.stopped", pluginID, map[string]interface{}{
        "plugin_id": pluginID,
        "status":    "stopped",
    })
    
    return cmd.Stop()
}
```

#### 暴露 execCommand.cmd

为了让 ResourceLimiter.Apply() 能访问底层 `exec.Cmd`，需要调整 `execCommand`：

```go
type execCommand struct {
    cmd           *exec.Cmd  // 改为导出（或添加 Cmd() 方法）
    socketPath    string
    contextSecret []byte
}

// 添加 getter 方法
func (e *execCommand) Cmd() *exec.Cmd {
    return e.cmd
}
```

然后在 Start() 中：
```go
if execCmd, ok := cmd.(*execCommand); ok {
    if err := rl.Apply(execCmd.Cmd()); err != nil {
        return nil, fmt.Errorf("apply resource limits: %w", err)
    }
}
```

### 3.4 配置默认限制

在 Gateway 配置中添加默认资源限制：

```yaml
# gateway.yaml
plugins:
  directory: /opt/gateway/plugins
  enabled: [plugin-a, plugin-b]
  
  # 全局默认资源限制
  default_resource_limits:
    memory:
      limit: "512M"
    cpu:
      limit: "50%"
    max_processes: 10
```

在 Supervisor 初始化时，如果 Manifest 未指定 resource_limits，使用默认值：

```go
func (s *Supervisor) Start(ctx context.Context, m *Manifest) (*PluginState, error) {
    // 合并默认限制
    limits := m.ResourceLimits
    if limits == nil && s.cfg.DefaultLimits != nil {
        limits = s.cfg.DefaultLimits
    }
    
    // 创建 ResourceLimiter
    var rl *limiter.ResourceLimiter
    if limits != nil {
        var err error
        rl, err = limiter.NewResourceLimiter(m.PluginID, *limits)
        if err != nil {
            return nil, fmt.Errorf("create resource limiter: %w", err)
        }
    }
    
    // ... 继续现有逻辑 ...
}
```

---

### 3.5 落地记录（2026-11-27，阶段 1 完成）

规划 §3 全部实施并验证通过。与原规划的差异点（实施时的更优解，均已在代码注释中说明）：

| 规划原文 | 实际落地 | 原因 |
|---------|---------|------|
| §3.2 复制 `resource_limiter.go` 到 `plugin-runtime/limiter/`，导入改为 `pluginruntime.ResourceLimits` | limiter 包**自持**线协议类型（`limiter.ResourceLimits/MemoryLimit/CPULimit`），根包 `types.go` 用**类型别名**反向引用（`type ResourceLimits = limiter.ResourceLimits`） | 反向依赖会造成包循环（根包 supervisor.go 要导入 limiter）；别名保持 Manifest JSON 合同单一来源，无双份结构漂移 |
| §3.3 暴露 `execCommand.cmd`（`Cmd()` getter）供 `Apply` 访问 | `execCommand` 新增 **prepare 钩子**：`Start()` 内部 `exec.CommandContext` 创建之后、`c.Start()` 之前调用 `prepare(cmd)`，Supervisor 把 `rl.Apply` 挂进去 | `exec.Cmd` 是 Start 内部用 `CommandContext(ctx)` 创建的（保留 ctx 取消语义），启动前根本不存在，`Cmd()` getter 拿到的是 nil |
| §3.4 默认限制经 `SupervisorConfig.DefaultLimits` 传入 | **两条通道都保留**：① `SupervisorConfig.DefaultLimits`（编程式，main.go 直连 Supervisor 的路径用）② controller `scan()` 在 manifest 校验后**合并默认值进 Manifest**（controller 管理的插件路径用） | scan 是所有启动路径的必经点 ⇒ 默认值合并/总开关天然覆盖热重载 |
| §6.3 总开关 | `plugins.resource_limits_enabled`（缺省 true）在 config validator 校验块、controller `defaultLimitsOf()` 两处生效；false 时 scan 不合并任何限制 | 紧急关闭通道：重载配置即生效，无需重启进程 |
| （规划未提及）| limiter 新增 `memory.swap.max` 写入（`MemoryLimit.Swap`，默认禁 swap）与单字母内存单位（`128M`/`1G`，manifest 示例用的格式） | 子模块原实现只支持 `Mi/MB` 双字母单位，与 §3.1 manifest 示例 `"256M"` 不符 |

**交付物**：

- `plugin-runtime/limiter/`（新包）：`types.go`（线协议类型）+ `resource_limiter.go`（移植实现）+ `resource_limiter_test.go`（7 项：创建/空配置/内存解析/CPU 解析/monitor Apply/Stop 幂等/Linux cgroup 条件测试）
- `plugin-runtime/types.go`：`Manifest.ResourceLimits *ResourceLimits`（omitempty 向后兼容）+ 3 个类型别名
- `plugin-runtime/supervisor.go`：`Supervisor.limiters` map、`SupervisorConfig.DefaultLimits`、Start 创建/prepare 注入/AssignProcess、Stop/Restart/Upgrade 三路径清理、`LimiterOf()` 观测访问器、`execCommand.prepare` + `Cmd()`
- `plugin-runtime/supervisor_test.go`：5 项集成测试（声明限制/未声明兼容/默认兜底/Restart-Upgrade 生命周期/真实 exec 全链路）
- `plugin-runtime/config/`：`PluginsConfig.ResourceLimitsEnabled` + `DefaultResourceLimits`（ResourceLimitsConfig 块）、validator 加载期格式校验、`gateway.yaml.example` 注释示例、4 项测试
- `plugin-runtime/controller/manifests.go`：`scan()` 默认合并 + `defaultLimitsOf()` 转换、5 项测试

**提交**（main 分支）：`3b7f6be54`（Supervisor 接线 + limiter 包）、`6a0a434e7`（config + controller 装配）。

**验证**：`go build ./...` 全仓通过；`go vet` plugin-runtime 全树干净；limiter 7/7、根包全量（含新增 5 项）、config 4 项新增、controller 全量（含新增 5 项）全部 PASS；`cmd/gateway` 构建通过；vendor 已刷新。

**遗留到验收阶段**（§8）：Linux cgroup 硬限制与 OOM 行为需 Linux 环境实测（本机 macOS 仅监控模式）；§8.2 性能指标与 §5.1 灰度推进未开始。

### 3.6 审计与修复记录（2026-11-27，阶段 1 交付后审计）

对阶段 1 交付做全链路审计，发现并修复 2 个问题：

| 级别 | 问题 | 修复 |
|------|------|------|
| P0 | `controller.snapshot()` 丢失 `scan()` 合并的默认限制：内存态 Manifest 带 `resource_limits` 而磁盘 bundle 没有，`sameManifest` JSON 比对误判「manifest 在快照期间被改变」⇒ **配置默认限制后全部插件拒启**。首日 controller 测试只覆盖了 `scan()` 单点，未覆盖 scan→snapshot→start 全链路 | `snapshot()` 比对前剥离内存版默认限制（磁盘版 manifest 自带声明仍参与比对，bundle 完整性不放松）；frozen 回填 `m.ResourceLimits` 让 sup.Start 照常建 limiter。新增端到端回归测试 `TestControllerStartsWithDefaultResourceLimits`（真实进程 ready + LimiterOf 断言）与 `TestSnapshotKeepsManifestDeclaredLimitsInComparison` |
| P1 | `limiter.AssignProcess` 未防御 pid≤0：fake command（`Pid()=0`）会进 monitor 轮询空转 | 入口拒绝 pid≤0（`TestAssignProcessRejectsInvalidPid`） |

审计确认无问题的点：`Manifest.ResourceLimits` 在严格 schema（v3 DisallowUnknownFields）下合法解析；`cloneManifestMap` 浅拷贝 + scan 每轮新建 ResourceLimits 使指针共享无变异风险；Upgrade/Restart 回滚路径的 limiter 清理时序正确。

### 3.7 监控指标埋点落地（2026-11-27，§5.3）

阶段 1 交付后限制「是否生效、是否够用」无观察面（monitor 超限只有 fmt WARN 进日志流；cgroup 模式写完 memory.max 就不读回）。本轮补上：

| 层 | 落地 |
|----|------|
| 指标定义（`metrics/plugin_resource_metrics.go`） | 7 个指标：`llmgw_plugin_memory_usage_bytes` / `llmgw_plugin_memory_limit_bytes` / `llmgw_plugin_cpu_usage_percent` / `llmgw_plugin_threads` / `llmgw_plugin_limiter_mode_info{mode}` / `llmgw_plugin_limit_violations_total{kind}` / `llmgw_plugin_oom_killed_total`。label 仅 `plugin_id`（部署期枚举，有界）+ `mode`/`kind` 闭集；经 GW-00 门禁（`TestNoHighCardinalityLabels` 自动覆盖） |
| limiter 采样读回（`resource_limiter.go`） | ① cgroup 模式新增 `monitorCgroup` 采样循环（5s，与 monitor 同周期）：`memory.current`/`pids.current`/`cpu.stat usage_usec` 差分 → `lastStats`；`memory.events oom_kill` 差分 → OOM 回调（首次读回记基线防历史 OOM 误计；计数器归零重置基线）；软限检测（usage > 90% memory.max，与 `memory.high` 一致）→ violation 回调。② monitor 模式既有超限 WARN 点同步触发 violation 回调。③ `SetObservabilityHooks(onViolation, onOOMKill)` 回调注入——limiter 不直接依赖 metrics 包，保持可独立测试；nil 回调行为与历史版本一致 |
| Supervisor 接线（`supervisor.go`） | `Start` 创建 limiter 后注入 `metrics.RecordPluginLimitViolation` / `RecordPluginOOMKill` 回调；新增 `States()` 枚举面（collector 遍历用）与 limiter `Limits()`/`ParseMemoryLimit()` 导出面 |
| 导出器（`cmd/gateway/plugin_metrics_collector.go`） | `pluginMetricsCollector` 周期（15s）遍历 `Supervisor.States()` × `LimiterOf()`，把 mode/usage/limit 快照写进 gauge；接入点与 healthLoop 并列（`main.go`，插件启用时启动、退出时停止）；usage gauge 无 stats 时不写（absent 语义，不造假 0）；坏 limit 配置不砸抓取（fail-fast 属 `Apply` 职责） |
| 测试 | metrics 3 项（注册形状/归一化/OOM 增量）；limiter 3 项（cgroup 文件解析两种形态/OOM 基线语义/软限触发，-race 通过）；supervisor 3 项（观测面/States 生命周期/回调注入）；collector 3 项（真实 limiter 导出/坏 limit 宽容/Stop 幂等） |

Linux cgroup 端到端验收测试（§4.2/§8.1）同轮落地：`plugin-runtime/controller/e2e_cgroup_linux_test.go`（Linux only，macOS/无 cgroup 写权限自动 Skip）——`TestE2ECgroupHardLimits` 全链路（controller 扫描→真实进程→memory.max/memory.high/swap.max/cpu.max/pids.max 四文件值核对→cgroup.procs 归组→memory.events 可读→停止清理）+ `TestE2ECgroupOOMKill`（8MB 硬限吃 64MB → 内核 oom_kill ≥ 1）。Shell 包装 `scripts/test-resource-limits.sh`（§4.3 规划形态，非 Linux 主机降级为单元/集成测试 + 提示）。

### 3.8 Linux 真机 e2e 执行记录与三处修复（2026-11-27，§8.1）

Linux 环境（Docker Desktop VM 内 linux/arm64 + cgroup v2 容器，`--cgroupns=host` 挂载 `/sys/fs/cgroup`）首次实跑 e2e，发现并修复 3 个**只在真机才暴露**的缺陷：

| # | 问题 | 修复 | 证据 |
|---|------|------|------|
| 1 | **subtree controllers 未启用**：limiter 只 `MkdirAll` 子组，父目录 `cgroup.subtree_control` 从未写 `+memory +cpu +pids`——cgroup v2 控制器自上而下委派，子组创建后**没有 memory.max/cpu.max/pids.max 控制文件**，所有 writeCgroup 全部 ENOENT | `NewResourceLimiter` 创建子组前先在 `llm-gateway-plugins/` 父目录写 `cgroup.subtree_control`（幂等；失败仅 WARN 降级） | 容器内首跑：目录创建成功但 memory.max 打不开（permission denied→ENOENT 家族）；修复后 `subtree_control = cpu memory pids`，四文件写入全部成功 |
| 2 | **Supervisor.Stop 顺序颠倒**：`rl.Stop()`（cgroup rmdir）在 `cmd.Stop()`（杀进程）**之前**执行——rmdir 拿着还挂着进程的 cgroup 必 EBUSY，重试窗口内进程根本没被通知退出 | 调整为先 `cmd.Stop()`（进程退出→组内 procs 清空）再 `rl.Stop()`；进程停止失败不阻断 limiter 清理 | 修复前：15×200ms 重试全 EBUSY，WARN `device or resource busy`；修复后清理一次成功 |
| 3 | **`os.RemoveAll` 清不掉 cgroup 目录**：cgroupfs 控制文件是内核虚拟文件**不可 unlink**，RemoveAll 逐文件删除被 EPERM 打断，永远走不到目录本身的 rmdir | `removeCgroup()` 改用 `syscall.Rmdir` + EBUSY/ENOENT/EPERM 分类重试（~3s，等进程组内线程退出） | 手动 rmdir 空目录成功、RemoveAll 残留，坐实差异 |

**验收结论**（§8.1 功能验收，硬限制部分）：

- ✅ `TestE2ECgroupHardLimits` 完整 PASS（多次运行收敛后）：controller 扫描→真实插件进程→**memory.max=134217728（128M）**、**memory.high=120795136（90% 软限，内核 page 4K 对齐）**、**memory.swap.max=0（默认禁 swap）**、**cpu.max=25000 100000（25%）**、**pids.max=3**、cgroup.procs 归组、memory.events 可读、停止后 cgroup 清理
- ⚠️ `TestE2ECgroupOOMKill`：**限制执行通道已证实**（8MB 限制下 64MB 分配被内核拦停，`memory.events max` 事件 300+ 次、memory.current 被压回限额），但 `oom_kill ≥ 1` 的最终断言在 Docker Desktop 虚拟化层无法触发——该层委派的 memory 控制器让进程在 ENOMEM 上自行退出而非内核 OOM kill。**裸金属 Linux 主机补验 §8.1 OOM 行为仍是开放项**（`sudo go test ./plugin-runtime/controller/ -run TestE2ECgroup -v`）
- 附注：测试内 helper 进程拉起（`go test -test.run` 自 exec 模式）在 Rosetta 转译下偶发 readiness 超时（模拟层开销，非产品代码问题）——重试收敛即可；裸金属无此噪音

**测试增量**：e2e 两项（Linux only）；`scripts/test-resource-limits.sh` 在容器内实跑通过（单元/集成 + 硬限制 e2e + 指标冒烟全绿）。

---

## 4. 测试方案

### 4.1 单元测试

#### 测试 ResourceLimiter 创建

```go
// plugin-runtime/limiter/resource_limiter_test.go
func TestNewResourceLimiter(t *testing.T) {
    limits := pluginruntime.ResourceLimits{
        Memory: &pluginruntime.MemoryLimit{Limit: "256M"},
        CPU:    &pluginruntime.CPULimit{Limit: "50%"},
        MaxProcesses: 5,
    }
    
    rl, err := NewResourceLimiter("test-plugin", limits)
    if err != nil {
        t.Fatalf("NewResourceLimiter failed: %v", err)
    }
    
    if rl == nil {
        t.Fatal("ResourceLimiter is nil")
    }
    
    // 验证模式（Linux 应该是 cgroup，macOS 应该是 monitor）
    if runtime.GOOS == "linux" {
        if rl.mode != ModeCgroup && rl.mode != ModeMonitor {
            t.Errorf("expected cgroup or monitor mode on Linux, got %s", rl.mode)
        }
    } else if runtime.GOOS == "darwin" {
        if rl.mode != ModeMonitor {
            t.Errorf("expected monitor mode on macOS, got %s", rl.mode)
        }
    }
}
```

#### 测试 Supervisor 集成

```go
// plugin-runtime/supervisor_test.go
func TestSupervisorWithResourceLimits(t *testing.T) {
    cfg := SupervisorConfig{
        SocketDir: t.TempDir(),
    }
    s := NewSupervisor(cfg)
    
    manifest := &Manifest{
        PluginID:      "test-plugin",
        PluginVersion: "1.0.0",
        Runtime: Runtime{
            Entrypoint: "test-binary",
        },
        ResourceLimits: &ResourceLimits{
            Memory: &MemoryLimit{Limit: "128M"},
            CPU:    &CPULimit{Limit: "25%"},
            MaxProcesses: 3,
        },
    }
    
    // 注入测试 command
    s.commandFactory = func(socketPath, entrypoint string, env []string) command {
        return &fakeCommand{pid: 12345}
    }
    
    state, err := s.Start(context.Background(), manifest)
    if err != nil {
        t.Fatalf("Start failed: %v", err)
    }
    
    if state.Pid != 12345 {
        t.Errorf("expected PID 12345, got %d", state.Pid)
    }
    
    // 验证 limiter 已创建
    s.mu.Lock()
    rl := s.limiters["test-plugin"]
    s.mu.Unlock()
    
    if rl == nil {
        t.Fatal("ResourceLimiter not created")
    }
    
    // 停止插件
    if err := s.Stop("test-plugin"); err != nil {
        t.Errorf("Stop failed: %v", err)
    }
}
```

### 4.2 集成测试

#### 测试 cgroup 限制（Linux only）

```go
func TestCgroupLimits(t *testing.T) {
    if runtime.GOOS != "linux" {
        t.Skip("cgroup tests require Linux")
    }
    
    // 检查 cgroup v2 是否可用
    if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
        t.Skip("cgroupv2 not available")
    }
    
    limits := pluginruntime.ResourceLimits{
        Memory: &pluginruntime.MemoryLimit{Limit: "50M"},
    }
    
    rl, err := NewResourceLimiter("test-cgroup", limits)
    if err != nil {
        t.Fatalf("NewResourceLimiter failed: %v", err)
    }
    defer rl.Stop()
    
    // 启动一个测试进程
    cmd := exec.Command("sleep", "60")
    if err := rl.Apply(cmd); err != nil {
        t.Fatalf("Apply failed: %v", err)
    }
    
    if err := cmd.Start(); err != nil {
        t.Fatalf("Start failed: %v", err)
    }
    defer cmd.Process.Kill()
    
    if err := rl.AssignProcess(cmd.Process.Pid); err != nil {
        t.Fatalf("AssignProcess failed: %v", err)
    }
    
    // 验证进程在 cgroup 中
    cgroupPath := filepath.Join("/sys/fs/cgroup", "llm-gateway-plugins", "test-cgroup")
    procsData, err := os.ReadFile(filepath.Join(cgroupPath, "cgroup.procs"))
    if err != nil {
        t.Fatalf("failed to read cgroup.procs: %v", err)
    }
    
    pids := strings.TrimSpace(string(procsData))
    if !strings.Contains(pids, fmt.Sprintf("%d", cmd.Process.Pid)) {
        t.Errorf("process %d not in cgroup, procs: %s", cmd.Process.Pid, pids)
    }
}
```

#### 测试内存超限（需要特殊测试环境）

```go
func TestMemoryLimit(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping memory limit test in short mode")
    }
    
    // 创建一个会分配大量内存的测试程序
    // 验证是否被 cgroup OOM killer 终止
}
```

### 4.3 端到端测试

#### 测试插件启动和资源监控

```bash
#!/bin/bash
# test-resource-limits.sh

# 1. 构建测试插件
go build -o /tmp/test-plugin ./cmd/test-plugin

# 2. 创建 manifest with resource_limits
cat > /tmp/test-plugin-manifest.json <<EOF
{
  "schema_version": 2,
  "plugin_id": "test-plugin",
  "plugin_version": "1.0.0",
  "runtime": {
    "entrypoint": "test-plugin"
  },
  "resource_limits": {
    "memory": {"limit": "128M"},
    "cpu": {"limit": "25%"},
    "max_processes": 3
  }
}
EOF

# 3. 启动 Gateway
LLM_GATEWAY_PLUGIN_RUNTIME_CONFIG=/tmp/gateway.yaml go run ./cmd/gateway &
GATEWAY_PID=$!

sleep 5

# 4. 检查 cgroup 是否创建（Linux only）
if [ "$(uname)" = "Linux" ]; then
    if [ -d "/sys/fs/cgroup/llm-gateway-plugins/test-plugin" ]; then
        echo "✓ cgroup created"
        
        # 检查内存限制
        memory_max=$(cat /sys/fs/cgroup/llm-gateway-plugins/test-plugin/memory.max)
        expected=$((128 * 1024 * 1024))
        if [ "$memory_max" -eq "$expected" ]; then
            echo "✓ memory limit set correctly: $memory_max bytes"
        else
            echo "✗ memory limit incorrect: got $memory_max, expected $expected"
        fi
    else
        echo "✗ cgroup not created"
    fi
fi

# 5. 清理
kill $GATEWAY_PID
```

---

## 5. 部署方案

### 5.1 阶段性推进

#### Phase 1: 开发环境验证（1-2 周）

1. 在开发环境部署带 ResourceLimiter 的 Gateway
2. 配置测试插件使用资源限制
3. 验证功能正常，无性能退化
4. 收集监控数据

#### Phase 2: 灰度发布（1-2 周）

1. 在 1-2 个测试环境启用 ResourceLimiter
2. 监控插件资源使用情况
3. 调整默认限制配置
4. 验证异常情况处理（OOM、CPU 超限等）

#### Phase 3: 生产推广（2-4 周）

1. 在生产环境逐步启用（先 10%，再 50%，最后 100%）
2. 持续监控资源使用和异常
3. 根据实际情况调整限制
4. 形成最佳实践文档

### 5.2 配置建议

#### 默认资源限制（保守配置）

```yaml
plugins:
  default_resource_limits:
    memory:
      limit: "512M"  # 保守：大多数插件足够
    cpu:
      limit: "50%"   # 保守：避免单个插件独占 CPU
    max_processes: 10  # 保守：防止进程泄漏
```

#### 插件特定配置

在各插件的 manifest 中可以覆盖默认值：

```json
{
  "plugin_id": "heavy-plugin",
  "resource_limits": {
    "memory": {"limit": "1G"},
    "cpu": {"limit": "100%"},
    "max_processes": 20
  }
}
```

### 5.3 监控指标

需要监控的关键指标（✅ = v1.3 已埋点，见 §3.7；命名按仓库 GW-00 规范带 `llmgw_` 前缀）：

| 指标 | 说明 | 告警阈值 | 状态 |
|------|------|---------|------|
| `llmgw_plugin_memory_usage_bytes` | 插件内存使用（cgroup memory.current / monitor RSS） | > 90% limit | ✅ |
| `llmgw_plugin_memory_limit_bytes` | 声明的内存硬限制（memory.max 写入值） | —（对照用） | ✅ |
| `llmgw_plugin_cpu_usage_percent` | 插件 CPU 使用（cgroup usage_usec 差分） | > 90% limit | ✅ |
| `llmgw_plugin_threads` | 插件线程/任务数（pids.current） | > 90% limit | ✅ |
| `llmgw_plugin_oom_killed_total` | OOM 终止次数（memory.events oom_kill 差分） | > 0 | ✅ |
| `llmgw_plugin_limit_violations_total{kind}` | 资源限制违规次数（monitor/cgroup 软限检测） | > 10/hour | ✅ |
| `llmgw_plugin_limiter_mode_info{mode}` | 生效限制模式（cgroup/monitor/disabled） | monitor 占比升高 = cgroup 降级信号 | ✅（超规划） |

---

## 6. 风险与缓解措施

### 6.1 技术风险

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|---------|
| **cgroup 创建失败** | 插件无资源限制 | 中 | 自动降级到监控模式，记录警告日志 |
| **性能开销** | Gateway 启动变慢 | 低 | 异步创建 cgroup，不阻塞主流程 |
| **旧插件兼容性** | 未配置 limits 的插件无法启动 | 低 | 向后兼容，未配置时不创建 limiter |
| **OOM killer 误杀** | 插件正常使用时被终止 | 中 | 设置保守的默认限制，允许插件自定义 |

### 6.2 运维风险

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|---------|
| **资源限制过小** | 插件频繁 OOM | 中 | 灰度期间收集数据，调整限制 |
| **监控数据不准确** | 无法判断限制是否合理 | 低 | 使用生产级监控工具（Prometheus） |
| **回滚困难** | 新版本有问题但无法回滚 | 低 | 通过配置开关控制，不修改代码 |

### 6.3 配置开关

在 Gateway 配置中添加总开关：

```yaml
plugins:
  resource_limits_enabled: true  # 全局开关，默认 true
  default_resource_limits:
    # ... 限制配置 ...
```

如果发现问题，可以快速关闭：

```yaml
plugins:
  resource_limits_enabled: false  # 紧急关闭
```

---

## 7. 长期规划

### 7.1 Phase 2: Supervisor 归并（3-6 个月）

在 ResourceLimiter 稳定运行后，启动根包与子模块 Supervisor 归并：

1. **需求分析**（2 周）
   - 梳理两套 Supervisor 的所有功能
   - 确定归并后的统一接口
   - 评估影响范围

2. **设计方案**（2 周）
   - 设计统一的 Supervisor 架构
   - 确定迁移路径（增量迁移 vs 一次性替换）
   - 制定测试计划

3. **实施迁移**（4-8 周）
   - 增量迁移各功能模块
   - 完善测试覆盖
   - 生产验证

4. **清理代码**（2 周）
   - 删除子模块 Supervisor
   - 更新文档

### 7.2 功能增强

**优先级 P1**：
- [ ] 支持动态调整资源限制（无需重启插件）
- [ ] 添加资源使用趋势分析
- [ ] 实现自动扩缩容（根据负载自动调整限制）

**优先级 P2**：
- [ ] 支持 IO 限制（磁盘、网络）
- [ ] 支持 GPU 资源限制
- [ ] 实现插件资源配额管理

---

## 8. 验收标准

### 8.1 功能验收

- [ ] ResourceLimiter 成功集成到根包 Supervisor
- [ ] Linux 环境下 cgroup 限制生效
- [ ] macOS 环境下监控模式正常工作
- [ ] 未配置 limits 的插件正常启动（向后兼容）
- [ ] 配置了 limits 的插件按预期受限
- [ ] 插件停止时 cgroup 正确清理

### 8.2 性能验收

- [ ] Gateway 启动时间增加 < 100ms
- [ ] 插件启动时间增加 < 50ms
- [ ] ResourceLimiter 内存开销 < 10MB per plugin
- [ ] 监控模式 CPU 开销 < 1%

### 8.3 稳定性验收

- [ ] 灰度期间无严重故障
- [ ] OOM 次数 < 1% of plugin starts
- [ ] 资源限制违规次数在可接受范围内
- [ ] 所有单元测试通过
- [ ] 所有集成测试通过

---

## 9. 参考文档

### 9.1 相关代码

- `plugin-runtime/supervisor.go` - 根包 Supervisor（生产）
- `plugin-runtime/supervisor/supervisor.go` - 子模块 Supervisor
- `plugin-runtime/supervisor/resource_limiter.go` - ResourceLimiter 实现
- `plugin-runtime/types.go` - Manifest 定义

### 9.2 相关文档

- [三套 Capability 词表设计说明](./capability-vocabularies-design.md)
- [插件开发指南](./plugin-development-guide.md)
- [插件化框架审计报告](../../audit/llm-gateway-plugin-framework-audit-2026-11-26.html)

### 9.3 外部参考

- [Linux cgroup v2 Documentation](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
- [Go syscall Package](https://pkg.go.dev/syscall)
- [Kubernetes Resource Management](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)

---

## 10. 更新日志

| 版本 | 日期 | 作者 | 变更 |
|------|------|------|------|
| v1.0 | 2026-11-27 | 插件化框架审计工作组 | 初始版本 |
| v1.1 | 2026-11-27 | 插件化框架审计工作组 | 阶段 1 实施完成（main: 3b7f6be54 + 6a0a434e7）：limiter 包/Manifest 扩展/Supervisor 四路径接线/config 默认限制 + 总开关/controller 装配；新增测试 21 项全绿；§3.5 落地记录 |
| v1.2 | 2026-11-27 | 插件化框架审计工作组 | 交付后审计：修复 P0（snapshot 丢默认限制致插件拒启）与 P1（AssignProcess pid≤0 空转）；新增 3 项回归测试；§3.6 审计记录 |
| v1.3 | 2026-11-27 | 插件化框架审计工作组 | §5.3 监控埋点落地：7 个 `llmgw_plugin_*` 指标（metrics/plugin_resource_metrics.go）、limiter cgroup 采样读回 + OOM/violation 回调、cmd/gateway 导出器（15s）、12 项新测试全绿（含 -race）；§4.2/§4.3 Linux e2e（TestE2ECgroupHardLimits/OOMKill + scripts/test-resource-limits.sh）；§3.7 落地记录 |
| v1.4 | 2026-11-27 | 插件化框架审计工作组 | Linux 真机 e2e 首轮执行（Docker Desktop VM cgroup v2）：发现并修复 3 个真机缺陷（subtree_control 未启用 / Stop 顺序颠倒致 rmdir EBUSY / RemoveAll 清不掉 cgroupfs）；硬限制验收完整 PASS；OOM-kill 断言受限虚拟化层待裸金属补验；§3.8 执行记录 |

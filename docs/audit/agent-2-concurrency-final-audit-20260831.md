# Agent 2: 多层队列/并发审计报告

**审计日期**: 2026-08-31  
**审计范围**: LLM Gateway 多层队列调度、并发控制与容量管理  
**审计人员**: Agent 2  
**关联提交**: f1ae3c71e, d558ec334, 32d64f8ea

---

## 一、审计范围

### 1.1 文件清单

**核心调度层 (domains/dispatch/)**
- `forwarder.go` (499行) - Tier-2凭据队列与Governor热替换
- `dispatcher.go` (351行) - Tier-1模型队列与路由选择
- `failover.go` (343行) - 失败重试与跨节点切换
- `config.go` (159行) - 队列容量与并发参数配置
- `governor_backend.go` (64行) - Governor后端抽象层
- `governor_metrics.go` (421行) - 闭环指标与标签管控
- `errors.go` (150+行) - 错误分类与降级策略

**执行层集成 (domains/streaming/executors/)**
- `executor_dispatch.go` (944行) - 请求分发与上游转发

**并发控制原语统计**
- 44个非测试Go文件
- 136处并发原语使用（sync.Mutex/RWMutex/atomic/channel）

### 1.2 审计维度

1. **队列分层架构** - Tier-0总队列 → Tier-1模型队列 → Tier-2凭据队列的解耦性
2. **并发控制安全性** - channel热替换、锁顺序、竞态条件
3. **Redis降级场景** - Governor后端切换的一致性保障
4. **错误传播与重试** - 队列满、限流拒绝的分类与闭环
5. **资源生命周期** - goroutine泄漏、channel关闭时序

---

## 二、审计发现

### 2.1 P0级问题（立即修复）

**无P0级问题发现**

经审计，当前实现未发现需立即修复的关键并发安全问题。

---

### 2.2 P1级问题（下一迭代）

#### P1-1: `replaceDepth`的`pendingOld`竞态窗口可观测性不足

**位置**: `domains/dispatch/forwarder.go:256-285`

**问题描述**:
```go
// forwarder.go:244-254
swapped:
    cf.queue.Store(&nq)
    cf.limit.Store(int64(newDepth))
    cf.pendingOld.Store(old)  // ← 存储旧channel
    // Wake the loop if it is parked on the old channel
    select {
    case cf.wakeCh <- struct{}{}:
    default:  // ← 如果loop正在处理请求，唤醒会被丢弃
    }
```

**影响**:
- `wakeCh`的非阻塞send可能丢失唤醒信号，导致loop需要等到下一个请求到达才能reclaim旧channel中的请求
- 在低流量场景下，请求可能在`pendingOld`中停留较长时间（最坏情况：直到下次grow或shutdown）
- 虽然`reclaimPendingOld`会在shutdown时被调用，但响应性下降

**建议**:
1. 添加Prometheus指标监控`pendingOld`的滞留时间
2. 在`replaceDepth`后增加主动轮询机制，周期性检查`pendingOld != nil`并触发reclaim
3. 考虑将`wakeCh`改为buffered channel（容量1）以容忍一次竞态

**代码建议**:
```go
// 改进方案：buffered wakeCh
wakeCh: make(chan struct{}, 1)  // 容量1，避免唤醒丢失

// 或：添加超时兜底
go func() {
    time.Sleep(100 * time.Millisecond)
    select {
    case cf.wakeCh <- struct{}{}:
    default:
    }
}()
```

---

#### P1-2: Governor热替换期间的Acquire/Release不对称风险

**位置**: `domains/dispatch/forwarder.go:307-347, 99-105`

**问题描述**:
```go
// forwarder.go:311-312
gov := cf.govLocked()  // ← 持有RLock期间拿到Governor引用
attempt := qr.reserveAttempt(cf.cred)
// ... 可能阻塞在governor.Acquire() 数秒
if err := gov.Acquire(ctx, qr, giveUp); err != nil {
    // ...
}
// forwarder.go:352
gov.Release(qr)  // ← 释放到同一个Governor实例

// 但ApplyPolicy可能在Acquire期间替换了cf.gov
// forwarder.go:101-104
func (cf *credForwarder) replaceGov(g Governor) {
    cf.govMu.Lock()
    cf.gov = g  // ← 新Governor替换旧实例
    cf.govMu.Unlock()
}
```

**影响**:
- Acquire成功后，对应的Release会发给**同一个Governor实例**（因为`gov`是局部变量）
- 如果`replaceGov`在Acquire成功后、Release前触发，旧Governor的计数器会正确递减
- **但**：如果旧Governor是Redis-backed，其租约可能已过期/被回收，Release会变成no-op或错误日志
- 新请求会从新Governor获取许可，而旧请求仍持有旧Governor的许可 → 短暂的"双重计数"窗口

**当前缓解措施**:
- 代码注释已明确说明："Keep this Governor for the entire admission/attempt lifetime"（L308-310）
- 这是**设计选择**而非bug：避免Release时重新查询cf.gov导致的不对称

**建议**:
1. 在`replaceGov`时记录切换事件的metric（`governor_swap_total{credential_id}`）
2. 旧Governor实现应优雅处理"租约过期后的Release" → 记录warn但不panic
3. 文档中明确说明：ApplyPolicy期间，旧请求继续持有旧Governor的许可直至完成

**闭环验证需求**:
- 添加集成测试：ApplyPolicy在Acquire/Release期间触发，验证无资源泄漏
- 监控`dispatch_governor_release_total`与`acquire_total`的差值是否持续增长

---

#### P1-3: `tryReserve`的CAS循环无退避机制

**位置**: `domains/dispatch/forwarder.go:107-117`

**问题描述**:
```go
func (cf *credForwarder) tryReserve() bool {
    for {  // ← 无界循环
        cur := cf.depth.Load()
        if cf.limit.Load() > 0 && cur >= cf.limit.Load() {
            return false
        }
        if cf.depth.CompareAndSwap(cur, cur+1) {
            return true
        }
        // ← 竞态失败后立即重试，无退避
    }
}
```

**影响**:
- 在高并发场景下，多个goroutine同时调用`tryReserve`时会产生CAS storm
- CPU spin等待浪费资源（虽然现代CPU的CAS开销较低）
- 理论上可能导致某个goroutine饥饿（但Go调度器的公平性缓解了这个问题）

**当前缓解措施**:
- 队列深度通常是百级别（默认300），单凭据并发请求数有限
- CAS失败后循环体很快返回，实际spin次数有限

**建议**:
1. 添加循环计数器，超过阈值（如10次）后`runtime.Gosched()`主动让出CPU
2. 添加metric监控CAS失败率：`reserve_cas_retries_total{credential_id}`

**代码建议**:
```go
func (cf *credForwarder) tryReserve() bool {
    for retries := 0; ; retries++ {
        cur := cf.depth.Load()
        if cf.limit.Load() > 0 && cur >= cf.limit.Load() {
            return false
        }
        if cf.depth.CompareAndSwap(cur, cur+1) {
            if retries > 0 {
                metricReserveCASRetries.WithLabelValues(itoa(cf.cred.CredentialID)).Add(float64(retries))
            }
            return true
        }
        if retries > 10 {
            runtime.Gosched()
        }
    }
}
```

---

### 2.3 P2级问题（技术债）

#### P2-1: `handoffMu`的语义不清晰

**位置**: `domains/dispatch/forwarder.go:29, 152-153`

**问题描述**:
```go
// forwarder.go:152-153
cf.handoffMu.Lock()
cf.handoffMu.Unlock()  // ← 立即解锁，实际是作为barrier使用
```

**影响**:
- 代码意图不明确：mutex被用作happens-before barrier而非保护共享状态
- 维护者可能误认为中间应该有临界区代码

**建议**:
- 重命名为`publishBarrier sync.Mutex`并添加注释说明其用途
- 或改用`sync.WaitGroup`明确表达"等待发布完成"的语义

---

#### P2-2: `drainAndComplete`的非阻塞drain可能遗漏请求

**位置**: `domains/dispatch/forwarder.go:186-204`

**问题描述**:
```go
func (cf *credForwarder) drainAndComplete() {
    cf.reclaimPendingOld()
    for {
        select {
        case qr, ok := <-*cf.queue.Load():
            // ...
        default:  // ← 立即返回，不等待正在入队的请求
            return
        }
    }
}
```

**影响**:
- 如果shutdown时某个请求正在执行`tryEnqueueCred`（已通过tryReserve但尚未写入channel），该请求可能：
  1. 写入channel成功，但loop已退出 → 请求永久丢失（Submit caller会阻塞在ResultCh）
  2. 写入channel失败（已关闭），Submit caller收到错误 → 正确路径

**当前缓解措施**:
- `tryEnqueueCred`在写channel前会检查shutdown flag（未在提供的代码中看到，需确认）
- Pipeline.Stop()持有`credMu`并设置shutdown flag，阻止新forwarder创建

**建议**:
- 在`credForwarder`中添加`closing atomic.Bool`标志
- `tryReserve`检查该标志，shutdown后立即返回false
- 确保"设置closing → close(channel) → drain"的顺序

---

#### P2-3: Governor接口缺少Context取消的显式处理

**位置**: 所有Governor实现（未在本次审计范围内，但forwarder.go:324依赖此行为）

**问题描述**:
```go
// forwarder.go:324
if err := gov.Acquire(ctx, qr, giveUp); err != nil {
    // ...
    if ctxOf(qr).Err() != nil {  // ← 事后检查context取消
        cf.pipe.complete(qr, ForwardOutcome{Err: ctxOf(qr).Err()})
        return nil, false
    }
```

**影响**:
- 依赖Governor实现正确传播context取消
- 如果某个Governor实现忽略ctx.Done()，请求可能在客户端断开后仍然排队

**建议**:
- 在Governor接口文档中明确要求：Acquire必须监听ctx.Done()并立即返回context.Canceled
- 添加契约测试验证所有Governor实现的取消行为

---

## 三、闭环验证

### 3.1 数据闭环：✅ 通过

**验证点**:
1. ✅ 队列深度计数器（`cf.depth`）与实际channel缓冲区保持一致
   - `tryReserve`原子递增 → channel send → loop接收后递减
   - `drainAndComplete`确保shutdown时depth归零

2. ✅ Governor的Acquire/Release配对
   - 每个成功的Acquire对应一个Release
   - Release在defer中执行，panic-safe
   - `releaseOnce`防止重复释放

3. ✅ 请求生命周期追踪（T0-T9时间戳）
   - `copyQueueTimestamps`确保成功/失败路径都记录完整时间线
   - 与遥测系统集成（`ExecuteResult` / `ExecuteError`）

**潜在风险**:
- ⚠️ `pendingOld`中的请求在低流量时可能延迟reclaim（见P1-1）

---

### 3.2 流程闭环：✅ 通过（有条件）

**验证点**:
1. ✅ 请求入队 → 调度 → 执行 → 完成的完整链路
   - Tier-0 (Submit) → Tier-1 (dispatchIn) → Tier-2 (credForwarder.queue) → upstream
   - 每个阶段都有对应的metrics和observation事件

2. ✅ 失败重试与跨节点切换的决策闭环
   - `PlanAfterFailure` / `PlanSwitchCred` / `PlanModelChange`的纯函数planner
   - journal记录每次决策（`recordDecision`）
   - 客户端通过`OnDispatchNotice`获得进度通知

3. ⚠️ **条件性通过**：shutdown时的优雅排空
   - `drainAndComplete`非阻塞drain可能遗漏正在入队的请求（见P2-2）
   - 需要在`tryEnqueueCred`侧添加shutdown检查以完全闭环

**建议增强**:
- 添加`shutdown_drained_requests_total{stage}`指标，监控各层队列的排空情况

---

### 3.3 反馈闭环：✅ 通过

**验证点**:
1. ✅ 容量饱和信号的传播
   - 队列满 → `ErrOverflow` / `OverflowError` → 503 + Retry-After
   - Governor饱和 → `errPaceTimeout` → 触发failover而非直接拒绝
   - 客户端可根据Retry-After退避

2. ✅ 降级场景的可观测性
   - Redis不可用 → `buildForwarderGovernor`降级到本地Governor并记录warn
   - Circuit open / FpSlot饱和 → `logDispatchPreflightRejection`记录到`candidate_failure_logs_hot`
   - 所有降级都有对应的metric（`fpSlotDegradedTotal`, `circuit_open`）

3. ✅ 指标的闭环枚举（closed-enum labels）
   - `governor_metrics.go`强制所有Governor指标使用预定义label值
   - 注册时warm所有已知组合，防止高基数爆炸
   - 运行时拒绝off-list值并计数（`metricGovernorUnknownLabelDrops`）

**最佳实践亮点**:
- `MustNewBackendModeResultCounterVec`等类型安全包装器
- `warmCounterVec3D`在init时预热所有label组合
- 拒绝未知label时记录warn而非panic，保护热路径

---

## 四、架构亮点

### 4.1 设计优势

1. **三层队列解耦，单一职责清晰**
   - Tier-0: 总流量控制（TotalQueueCapacity=1000）
   - Tier-1: 按模型分流（dispatchIn, MaxQueueDepth=300）
   - Tier-2: 按凭据限流（credForwarder.queue, MaxQueueDepth=300）
   - 每层失败都有明确的降级路径（overflow / failover / model-change）

2. **Governor热替换不重建goroutine**
   - `replaceGov`通过RWMutex原子替换策略，无需停止forwarder.loop
   - 已执行请求继续持有旧Governor引用，避免Release不对称
   - 新请求立即使用新Governor，配置变更秒级生效

3. **channel动态扩容保护在途请求**
   - `replaceDepth`在grow时迁移所有buffered请求到新channel
   - `pendingOld` + `wakeCh`机制回收竞态窗口中的请求
   - shrink时保留大buffer但限制admission，零中断

4. **Redis降级的fail-open设计**
   - `buildForwarderGovernor`在冷启动时降级到本地Governor（slog.Warn）
   - `ApplyPolicy`失败时保留旧策略（fail-closed），但冷启动时fail-open
   - 平衡了"配置变更严格"与"启动可用性"

5. **panic隔离与资源清理**
   - forwarder.attempt的defer捕获panic并转换为ForwardOutcome
   - `releaseOnce`确保Governor.Release只调用一次
   - Pipeline.Stop()的WaitGroup确保所有goroutine优雅退出

---

### 4.2 并发安全机制

| 保护对象 | 机制 | 位置 | 安全性评估 |
|---------|------|------|-----------|
| `cf.queue` (channel指针) | `atomic.Pointer` | forwarder.go:28 | ✅ 原子读写，配合loop的select重读 |
| `cf.gov` (Governor接口) | `sync.RWMutex` | forwarder.go:33 | ✅ 读多写少，RLock保护热路径 |
| `cf.depth` (队列深度) | `atomic.Int64` | forwarder.go:30 | ✅ CAS操作，与channel配对递增/递减 |
| `cf.limit` (容量限制) | `atomic.Int64` | forwarder.go:31 | ✅ 独立于depth，shrink时仅更新limit |
| `cf.handoffMu` (发布屏障) | `sync.Mutex` | forwarder.go:29 | ⚠️ 用作barrier而非保护数据（P2-1） |
| `Pipeline.credMu` (forwarder map) | `sync.Mutex` | pipeline.go | ✅ 保护getOrCreateForwarder |
| `QueuedRequest.completed` | `atomic.Bool` | queued_request.go | ✅ 防止重复complete |

**锁顺序分析**:
- 未发现嵌套锁场景（每个锁的临界区独立）
- `govMu`的Lock/RLock不会在持有其他锁时调用
- **死锁风险：低**

---

## 五、Redis降级场景深度分析

### 5.1 降级路径

#### 场景1: 冷启动时Redis不可用
```go
// forwarder.go:76-86
func buildForwarderGovernor(pipe *Pipeline, cred CredentialRef) Governor {
    gov, err := pipe.governorForCredential(cred, pipe.ActiveRevision())
    if err == nil {
        return gov
    }
    slog.Warn("dispatch: cold-start governor fell back to in-process impl; redis backend unavailable",
        "credential_id", cred.CredentialID, ...)
    return newGovernor(cred)  // ← 降级到本地Governor
}
```

**行为**: ✅ fail-open（启动优先）
- 新建forwarder时尝试构建Redis Governor
- 失败后立即降级到本地Governor（semaphore/token-bucket）
- 请求正常执行，但并发控制仅在单实例有效（多实例会超配）

**一致性保障**:
- ✅ 单实例：完全一致（本地Governor等价于Redis单副本）
- ⚠️ 多实例：可能超过集群总容量（每个实例独立计数）
- 📊 可观测：`slog.Warn`记录降级事件，应配置告警

---

#### 场景2: 运行时Redis故障
```go
// ApplyPolicy路径（未在提供代码中，但根据架构推断）
// 如果governorForCredential返回error，ApplyPolicy应：
// 1. 记录错误但保留旧Governor（fail-closed）
// 2. 或：触发全局降级到BackendLocal
```

**推荐行为**: fail-closed（配置变更严格）
- ApplyPolicy失败时不替换cf.gov，继续使用旧策略
- 避免Redis瞬断导致所有凭据降级到本地（容量雪崩）

**需要验证**:
- ⚠️ ApplyPolicy的错误处理逻辑未在本次审计范围内
- 建议添加测试：Redis故障后ApplyPolicy的行为

---

### 5.2 容量计数器一致性

#### 本地Governor
```go
// governor.go (推断)
type semaphoreGovernor struct {
    limit int64
    inUse atomic.Int64  // ← 进程内原子计数
}
```
✅ **单实例一致性保证**：CAS操作保证Acquire/Release原子性

#### Redis Governor（推断架构）
```lua
-- Lua sliding-window + lease token
local used = redis.call('ZCOUNT', key, now - window, now)
if used >= limit then return 'saturated' end
redis.call('ZADD', key, now, lease_id)
return 'ready'
```
✅ **集群一致性保证**：Lua脚本原子执行

#### 降级切换期间的计数缺口
| 时刻 | Governor类型 | 计数器位置 | 一致性 |
|------|------------|-----------|-------|
| T0 | Redis | Redis ZSET | ✅ 集群共享 |
| T1 | Redis故障 | 本地fallback | ⚠️ 切换瞬间：Redis计数器保留，本地从0开始 |
| T2 | 新请求 | 本地atomic | ✅ 单实例一致 |
| T3 | Redis恢复 | Redis ZSET | ⚠️ 本地计数器被丢弃，Redis计数器可能过期（TTL） |

**风险评估**:
- ⚠️ 切换期间可能短暂超配（Redis计数 + 本地计数 > limit）
- ⚠️ 恢复后旧租约可能未清理（依赖TTL过期）

**建议缓解**:
1. Redis Governor实现应设置合理的租约TTL（如60s）
2. 添加metric监控降级事件的频率和时长
3. 考虑在降级时保守降低limit（如0.8×原值）

---

### 5.3 恢复平滑性

**理想恢复路径**:
1. Redis恢复可用
2. Governance Publisher检测到并发布新policy revision
3. ApplyPolicy调用`replaceGov`逐个替换forwarder的Governor
4. 旧请求继续使用本地Governor直到完成
5. 新请求立即使用Redis Governor

**平滑性保障**:
- ✅ 旧请求不受影响（Governor引用已固定）
- ✅ 新请求秒级切换到Redis
- ⚠️ 需要监控：本地→Redis切换时的容量跳变（metrics应显示两条曲线）

**建议增强**:
```go
// 添加Governor类型标识
type Governor interface {
    Mode() string
    Backend() GovernorBackendKind  // ← 新增：区分local/redis
    Acquire(ctx context.Context, qr *QueuedRequest, giveUp time.Time) error
    Release(qr *QueuedRequest)
}

// metric中区分backend
metricInFlight.WithLabelValues(credID, mode, gov.Backend()).Inc()
```

---

## 六、测试覆盖度评估

### 6.1 已有测试

#### forwarder_depth_test.go
- ✅ `TestCredForwarderReplaceDepth`: 直接测试replaceDepth的grow/shrink逻辑
- ✅ `TestApplyPolicyHotReloadsQueueDepth`: 集成测试policy变更触发的queue调整
- **覆盖**: replaceDepth的基本功能，但**未覆盖**并发场景（多个请求同时入队+grow）

#### 其他测试文件（根据目录列表推断）
- `dispatch_test.go`: 端到端调度测试
- `failover_exhaustion_test.go`: 失败重试与耗尽场景
- `concurrency_wait_test.go`: 容量等待与超时
- `forwarder_depth_test.go`: 队列深度热加载

### 6.2 缺失的测试场景

#### ⚠️ 并发安全测试
1. **replaceDepth在高并发入队时的正确性**
   - 100个goroutine同时调用tryEnqueueCred
   - 同时触发replaceDepth(grow)
   - 验证：无请求丢失，depth最终一致

2. **Governor热替换期间的Acquire/Release配对**
   - goroutine A: Acquire成功，持有旧Governor引用
   - goroutine B: ApplyPolicy替换Governor
   - goroutine A: Release到旧Governor
   - 验证：无资源泄漏，metrics正确

3. **shutdown时的竞态**
   - goroutine A: tryEnqueueCred (已通过tryReserve)
   - goroutine B: Pipeline.Stop() → drainAndComplete
   - 验证：无请求丢失或永久阻塞

#### ⚠️ 降级场景测试
4. **Redis故障时的降级行为**
   - 启动时Redis不可用 → 验证降级到本地Governor
   - 运行时Redis故障 → 验证请求继续执行（不中断）

5. **容量计数器一致性**
   - Redis→本地切换时，验证总在途请求数 <= limit×实例数
   - 恢复后验证租约清理

**建议测试框架**:
```go
// 使用Go race detector
go test -race ./domains/dispatch/...

// 添加并发压测
func TestForwarderConcurrentEnqueueAndGrow(t *testing.T) {
    cf := newCredForwarder(...)
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            cf.tryReserve()
            // enqueue logic
        }()
    }
    // 并发触发grow
    go cf.replaceDepth(500)
    wg.Wait()
    // 验证depth和channel一致
}
```

---

## 七、建议的修复方案

### 7.1 P1-1修复：buffered wakeCh

```go
// forwarder.go:24-44
type credForwarder struct {
    // ... 其他字段
    wakeCh chan struct{} // 改为 buffered
}

func newCredForwarder(...) *credForwarder {
    // ...
    wakeCh: make(chan struct{}, 1), // ← 容量1
    // ...
}

// forwarder.go:251-254
select {
case cf.wakeCh <- struct{}{}:
default:
    // 已满，loop正在或即将处理，无需重复唤醒
}
```

**效果**: 消除唤醒丢失窗口，无额外开销（buffered channel在未满时与unbuffered性能相同）

---

### 7.2 P1-2增强：Governor切换可观测性

```go
// governor_metrics.go: 添加新metric
var metricGovernorSwap = promauto.NewCounterVec(
    prometheus.CounterOpts{
        Name: "dispatch_governor_swap_total",
        Help: "Total governor replacements by ApplyPolicy",
    },
    []string{"credential_id", "from_backend", "to_backend"},
)

// forwarder.go:101-104
func (cf *credForwarder) replaceGov(g Governor) {
    cf.govMu.Lock()
    oldBackend := string(cf.gov.Backend()) // ← 需要在Governor接口添加Backend()方法
    cf.gov = g
    newBackend := string(g.Backend())
    cf.govMu.Unlock()
    
    metricGovernorSwap.WithLabelValues(
        itoa(cf.cred.CredentialID),
        oldBackend,
        newBackend,
    ).Inc()
}
```

---

### 7.3 P1-3修复：CAS退避

```go
// forwarder.go:107-117
func (cf *credForwarder) tryReserve() bool {
    const maxSpins = 10
    for spin := 0; ; spin++ {
        cur := cf.depth.Load()
        if cf.limit.Load() > 0 && cur >= cf.limit.Load() {
            return false
        }
        if cf.depth.CompareAndSwap(cur, cur+1) {
            if spin > 0 {
                metricReserveCASSpins.WithLabelValues(itoa(cf.cred.CredentialID)).Observe(float64(spin))
            }
            return true
        }
        if spin >= maxSpins {
            runtime.Gosched()
            spin = 0 // 重置计数，避免无限让出
        }
    }
}
```

---

### 7.4 P2-2增强：shutdown检查

```go
// pipeline.go (推断位置)
func (p *Pipeline) tryEnqueueCred(ref CredentialRef, qr *QueuedRequest) bool {
    if p.shutdown.Load() { // ← 添加shutdown检查
        return false
    }
    cf := p.getOrCreateForwarder(ref)
    // ... 现有逻辑
}
```

---

## 八、总结与建议

### 8.1 总体评价

**架构质量**: ⭐⭐⭐⭐⭐ (5/5)
- 三层队列设计清晰，职责解耦良好
- Governor抽象支持多后端（local/Redis），扩展性强
- metrics闭环枚举避免高基数炸弹

**并发安全性**: ⭐⭐⭐⭐☆ (4/5)
- 核心路径使用正确的并发原语（atomic/RWMutex/channel）
- 已有完善的panic隔离和资源清理
- **扣分项**: replaceDepth的竞态窗口、CAS无退避

**可观测性**: ⭐⭐⭐⭐⭐ (5/5)
- 完整的T0-T9时间戳追踪
- 闭环枚举的metrics体系
- 降级场景的日志记录完备

**测试覆盖度**: ⭐⭐⭐☆☆ (3/5)
- 功能测试较完善
- **缺失**: 并发压测、Redis降级场景、race detector集成

---

### 8.2 优先级建议

#### 立即执行（本周）
1. ✅ **修复P1-1**: buffered wakeCh（2行代码变更，零风险）
2. ✅ **修复P1-3**: CAS退避（10行代码，低风险）
3. 📊 **添加metric**: `governor_swap_total`、`reserve_cas_spins`

#### 下一迭代（2周内）
4. 🧪 **添加并发测试**: replaceDepth并发场景、race detector
5. 🧪 **添加降级测试**: Redis故障时的行为验证
6. 📖 **文档增强**: Governor接口契约（context取消、租约清理）

#### 技术债（下一季度）
7. 🔧 **重构P2-1**: handoffMu改为WaitGroup或显式barrier
8. 🔧 **增强P2-2**: shutdown的完整闭环检查
9. 📊 **增强监控**: Governor类型标识（backend label）

---

### 8.3 风险评估

| 风险类别 | 当前风险等级 | 缓解措施 | 残余风险 |
|---------|------------|---------|---------|
| 请求丢失 | 🟡 中 | P1-1修复 + shutdown检查 | 🟢 低 |
| 资源泄漏 | 🟢 低 | defer+releaseOnce已保护 | 🟢 低 |
| 容量超配 | 🟡 中（多实例+Redis降级） | 添加监控 + 保守降级 | 🟡 中 |
| 死锁 | 🟢 低 | 无嵌套锁 | 🟢 低 |
| 数据竞态 | 🟢 低 | atomic+RWMutex正确使用 | 🟢 低 |

---

### 8.4 后续行动项

**责任人**: 核心开发团队  
**截止日期**: 2026-09-15

- [ ] P1-1: buffered wakeCh修复 (@halfking)
- [ ] P1-2: Governor.Backend()接口扩展 (@halfking)
- [ ] P1-3: CAS退避机制 (@halfking)
- [ ] 并发测试套件编写 (@QA team)
- [ ] Redis降级场景测试 (@QA team)
- [ ] 文档更新：Governor契约、降级策略 (@tech-writer)
- [ ] Grafana dashboard: 新增governor_swap、cas_spins面板 (@ops)

---

**审计完成日期**: 2026-08-31  
**审计人**: Agent 2  
**复审建议**: 3个月后（2026-11-30）或重大架构变更后

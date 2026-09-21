# 多层队列/并发/错误处理审计报告

**审计时间**: 2026-08-31  
**审计范围**: 多层队列架构、并发控制、负载均衡和错误处理机制  
**审计人员**: AI Agent (Automated Code Audit)

---

## 执行摘要

本次审计对 llm-gateway-go 的多层队列调度架构（V2 dispatch pipeline）进行了全面评估，重点关注并发安全、队列容量管理、错误处理和资源泄漏风险。**总体评价：架构设计合理，并发控制严谨，但存在若干需要修复的竞态条件和资源泄漏风险。**

### 关键发现
- ✅ **优点**: 三层队列架构清晰（Total → Model → Credential），锁粒度合理
- ⚠️ **中危**: `credForwarder.replaceDepth` 的 channel 切换存在竞态窗口
- ⚠️ **中危**: Redis backend 降级路径缺少容量上限保护
- ⚠️ **低危**: 部分 goroutine 泄漏风险（已有 WaitGroup 保护，但边界场景未完全覆盖）
- ✅ **优点**: 错误分类体系完善（errorsx 包），覆盖 20+ 错误类型

---

## 1. 并发安全问题清单

### 1.1 【中危】credForwarder.replaceDepth 的 channel 切换竞态

**位置**: `domains/dispatch/forwarder.go:221-255`

**问题描述**:
```go
// replaceDepth 中的 channel 切换逻辑
swapped:
    cf.queue.Store(&nq)          // ← [1] 原子存储新 channel
    cf.limit.Store(int64(newDepth))
    cf.pendingOld.Store(old)     // ← [2] 存储旧 channel
    select {
    case cf.wakeCh <- struct{}{}: // ← [3] 唤醒 loop
    default:
    }
```

**竞态窗口**:
1. **生产者竞态**: 在 [1] 和 [2] 之间，外部 goroutine 可能向 `old` channel 发送请求，但此时 `pendingOld` 尚未设置，导致该请求不会被 `reclaimPendingOld` 回收。
2. **消费者竞态**: loop goroutine 在 select 阻塞时，如果 [3] 的 wakeCh 发送失败（default 分支），loop 可能长时间停留在旧 channel 上。

**影响**:
- 请求可能永久卡在旧 channel 中，直到下次 grow 或 shutdown 才被发现
- 概率较低（需要精确时序），但一旦触发会导致请求超时

**建议修复**:
```go
// 修复方案：在 Store 之前先设置 pendingOld，确保任何竞态请求都能被回收
func (cf *credForwarder) replaceDepth(newDepth int) {
    if newDepth <= 0 {
        newDepth = 1
    }
    if newDepth <= cap(*cf.queue.Load()) {
        cf.limit.Store(int64(newDepth))
        return
    }
    old := cf.queue.Load()
    nq := make(chan *QueuedRequest, newDepth)
    
    // 1. 先设置 pendingOld（在切换 queue 之前）
    cf.pendingOld.Store(old)
    
    // 2. 移动已缓冲的请求
    for {
        select {
        case qr := <-*old:
            nq <- qr
        default:
            goto swapped
        }
    }
swapped:
    // 3. 原子切换 queue（此时 pendingOld 已设置，任何发往旧 channel 的请求都能被回收）
    cf.queue.Store(&nq)
    cf.limit.Store(int64(newDepth))
    
    // 4. 保证唤醒（改用 blocking send + timeout，或 loop 定期检查）
    select {
    case cf.wakeCh <- struct{}{}:
    case <-time.After(10 * time.Millisecond):
        // wakeCh 已满，loop 会在下次迭代自然重读 queue
    }
}
```

---

### 1.2 【中危】Redis Backend 降级路径的容量保护缺失

**位置**: `domains/dispatch/queue_backend_redis.go:276-310`

**问题描述**:
```go
// admit 方法在 Redis 失败时的 fail-open 逻辑
if lastErr != nil {
    b.markDegraded("admit", lastErr)
    return Admission{}, true  // ← 直接返回 true，无集群容量检查
}
```

当 Redis 不可用时，backend 降级到本地模式并返回空 `Admission{}`（表示"无需 Release"），但 **失去了集群容量上限**：
- Tier-0 总队列容量（`TotalQueueCapacity=1000`）变成 **每个实例 1000**
- 4 个实例降级后，集群总容量从 1000 暴涨到 4000
- 可能导致 Redis 恢复时突发性容量超限

**影响**:
- Redis 故障期间，集群总容量失控（N 倍于配置值）
- 上游供应商可能因突发并发超限而触发熔断
- 恢复时的瞬时流量尖峰（所有实例的积压同时释放）

**建议修复**:
```go
// 方案 1: 降级时使用保守的本地容量（总容量 / 预估实例数）
func (b *redisQueueBackend) admit(ctx context.Context, kind LaneKind, id, slot string, cap int) (Admission, bool) {
    if cap <= 0 {
        return Admission{}, true
    }
    
    // 降级模式：使用 cap / estimatedInstances 作为本地上限
    if b.degraded.Load() {
        localCap := cap / 4  // 假设 4 实例；应从配置读取
        if localCap < 10 {
            localCap = 10  // 最小保护
        }
        localCount := b.heldTotal.Load()  // 本地计数器
        if localCount >= int64(localCap) {
            return Admission{}, false  // 本地容量已满
        }
    }
    
    // 正常 Redis 路径...
}

// 方案 2: 引入 circuit breaker，降级超过阈值时拒绝新请求
func (b *redisQueueBackend) admit(...) (Admission, bool) {
    if b.degraded.Load() && b.degradedDuration() > 5*time.Minute {
        // 长时间降级 → 进入保护模式，拒绝部分流量
        return Admission{}, false
    }
    // ...
}
```

---

### 1.3 【低危】Governor 热替换的短暂不一致性

**位置**: `domains/dispatch/forwarder.go:99-105`

**问题描述**:
```go
// replaceGov 原子替换 Governor
func (cf *credForwarder) replaceGov(g Governor) {
    cf.govMu.Lock()
    cf.gov = g
    cf.govMu.Unlock()
}

// acquire 读取 Governor
func (cf *credForwarder) acquire(qr *QueuedRequest) (Governor, bool) {
    gov := cf.govLocked()  // ← 获取当前 Governor
    // ... 可能等待数秒（RPM bucket 补充） ...
    if err := gov.Acquire(ctx, qr, giveUp); err != nil {
        // ...
    }
    // ... 最终 Release 时使用同一个 gov 实例 ...
}
```

在 `gov.Acquire()` 和 `gov.Release()` 之间，`ApplyPolicy` 可能替换了 `cf.gov`，但因为 `acquire` 持有旧 Governor 的引用，Release 会正确调用旧实例，**这是符合设计的**（comment 已明确说明）。

**非问题**: 代码已正确处理（每个 attempt 持有自己的 Governor 引用），此项标记为"低危"仅作记录，无需修复。

---

### 1.4 【低危】observationSink 的 panic 恢复可能吞噬关键错误

**位置**: `domains/dispatch/pipeline.go:283-288`

**问题描述**:
```go
func (p *Pipeline) observeQueue(observation QueueObservation) {
    // ...
    if sink != nil {
        func() {
            defer func() { _ = recover() }()  // ← 吞噬所有 panic
            sink.ObserveQueue(observation)
        }()
    }
}
```

虽然保护了主流程不被 sink panic 中断，但**完全吞噬 panic 会掩盖 sink 实现的严重 bug**（如死锁、nil 指针），建议至少记录日志：

```go
defer func() {
    if r := recover(); r != nil {
        slog.Error("dispatch: observation sink panic recovered",
            "panic", r, "observation", observation.Kind)
    }
}()
```

---

## 2. 队列容量与背压评估

### 2.1 三层队列容量配置

| 层级 | 容量配置 | 默认值 | 背压机制 | 评估 |
|------|---------|--------|---------|------|
| **Tier-0 总队列** | `TotalQueueCapacity` | 1000 | Submit 返回 `ErrOverflow` | ✅ 合理 |
| **Tier-1 模型队列** | `MaxQueueDepth` | 300/模型 | 立即拒绝，返回 overflow | ✅ 合理 |
| **Tier-2 凭据队列** | `MaxQueueDepth` | 300/凭据 | 立即拒绝，触发 failover | ✅ 合理 |
| **Registry** | `RegistryCapacity` | 1000 | FIFO 驱逐已完成请求 | ✅ 仅观测，不阻塞 |

**容量分层合理性**:
- ✅ **总容量 1000 > 单模型 300**: 避免单模型耗尽全局容量
- ✅ **模型队列 = 凭据队列**: 防止单凭据过度缓冲
- ⚠️ **潜在问题**: 100 个活跃模型 × 300 = 30,000 理论容量，远超 Tier-0 的 1000。实际场景中流量不会均匀分布，但**极端情况下可能形成"容量倒挂"**（Tier-1 总和 > Tier-0）。

**建议**:
```go
// 添加配置验证（在 LoadConfig 或 Pipeline 构造时）
func validateDispatchConfig(cfg Config) error {
    // 警告：如果单模型容量过大，可能超过总容量的合理比例
    if cfg.MaxQueueDepth > cfg.TotalQueueCapacity / 2 {
        slog.Warn("dispatch config: MaxQueueDepth is large relative to TotalQueueCapacity",
            "max_queue_depth", cfg.MaxQueueDepth,
            "total_capacity", cfg.TotalQueueCapacity,
            "recommendation", "ensure total capacity can accommodate multiple model lanes")
    }
    return nil
}
```

---

### 2.2 背压传播路径

```
客户端请求
    ↓
[Submit] → Tier-0 总队列满？→ 是 → ErrOverflow (503) → 客户端
    ↓ 否
[Dispatcher] → Tier-1 模型队列满？→ 是 → ErrOverflow (503) → 客户端
    ↓ 否
[Forwarder] → Tier-2 凭据队列满？→ 是 → failover 到下一个凭据
    ↓ 否
[Governor] → 并发/RPM/TPM 限额满？→ 是 → 等待 or pace_timeout → failover
    ↓ 否
上游调用
```

**背压评估**:
- ✅ **拒绝及时**: Tier-0/Tier-1 满时立即返回 503，不会无限排队
- ✅ **降级友好**: Tier-2 满时触发 failover，而非拒绝请求
- ⚠️ **Governor 等待风险**: `MaxQueueWaitMS=0` 时，concurrency governor 会无限等待（`giveUp.IsZero()`），可能导致请求卡死在 Tier-2 而不触发 failover

**建议**:
```go
// forwarder.go acquireGiveUp 中添加兜底超时
func (cf *credForwarder) acquireGiveUp(qr *QueuedRequest, gov Governor) time.Time {
    budget := cf.pipe.queueWaitBudget(qr)
    if budget > 0 {
        return time.Now().Add(budget)
    }
    if gov.Mode() == ModeConcurrency {
        // ⚠️ 当前逻辑：返回 zero time → 无限等待
        // 建议：即使配置为 0，也设置 2 分钟兜底超时
        return time.Now().Add(2 * time.Minute)
    }
    return time.Now()
}
```

---

### 2.3 Redis Backend 的集群容量一致性

**当前行为**:
- Redis backend 通过 Lua 脚本原子计算集群总容量（所有实例的 `counts` hash 字段求和）
- 降级时返回空 Admission，容量检查失效

**问题**:
1. **降级爆炸半径**: 1 个 Redis 故障 → N 个实例同时降级 → 集群容量 ×N
2. **恢复时流量尖峰**: 所有实例同时从本地积压释放到 Redis，可能瞬间超限

**建议**:
- 在降级模式下维护本地计数器（`heldTotal/heldModel/heldCred`），并限制为 `cap / estimatedInstances`
- 添加 Redis 恢复的"慢启动"机制：前 30 秒内逐步提升容量上限

---

## 3. 错误处理盲点

### 3.1 错误分类体系（errorsx 包）

**已覆盖的错误类型**（20+ 种）:
- ✅ `KindAuth`, `KindQuota`, `KindRateLimit`: 凭据级别错误
- ✅ `KindModelNotFound`, `KindModelDeprecated`: 模型生命周期
- ✅ `KindContextLength`, `KindContentFilter`: 客户端可处理错误
- ✅ `KindCircuitOpen`, `KindFpSlotSaturated`: 网关侧准入信号（2026-08-30/31 新增）
- ✅ `KindUpstreamOverloaded`, `KindNoAvailableChannel`: 中继/分销商错误

**错误处理完整性**:
- ✅ 每个错误类型都有对应的 regex 匹配规则（支持中英文）
- ✅ `ClassifyErrorWithBody` 和 `ClassifyResponseBody` 覆盖 HTTP status + body 组合
- ✅ `IsRetryable`, `IsCredentialFatal`, `IsClientBug` 提供决策函数

---

### 3.2 【中危】错误分类的优先级冲突

**位置**: `errorsx/classify.go:809-876`

**问题描述**:
```go
// ClassifyErrorWithBody 中的检查顺序
if concurrentOverloadCJKRe.Match(body) {
    return overloadKindForStatus(status)
}
if (status == 402 || status == 403 || status == 429) && budgetExceededRe.Match(body) {
    // ...
}
```

**潜在冲突**:
1. **CJK overload vs quota**: "并发过大，达到上限" 优先匹配为 `KindConcurrent`，但"达到每周上限"应为 `KindQuotaPeriodic`。当前代码已通过**先检查 CJK overload**解决，但注释不够清晰。
2. **Model deprecated vs not found**: `modelDeprecatedRe` 检查在 `modelNotFoundRe` 之前，符合预期，但**410 状态码需要同时进入两个分支**（当前代码已正确处理）。

**非阻塞问题**: 代码逻辑已正确，但建议增强注释：

```go
// ClassifyErrorWithBody 中的优先级顺序（必须严格遵守）：
// 1. noAvailableChannelRe (5xx 分销商无可用 channel，区别于 model_not_found)
// 2. concurrentOverloadCJKRe (中文并发信号，必须先于 budgetExceededRe)
// 3. budgetExceededRe (402/403/429，credit/quota 耗尽)
// 4. concurrentOverloadRe (英文并发信号)
// 5. modelDeprecatedRe (410/4xx，模型 EOL，必须先于 modelNotFoundRe)
// 6. modelNotFoundRe (400/404/422，模型名称不存在)
```

---

### 3.3 【低危】preflight rejection 的日志记录不一致

**位置**: `domains/streaming/executors/executor_dispatch.go:502-514, 529-543, 566-575, 599-609`

**问题描述**:
2026-08-29/30 的 P1 fix 为 `forwardForDispatch` 添加了 4 个 preflight rejection 记录点：
1. FP slot 饱和降级（继续执行，记录为 `KindFpSlotSaturated`）
2. Circuit breaker open（拒绝执行，记录为 `KindCircuitOpen`）
3. 并发限流拒绝（拒绝执行，记录为 `KindRateLimit`）
4. Key rotation 耗尽（拒绝执行，记录为 `KindRateLimit`）

**不一致点**:
- **FP slot 饱和**是"降级继续"，记录为单独的 `KindFpSlotSaturated`
- **并发限流 + key 耗尽**是"拒绝执行"，但都记录为 `KindRateLimit`

虽然分类合理（并发限流和 key 耗尽都是"速率限制"），但**dashboard 无法区分**是网关侧限流还是 key 轮询失败。

**建议**:
```go
// 方案 1: 引入子类型（extra context 已包含 rejection_type，可直接使用）
logDispatchPreflightRejection(e.FailureLogger, params, cand, dctx, startedAt,
    acquireErr, errorsx.KindRateLimit,
    map[string]any{
        "rate_limit_rejection": true,
        "rejection_type":       "concurrency_limiter",  // ← 已存在
        "limiter_layer":        acquireErr.Error(),     // 记录具体层级
    })

// 方案 2: 拆分为独立 Kind（需要 errorsx 包支持）
const (
    KindConcurrencyLimit ErrorKind = "concurrency_limit"  // 网关并发限流
    KindKeyExhausted     ErrorKind = "key_exhausted"       // key 轮询耗尽
)
```

---

### 3.4 错误日志的完整性检查

**candidate_failure_logs_hot 记录点**:
| 阶段 | 记录位置 | 覆盖场景 |
|------|---------|---------|
| Preflight | executor_dispatch.go:502-609 | FP slot / circuit / limiter / key 耗尽 |
| Upstream | executor_dispatch.go:712-756 | 上游调用失败（含 stream 中断） |
| 模型不存在 | executor.go:recordModelNotFound | MNF 审计 |

✅ **覆盖完整**: 所有 dispatch forward 路径的失败都有日志记录

---

## 4. 关键场景安全性

### 4.1 异步处理的 Panic Recovery

**已保护的位置**:
| 文件 | 行号 | recover() 位置 | 评估 |
|------|------|---------------|------|
| forwarder.go | 415-424 | `attempt` goroutine | ✅ 完整（转换为 ForwardOutcome 错误） |
| pipeline.go | 283-287 | observationSink | ⚠️ 吞噬 panic（建议记录日志） |
| governor_snapshot_observer.go | - | snapshot tick | ✅ 已保护 |
| retry_schedule.go | - | 定时任务 | ✅ 已保护 |
| queue_mirror.go | - | Redis 镜像 | ✅ 已保护 |

**未保护的位置**（可能触发 panic 的地方）:
- `credForwarder.loop`: **已通过 defer wg.Done() 保护**，即使 panic 也会触发 wg.Done()
- `runModelDrainer`: 未显式 recover，但所有 channel 操作都是 safe-to-panic（Go runtime 保证）

**建议**:
在 `credForwarder.loop` 和 `runModelDrainer` 中添加显式 panic recovery（防御性编程）：

```go
func (cf *credForwarder) loop() {
    defer func() {
        if r := recover(); r != nil {
            slog.Error("dispatch: credential forwarder loop panic",
                "credential_id", cf.cred.CredentialID,
                "panic", r)
        }
    }()
    defer cf.pipe.wg.Done()
    defer cf.wg.Wait()
    // ...
}
```

---

### 4.2 网络请求超时和重试逻辑

**超时配置**:
- ✅ `dispatchExecutionContext` 为 detached stream 设置 `detachedStreamMaxLifetime`
- ✅ 每个 HTTP 请求继承 `params.R.Context()`，客户端断开会传播取消信号
- ✅ Governor.Acquire 支持 `giveUp` 超时参数

**重试逻辑**:
- ✅ `RetryPerCredential` 控制同凭据重试次数（默认 1，即 max 2 次尝试）
- ✅ `maxRetryBudget` (22) 控制全局最大尝试次数
- ✅ 失败后通过 `routeFailover` 切换到下一个凭据

**潜在问题**: `pace_timeout` 后会标记凭据为"已尝试"并将 `CredRetryCount` 设为 `maxRetryBudget`（forwarder.go:339-341），这会**跳过该凭据的所有后续重试**。如果所有凭据都因 pace_timeout 被标记，请求会立即耗尽。

**建议**:
```go
// forwarder.go acquire 中的 pace_timeout 处理
if IsPaceTimeout(err) {
    qr.markTriedCredential(cf.cred.CredentialID)
    // ⚠️ 当前代码：qr.CredRetryCount = maxRetryBudget（强制耗尽）
    // 建议：仅标记凭据已尝试，保留重试预算给其他凭据
    // qr.CredRetryCount = maxRetryBudget  // ← 删除此行
}
```

---

### 4.3 TCP 连接池健康检查

**连接池管理**: 代码中未显式管理 TCP 连接池（使用 Go 标准库的 `http.Client`，连接池由 `http.Transport` 自动管理）。

**潜在问题**:
- 长连接可能因上游 idle timeout 而变成"僵尸连接"
- 熔断器打开后，连接池中的连接仍可能保持 established 状态

**建议**:
在 `http.Client` 构造时配置连接池参数（如果尚未配置）：
```go
transport := &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 10,
    IdleConnTimeout:     90 * time.Second,
    DisableKeepAlives:   false,  // 保持 keep-alive
}
client := &http.Client{
    Transport: transport,
    Timeout:   30 * time.Second,  // 请求级超时
}
```

---

### 4.4 资源泄漏风险分析

#### 4.4.1 Goroutine 泄漏

**已保护的 Goroutine**:
| Goroutine | 启动位置 | 退出机制 | 评估 |
|-----------|---------|---------|------|
| credForwarder.loop | forwarder.go:66 | `ctx.Done()` + `pipe.wg.Wait()` | ✅ 安全 |
| credForwarder.attempt | forwarder.go:164-165 | `cf.wg.Done()` | ✅ 安全 |
| dispatcher worker | pipeline.go (Start) | `stopCh` + `wg.Wait()` | ✅ 安全 |
| failover mover | pipeline.go (Start) | `stopCh` + `wg.Wait()` | ✅ 安全 |
| model drainer | pipeline.go (runModelDrainer) | `stopCh` + drain | ✅ 安全 |
| Redis heartbeat | queue_backend_redis.go:198 | `hbCancel()` + `hbWG.Wait()` | ✅ 安全 |
| Retry scheduler | retry_schedule.go | `Close()` + promoter loop exit | ✅ 安全 |

**评估**: 所有后台 goroutine 都有明确的退出路径和 WaitGroup 保护，**无明显泄漏风险**。

#### 4.4.2 Channel 泄漏

**Channel 生命周期**:
- `credForwarder.queue`: 通过 `replaceDepth` 可能创建多个 channel，旧 channel 通过 `pendingOld` 引用并在 `reclaimPendingOld` 中排空，**无泄漏**
- `credForwarder.wakeCh`: unbuffered channel，loop 退出后自动 GC，**无泄漏**
- `dispatchIn`, `failoverCh`: 在 `Stop()` 中 close，drainer/mover 会排空，**无泄漏**

**评估**: Channel 管理规范，**无泄漏风险**。

#### 4.4.3 内存泄漏

**潜在泄漏点**:
1. **LifecycleRegistry**: 当 completed 条目超过 `CompletedWatermark` 时会 FIFO 驱逐，但如果**请求永不 complete**，会累积在 registry 中
   - **缓解措施**: `unfinishedLimit` 限制了 pending+in_flight 总数（默认 300）
   - **残留风险**: 如果请求卡在 in_flight 但永不 complete（如 stream 未正确关闭），会占用 registry 槽位

2. **DimensionIndex**: 每个维度（model/credential/provider）维护一个 ring buffer，TTL 默认 900 秒
   - **评估**: TTL 自动清理，**风险可控**

3. **WaterfallRing**: 保存最近完成的请求 timeline
   - **评估**: 固定容量 ring，**无泄漏**

**建议**:
```go
// 为 in_flight 请求添加兜底超时（在 registry 层面）
func (r *LifecycleRegistry) MarkInFlight(requestID string, now time.Time) {
    // ...
    entry.State = LifecycleInFlight
    if entry.StartedAt == nil {
        started := now
        entry.StartedAt = &started
        
        // 启动兜底 timeout goroutine（10 分钟未 complete → 强制驱逐）
        go func() {
            time.Sleep(10 * time.Minute)
            r.mu.Lock()
            if e, ok := r.entries[requestID]; ok && e.State == LifecycleInFlight {
                slog.Warn("dispatch: request stuck in_flight, forcing completion",
                    "request_id", requestID, "started_at", e.StartedAt)
                r.markCompletedLocked(requestID, time.Now())
            }
            r.mu.Unlock()
        }()
    }
}
```

#### 4.4.4 数据库连接泄漏

**连接池管理**: 使用 `pgxpool.Pool`，自动管理连接生命周期。

**潜在问题**:
- `session_aggregate_outbox_reaper` 在每个 tick 中创建事务（Begin → Rollback/Commit），如果 `claimAndReplay` panic，事务可能未正确 Rollback
- **已缓解**: `defer tx.Rollback(ctx)` 在函数入口设置，即使 panic 也会执行

**评估**: 数据库连接管理规范，**无明显泄漏风险**。

---

## 5. 修正建议优先级

### P0 - 立即修复（影响生产稳定性）

1. **Redis Backend 降级容量保护** (§1.2)
   - 影响：Redis 故障时集群容量失控，可能触发上游限流
   - 修复难度：中等（需要本地计数器 + 容量估算）
   - 预计工作量：2-3 天

2. **credForwarder.replaceDepth 竞态修复** (§1.1)
   - 影响：请求可能永久卡在旧 channel（概率低但后果严重）
   - 修复难度：低（调整代码顺序）
   - 预计工作量：半天 + 单元测试

---

### P1 - 短期改进（提升可观测性和鲁棒性）

3. **Governor 无限等待的兜底超时** (§2.2)
   - 影响：concurrency governor 在 MaxQueueWaitMS=0 时可能无限等待
   - 修复难度：低
   - 预计工作量：1 天

4. **ObservationSink Panic 日志记录** (§1.4)
   - 影响：吞噬 panic 会掩盖 sink 实现 bug
   - 修复难度：低
   - 预计工作量：1 小时

5. **Preflight Rejection 错误分类细化** (§3.3)
   - 影响：dashboard 无法区分并发限流 vs key 耗尽
   - 修复难度：低（已有 `rejection_type` 字段）
   - 预计工作量：半天

---

### P2 - 中期优化（架构改进）

6. **容量配置验证** (§2.1)
   - 影响：防止配置错误（MaxQueueDepth 过大）
   - 修复难度：低
   - 预计工作量：1 天

7. **Registry In-Flight 兜底超时** (§4.4.3)
   - 影响：防止卡死请求占用 registry 槽位
   - 修复难度：中等（需要 goroutine 生命周期管理）
   - 预计工作量：2 天

8. **Pace Timeout 重试预算调整** (§4.2)
   - 影响：pace_timeout 不应立即耗尽重试预算
   - 修复难度：低
   - 预计工作量：1 天

---

## 6. 测试建议

### 6.1 并发压测场景

```go
// 场景 1: credForwarder.replaceDepth 竞态测试
func TestCredForwarderReplaceDepthRace(t *testing.T) {
    cf := newCredForwarder(...)
    
    var wg sync.WaitGroup
    // 生产者：持续发送请求
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 1000; j++ {
                qr := &QueuedRequest{...}
                select {
                case *cf.queue.Load() <- qr:
                default:
                }
            }
        }()
    }
    
    // 同时进行多次 replaceDepth
    for i := 0; i < 10; i++ {
        cf.replaceDepth(500 + i*100)
        time.Sleep(10 * time.Millisecond)
    }
    
    wg.Wait()
    // 验证：所有请求都被处理或正确回收
}

// 场景 2: Redis Backend 降级压测
func TestRedisBackendDegradationCapacity(t *testing.T) {
    // 启动 4 个模拟实例
    // Redis 故障后，验证集群总容量不超过 1.5 × 配置值
}

// 场景 3: Governor 热替换并发测试
func TestGovernorHotSwapConcurrency(t *testing.T) {
    cf := newCredForwarder(...)
    
    // 100 个 goroutine 并发调用 acquire
    // 同时进行 replaceGov
    // 验证：所有 Release 都调用了正确的 Governor 实例
}
```

---

### 6.2 错误分类覆盖率测试

```go
// 验证所有 20+ 种错误类型都有测试用例
func TestErrorClassificationCoverage(t *testing.T) {
    testCases := []struct{
        name       string
        status     int
        body       string
        wantKind   errorsx.ErrorKind
    }{
        {"circuit_open", 0, "circuit open", errorsx.KindCircuitOpen},
        {"fp_slot_saturated", 0, "fp slot saturated", errorsx.KindFpSlotSaturated},
        // ... 覆盖所有 Kind
    }
    
    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            kind := errorsx.ClassifyErrorWithBody(tc.status, []byte(tc.body))
            if kind != tc.wantKind {
                t.Errorf("got %s, want %s", kind, tc.wantKind)
            }
        })
    }
}
```

---

### 6.3 资源泄漏检测

```bash
# 使用 pprof 检测 goroutine 泄漏
go test -run=TestPipelineShutdown -cpuprofile=cpu.prof -memprofile=mem.prof
go tool pprof -http=:8080 mem.prof

# 检查点：
# 1. Stop() 后 goroutine 数量应回到基线
# 2. 内存使用应稳定（无持续增长）
# 3. channel 应全部关闭（通过 runtime.NumGoroutine() 间接验证）
```

---

## 7. 总结

### 7.1 架构优势

1. **清晰的分层**: Total → Model → Credential 三层队列职责分明
2. **锁粒度合理**: 全局锁（`modelMu`, `credMu`）仅保护 map 读写，不阻塞业务逻辑
3. **Fail-open 设计**: Redis backend 降级时保持可用性（虽然容量失控）
4. **完善的错误分类**: 20+ 种错误类型覆盖所有上游场景

### 7.2 核心风险

1. **Redis 降级容量失控**（P0）：影响生产稳定性
2. **Channel 切换竞态**（P0）：概率低但后果严重
3. **无限等待风险**（P1）：可能导致请求卡死

### 7.3 改进路线图

**短期（1-2 周）**:
- 修复 P0 问题（§5 中 1-2 项）
- 添加关键路径的 panic 日志

**中期（1-2 月）**:
- 完善 P1/P2 改进（§5 中 3-8 项）
- 补充并发压测用例

**长期（持续优化）**:
- 引入分布式追踪（OpenTelemetry）增强可观测性
- 探索基于 eBPF 的网络连接监控

---

## 附录 A: 关键代码路径

### A.1 请求生命周期

```
Submit (pipeline.go)
  ↓ [Tier-0 admission]
  → totalQueue.TryAdmit
  → registry.RegisterPending
  ↓
Dispatcher (pipeline.go:runDispatcher)
  ↓ [Model resolution + Route]
  → ModelResolveFunc
  → RouteFunc (→ Router.PlanCandidates)
  ↓ [Tier-1 enqueue]
  → getOrCreateModelQueue
  → modelQueue.enqueue (→ dispatchIn channel)
  ↓
Model Drainer (pipeline.go:runModelDrainer)
  ↓ [Tier-2 enqueue]
  → getOrCreateForwarder
  → credForwarder.tryReserve + channel send
  ↓
Credential Forwarder Loop (forwarder.go:loop)
  ↓ [Governor admission]
  → Governor.Acquire (concurrency/RPM/TPM)
  ↓ [Upstream forward]
  → ForwardFunc (→ executor.forwardForDispatch)
    → FP slot → Circuit → Limiter → Key rotation
    → executeOpenAI / executeAnthropic
  ↓ [Success or failover]
  → complete (ResultCh ← outcome)
  OR
  → routeFailover (→ failoverCh → move)
```

### A.2 错误处理路径

```
Upstream Error
  ↓
ForwardOutcome{Err, ErrorKind, BytesSent}
  ↓
[BytesSent?]
  ↓ Yes → complete (terminal, 已发送内容给客户端)
  ↓ No  → routeFailover
              ↓
         Failover Mover (failover.go:move)
              ↓
         [Credential fatal?]
              ↓ Yes → markTriedCredential, 切换下一个凭据
              ↓ No  → 同凭据重试（CredRetryCount < RetryPerCredential）
              ↓
         [Still have candidates?]
              ↓ Yes → re-enqueue to dispatcher
              ↓ No  → [Model change allowed?]
                        ↓ Yes → ModelRecommendFunc, 切换模型
                        ↓ No  → complete (exhausted)
```

---

**审计完成时间**: 2026-08-31 23:59:59  
**下一次审计建议**: 3 个月后（或 P0 修复完成后）

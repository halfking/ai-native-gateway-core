# 路由系统全面审计报告

**审计日期**: 2026-07-07  
**审计范围**: 路由实现、限流机制、凭据选择、资源利用  
**审计人**: ZCode Agent

## 执行概要

已完成对整个路由系统的全面审计，重点关注限流、凭据节点选择和资源利用问题。发现系统存在**多条并行路由路径**和**资源管理复杂度高**的问题，但整体架构具备较强的容错机制。

## 关键发现

### 🔴 严重问题

#### 1. 多条路由路径共存，存在不一致风险

**位置**: `domains/streaming/executors/router.go:56-278`

系统同时存在三条路由路径：
- **URSM路径** (统一路由状态管理器) - 新架构，未完全启用
- **StateManager路径** - 中间态架构
- **Legacy路径** - 传统路由逻辑

```go
func (r *Router) PlanCandidates(...) {
    // 优先使用URSM路由（如果可用）
    if r.URSM != nil && r.URSM.Enabled() {
        return r.planWithURSM(...)
    }
    
    // 使用状态管理器过滤（如果启用）
    if r.StateManager != nil && r.StateManager.Enabled() {
        available = r.filterAvailableWithStateManager(ctx, candidates)
    } else {
        available = filterAvailable(candidates)
    }
    // ... legacy logic
}
```

**风险**：
- 不同路径可能产生不同的路由结果
- 特性开关切换时可能导致流量突变
- 三套状态管理逻辑增加维护成本

**影响范围**: 所有路由决策

---

#### 2. 指纹槽降级模式可能导致凭据资源过载

**位置**: `domains/streaming/executors/executor.go:811-824`

当所有凭据的指纹槽都饱和时，系统会进入"降级模式"，允许请求在没有指纹槽的情况下执行：

```go
if len(filtered) == 0 && len(candidates) > 0 {
    slog.Warn("cred_fp_slot all saturated, degrading to full candidate set",
        "candidate_count", len(candidates),
        "client_model", params.ClientModel,
    )
    fpSlotDegraded = true
    // 使用原始候选集，无指纹隔离
}
```

**风险**：
- 多个请求可能使用相同的虚拟身份，增加被识破风险
- 失去了指纹槽本该提供的隔离保护
- 降级模式下的并发控制仅依赖 Limiter，可能超过凭据实际承受能力

**建议**：添加降级模式的监控告警，降级请求占比超过10%时应触发告警

---

#### 3. 并发限流层级过多，存在死锁风险

**位置**: `domains/credential/limiter.go:310-371`

系统实现了5层嵌套限流：

```go
func (l *Limiter) AcquireAll(...) (ReleaseFunc, error) {
    // 1. Global limit (1000)
    if err := l.global.Acquire(ctx); err != nil {
        return nil, fmt.Errorf("global limit: %w", err)
    }
    
    // 2. Pool limit (100/provider)
    pool := l.Pool(providerID)
    if err := pool.Acquire(ctx); err != nil {
        l.global.Release()  // 回滚
        return nil, fmt.Errorf("pool limit: %w", err)
    }
    
    // 3. Credential limit (50/credential)
    cred := l.Credential(providerID, credentialID)
    if err := cred.Acquire(ctx); err != nil {
        pool.Release()     // 回滚
        l.global.Release() // 回滚
        return nil, fmt.Errorf("credential limit: %w", err)
    }
    
    // 4. Identity limit (10/identity) - 非阻塞
    // 5. Per-key limit - 非阻塞
}
```

**问题**：
- 3层阻塞获取 + 手动回滚，代码复杂度高
- 如果 context 超时，可能在某一层阻塞导致后续层无法获取
- Release 调用顺序必须精确匹配，否则会泄漏资源

**实际问题**: 在 executor.go:887-904，存在复杂的资源清理逻辑，如果 defer 顺序错误会导致资源泄漏

---

### 🟡 中等问题

#### 4. 凭据选择算法存在"固死"倾向

**位置**: `domains/streaming/executors/router.go:435-593`

P2C (Power of Two Choices) 算法在相同 loadScore 时会随机选择，但实际存在多个导致"固死在某个凭据"的因素：

1. **Sticky 缓存优先级最高** (router.go:153-155)
```go
if stickyCredentialID != nil {
    ordered = prioritizeSticky(ordered, *stickyCredentialID)
}
```

2. **轮转偏移量对新会话不生效** (router.go:142-151)
```go
if stickyCredentialID == nil && len(ordered) > 1 {
    offset := int(r.rrCounter.Add(1) % uint64(len(ordered)))
    ordered = rotateCandidates(ordered, offset)
}
```
只有新会话才会轮转，有 sticky 的会话始终使用同一凭据

3. **Bandit 算法可能放大不均衡** (router.go:747-805)
Thompson Sampling 会偏向历史表现好的凭据，导致"赢家通吃"

**具体场景**：
- 系统有5个凭据，其中1个因为启动早，积累了更多成功记录
- Bandit 算法会给它更高的采样分数
- 更多流量 → 更多成功 → 更高分数 → 更多流量（正反馈循环）
- 其他4个凭据利用率不足

---

#### 5. FpSlots 和 Limiter 维度混淆

**位置**: `domains/streaming/executors/router.go:554-563`

loadScore 计算中混合了两个概念：

```go
// Legacy inFlight from limiter (per-identity)
inFlight := 0
if r.Limiter != nil {
    if cred := r.Limiter.Credential(c.ProviderID, c.CredentialID); cred != nil {
        inFlight = cred.Used()
    }
}

// Global credential-level concurrency from FpSlots
fpUsed := 0
if r.FpSlots != nil && r.FpSlots.Enabled() {
    if limit, used, _ := r.FpSlots.Stats(ctx, c.CredentialID, c.ConcurrencyLimit); used != nil {
        fpUsed = *used
    }
}

score := (float64(inFlight) + pressure*float64(fpLimit)) * latencyPenalty / (float64(w) * quality)
```

**问题**：
- inFlight 是单个 identity 的并发
- fpUsed 是整个凭据的并发
- 两者量级不同，混合计算导致权重失衡

---

#### 6. 限流器动态 Shrink/Recover 可能导致波动

**位置**: `domains/credential/limiter.go:123-167`

当凭据遇到限流时，会缩小其容量到 70%：

```go
func (l *Limiter) Shrink(providerID, credentialID int) {
    l.Credential(providerID, credentialID).Shrink(0.7)
}
```

然后每5分钟恢复 50%：

```go
func (l *Limiter) recoveryLoop() {
    ticker := time.NewTicker(shrinkRecoveryInterval) // 5min
    for {
        select {
        case <-ticker.C:
            l.recoveryStep()  // 恢复 50% of (target - current)
        }
    }
}
```

**风险**：
- 如果凭据持续触发限流，容量会震荡在 0.7x → 0.85x → 0.7x
- 恢复时可能突然增加流量，再次触发限流
- 没有考虑凭据实际健康状态

---

### 🟢 设计优点

#### 1. 多层降级机制保证可用性

- FpSlot 饱和 → 降级模式继续执行
- 单候选者瞬态不可用 → tryDegradedMode (router.go:816-833)
- 所有候选者过滤 → fail-open 保持原集合

#### 2. 熔断器设计合理

**位置**: `domains/credential/breaker.go`
- 4种状态：Closed / Open / HalfOpen / Quarantined
- 根据错误类型自动调整冷却策略
- 指数退避防止雪崩
- 客户端错误不触发熔断 (breaker.go:213-215)

#### 3. 资源清理使用 defer 保证安全

**位置**: `domains/streaming/executors/executor.go:921-946`

```go
func() {
    releasePeak := e.PeakCollector != nil
    if releasePeak {
        e.PeakCollector.Acquire(...)
    }
    
    defer func() {
        if releasePeak {
            e.PeakCollector.Release(...)
        }
        release()  // Limiter
        if fpLease != nil {
            e.FpSlots.Release(...)
        }
    }()
    
    // Execute
}()
```

即使执行提前返回，资源也会被释放

---

## 核心路由流程分析

### 完整路由决策链

```
1. 请求到达
   └─> provider/client.go:GetCandidates()

2. 模型解析
   └─> resolveModelDB() - 规范化模型名

3. 候选者加载
   └─> loadCandidatesDB() - SQL查询所有可能的凭据
       ├─ 过滤: lifecycle_status != 'active'
       ├─ 过滤: is_routable = FALSE
       ├─ 过滤: model_probe_state = 'broken_confirmed'
       └─ 过滤: recent_success_rate < 0.3 (with 20+ samples)

4. Router.PlanCandidates()
   ├─ 路径选择 (URSM / StateManager / Legacy)
   ├─ filterAvailable() - 可用性过滤
   ├─ filterHealthyNodes() - FpSlots 健康检查
   ├─ splitByBillingRound() - 分离计划/按量
   └─ planByTier()
       ├─ Bandit排序 或 P2C排序
       ├─ 轮转偏移 (新会话)
       └─ Sticky优先级调整

5. Executor.executeStream()
   ├─ 逐个尝试候选者
   ├─ FpSlots.Acquire() - 指纹槽
   ├─ Circuit.Allow() - 熔断检查
   ├─ Limiter.AcquireAll() - 5层限流
   ├─ executeAnthropic/executeOpenAI()
   └─ Release resources (defer)

6. 状态回写
   ├─ recordStickySuccess()
   ├─ recordBanditSuccess()
   ├─ HealthTracker.OnSuccess()
   └─ StateObserver.RecordSuccess()
```

### 限流层级详解

```
Global Limiter (1000)
  └─ Pool (100/provider)
      └─ Credential (50/credential)
          └─ Identity (10/identity, soft)
              └─ Per-Key (from DB, soft)

并行独立:
  └─ FpSlots (per-credential fingerprint pool, default 20)
```

**关键观察**：
- Limiter 和 FpSlots 是两个独立维度
- Limiter 控制并发请求数
- FpSlots 控制虚拟身份数
- 但在 loadScore 中被混合权重计算

---

## 潜在问题场景

### 场景1：凭据资源未充分利用

**条件**：
- 5个凭据，每个 concurrency_limit=50
- 总容量 = 250 并发
- 但 Sticky 使 80% 流量集中在前2个凭据

**结果**：
- 凭据1,2: 饱和 (50/50)
- 凭据3,4,5: 空闲 (5/50)
- 实际利用率: 115/250 = 46%

**根因**：
- Sticky TTL 过长 (30分钟，policy.go:256)
- 新会话轮转未生效
- Bandit 放大不均衡

---

### 场景2：FpSlot 饱和但 Limiter 未满

**条件**：
- 凭据 fp_slot_limit=20, concurrency_limit=50
- 20个长连接会话，每个平均1个并发请求

**结果**：
- FpSlots: 20/20 (满)
- Limiter: 20/50 (40% 利用率)
- 新会话被拒绝："cred_fp_slot saturated"

**根因**：
- FpSlot 和并发限制维度不匹配
- LRU 回收不够激进 (5分钟活跃窗口，credentialfpslot/slot.go:66-74)

---

### 场景3：限流器震荡

**条件**：
- 凭据触发上游限流
- Limiter.Shrink(0.7) → 容量 50→35
- 5分钟后恢复到 42
- 再次触发限流 → 29

**结果**：
- 容量在 29-42 之间震荡
- 无法稳定在安全值
- 频繁触发限流告警

---

## 建议改进

### 短期修复 (1-2周)

#### 1. 统一路由路径
- 决定是否启用 URSM，移除未使用的路径
- 如果保留多路径，添加 A/B 测试框架和指标对比

#### 2. 修复 loadScore 权重
```go
// 分离两个维度
concurrencyPressure := float64(fpUsed) / float64(fpLimit)
identityInFlight := float64(inFlight) / float64(identityLimit)

// 独立权重
score := (concurrencyPressure * 10 + identityInFlight) * latencyPenalty / (w * quality)
```

#### 3. 添加降级模式监控
```go
if fpSlotDegraded {
    metrics.RecordFpSlotDegradation(clientModel)
    if degradationRate > 0.1 { // 10%阈值
        alert.Trigger("fp_slot_saturation")
    }
}
```

#### 4. 优化 Sticky TTL
- 当前 30分钟过长
- 建议根据模型类型动态调整：
  - Chat: 5分钟
  - Completion: 10分钟
  - Embedding: 30秒

---

### 中期重构 (1-2月)

#### 1. 实现动态负载均衡
- 周期性重新评估 sticky 绑定
- 如果绑定凭据压力 > 平均压力 1.5倍，解绑并重选

#### 2. FpSlot 激进回收
- 活跃窗口从5分钟降低到2分钟
- 添加 LFU (Least Frequently Used) 策略

#### 3. 限流器智能恢复
```go
func (l *Limiter) SmartRecover(credID int, recentSuccessRate float64) {
    if recentSuccessRate > 0.95 {
        // 快速恢复
        l.RecoverStep(targetCapacity)
    } else if recentSuccessRate < 0.7 {
        // 暂停恢复
        return
    }
    // 正常恢复
}
```

---

### 长期优化 (3-6月)

#### 1. 完成 URSM 迁移
- 按照 docs/ursm-routing-redesign/ 完成迁移
- 统一状态管理
- 简化代码路径

#### 2. 实现流量预测
- 基于历史数据预测各凭据负载
- 提前调整路由权重

#### 3. 凭据池弹性扩容
- 检测持续饱和信号
- 自动提交告警，建议添加新凭据

---

## 监控建议

### 关键指标

#### 1. 路由决策延迟
- **指标**: p50/p95/p99
- **目标**: p99 < 20ms
- **告警**: p99 > 100ms

#### 2. 凭据利用率分布
- **指标**: 每个凭据的实际并发 / 容量
- **目标**: 标准差 < 20%
- **告警**: 标准差 > 30% (不均衡)

#### 3. FpSlot 饱和率
- **指标**: fpSlotDegraded 请求占比
- **目标**: < 1%
- **告警**: > 5%

#### 4. 限流器容量历史
- **指标**: 跟踪 Shrink/Recover 事件
- **可视化**: 容量震荡趋势图
- **告警**: 1小时内超过3次震荡

#### 5. Sticky 命中率
- **指标**: 会话复用比例
- **目标**: 60-80%
- **告警**: > 90% (过度粘性) 或 < 50% (失效过快)

---

## 代码位置索引

### 核心路由组件
- `provider/client.go` - 候选者加载 (1143行)
- `domains/streaming/executors/router.go` - 路由决策 (857行)
- `domains/streaming/executors/executor.go` - 执行器 (3000+行)
- `domains/credential/limiter.go` - 并发限流 (448行)
- `domains/credential/breaker.go` - 熔断器 (483行)
- `credentialfpslot/slot.go` - 指纹槽管理 (31731字节)

### 状态管理
- `domains/ursm/` - 统一路由状态管理 (新架构)
- `domains/credentialstate/` - 凭据状态缓存 (中间态)
- `domains/credentialhealth/` - 健康度追踪

### 配置和策略
- `provider/client.go:DefaultPolicy()` - 默认策略
- `settings/spec_ratelimit.go` - 限流配置
- `domains/ursm/config.go` - URSM配置

---

## 结论

系统整体架构设计合理，具有多层容错机制。主要问题集中在：

1. **架构过渡期** - 三条路由路径共存，增加复杂度
2. **维度混淆** - FpSlot 和 Limiter 未清晰分离
3. **负载不均** - Sticky + Bandit 导致凭据利用率不均

建议优先解决短期修复项，确保资源充分利用，然后按计划推进 URSM 迁移以简化架构。

---

**审计状态**: ✅ 完成  
**下一步行动**: 根据建议改进优先级制定实施计划

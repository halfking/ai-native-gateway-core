# Phase 2.1 技术分析 - 压力信号数据采集

**日期**: 2026-07-24  
**阶段**: Phase 2.1 - 数据采集接口设计  
**状态**: 分析完成，准备实施

---

## 一、现有系统分析

### 1.1 FingerprintSlotManager 分析

**位置**: `domains/ursm/fp_slot_manager.go`

**核心数据结构**:
```go
type FingerprintSlotManager struct {
    redis      *redis.Client
    activeGate time.Duration // 活跃阈值（默认5分钟）
    slotTTL    time.Duration // 槽位TTL（默认30分钟）
    pinTTL     time.Duration // Pin TTL（默认24小时）
}
```

**现有方法**:
- `CheckAndAcquire()` - 检查并获取指纹槽
- `Release()` - 释放指纹槽
- `ForceUnpin()` - 强制解绑pin

**Redis 数据结构**:
```
# 槽位键（记录占用会话）
fpslot:cred:{credentialID}:slot:{slotIndex} = sessionID
TTL: 30分钟

# Pin键（会话绑定槽位）
fppin:session:{sessionID}:cred:{credentialID} = slotIndex
TTL: 24小时
```

**压力计算方式**:
- 槽位上限：从候选节点的 `fp_slot_limit` 字段获取
- 当前占用：扫描 Redis 的 `fpslot:cred:{credentialID}:slot:*` 键

**挑战**:
- ⚠️ 需要扫描 Redis 键来计算占用数（可能较慢）
- ✅ 可以使用 Redis SCAN 命令分批扫描
- ✅ 结果可以缓存（TTL 1-5秒）

---

### 1.2 Limiter 分析

**位置**: `domains/credential/limiter.go`

**核心数据结构**:
```go
type Limiter struct {
    name     string
    global   *Semaphore           // Layer 0: 全局并发限制
    pools    map[int]*Semaphore   // Layer 1: Pool 并发限制
    keys     map[int]*Semaphore   // Layer 2: Credential 并发限制
    identities map[string]*Semaphore // Layer 3: Identity 并发限制
    mu       sync.RWMutex
}

type Semaphore struct {
    name     string
    capacity atomic.Int64  // 容量（上限）
    used     atomic.Int64  // 当前占用数
}
```

**现有方法**:
- `Capacity()` - 返回当前容量
- `Used()` - 返回当前占用数
- `Available()` - 返回剩余容量
- `AcquireAll()` - 获取所有层的令牌
- `ReleaseAll()` - 释放所有层的令牌

**压力计算方式**:
- ✅ 每个 Semaphore 已有 `Capacity()` 和 `Used()` 方法
- ✅ 压力 = Used / Capacity（直接计算，无需扫描）

**优势**:
- ✅ 数据在内存中，查询 O(1) 时间复杂度
- ✅ atomic.Int64 操作，线程安全
- ✅ 不需要访问 Redis

---

## 二、GetPressure() 接口设计

### 2.1 FingerprintSlotManager.GetPressure()

**签名**:
```go
// GetPressure 返回指定 credential 的 FpSlots 压力（0-1）
// 压力 = 当前占用槽位数 / 槽位上限
// 返回 0 表示无压力或无限制
func (m *FingerprintSlotManager) GetPressure(
    ctx context.Context,
    credentialID int,
    slotLimit int, // 从 candidate.FpSlotLimit 传入
) (float64, error)
```

**实现思路**:
```go
func (m *FingerprintSlotManager) GetPressure(
    ctx context.Context,
    credentialID int,
    slotLimit int,
) (float64, error) {
    // 无限制（slotLimit == 0 或 nil）
    if slotLimit <= 0 {
        return 0, nil
    }
    
    // Redis 不可用
    if m.redis == nil {
        return 0, nil
    }
    
    // 扫描 fpslot:cred:{credentialID}:slot:* 键
    pattern := fmt.Sprintf("fpslot:cred:%d:slot:*", credentialID)
    var cursor uint64
    var count int
    
    for {
        keys, nextCursor, err := m.redis.Scan(ctx, cursor, pattern, 100).Result()
        if err != nil {
            return 0, err
        }
        count += len(keys)
        cursor = nextCursor
        if cursor == 0 {
            break
        }
    }
    
    pressure := float64(count) / float64(slotLimit)
    if pressure > 1.0 {
        pressure = 1.0
    }
    
    return pressure, nil
}
```

**性能优化**:
```go
// 添加缓存（避免频繁扫描 Redis）
type pressureCache struct {
    value     float64
    timestamp time.Time
}

var fpPressureCache = sync.Map{} // credentialID -> pressureCache
const fpPressureCacheTTL = 5 * time.Second

func (m *FingerprintSlotManager) GetPressure(
    ctx context.Context,
    credentialID int,
    slotLimit int,
) (float64, error) {
    if slotLimit <= 0 {
        return 0, nil
    }
    
    // 检查缓存
    if cached, ok := fpPressureCache.Load(credentialID); ok {
        c := cached.(pressureCache)
        if time.Since(c.timestamp) < fpPressureCacheTTL {
            return c.value, nil
        }
    }
    
    // 计算压力（扫描 Redis）
    pressure, err := m.calculatePressure(ctx, credentialID, slotLimit)
    if err != nil {
        return 0, err
    }
    
    // 更新缓存
    fpPressureCache.Store(credentialID, pressureCache{
        value:     pressure,
        timestamp: time.Now(),
    })
    
    return pressure, nil
}
```

---

### 2.2 Limiter.GetPressure()

**签名**:
```go
// GetPressure 返回指定 credential 的 Limiter 压力（0-1）
// 压力 = max(各层 Used / Capacity)
// 返回 0 表示无压力或无限制
func (l *Limiter) GetPressure(
    credentialID int,
    poolID int,
    identityKey string,
) float64
```

**实现思路**:
```go
func (l *Limiter) GetPressure(
    credentialID int,
    poolID int,
    identityKey string,
) float64 {
    var maxPressure float64
    
    // Layer 0: Global
    if l.global != nil {
        cap := l.global.Capacity()
        if cap > 0 {
            used := l.global.Used()
            pressure := float64(used) / float64(cap)
            if pressure > maxPressure {
                maxPressure = pressure
            }
        }
    }
    
    // Layer 1: Pool
    l.mu.RLock()
    if pool, ok := l.pools[poolID]; ok {
        l.mu.RUnlock()
        cap := pool.Capacity()
        if cap > 0 {
            used := pool.Used()
            pressure := float64(used) / float64(cap)
            if pressure > maxPressure {
                maxPressure = pressure
            }
        }
    } else {
        l.mu.RUnlock()
    }
    
    // Layer 2: Credential
    l.mu.RLock()
    if cred, ok := l.keys[credentialID]; ok {
        l.mu.RUnlock()
        cap := cred.Capacity()
        if cap > 0 {
            used := cred.Used()
            pressure := float64(used) / float64(cap)
            if pressure > maxPressure {
                maxPressure = pressure
            }
        }
    } else {
        l.mu.RUnlock()
    }
    
    // Layer 3: Identity
    if identityKey != "" {
        l.mu.RLock()
        if identity, ok := l.identities[identityKey]; ok {
            l.mu.RUnlock()
            cap := identity.Capacity()
            if cap > 0 {
                used := identity.Used()
                pressure := float64(used) / float64(cap)
                if pressure > maxPressure {
                    maxPressure = pressure
                }
            }
        } else {
            l.mu.RUnlock()
        }
    }
    
    if maxPressure > 1.0 {
        maxPressure = 1.0
    }
    
    return maxPressure
}
```

---

## 三、接口使用示例

### 3.1 在 Router 层调用

```go
// domains/streaming/executors/router.go

func (r *Router) getPressureSignals(candidate provider.Candidate) (fpPressure, limiterPressure float64) {
    ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
    defer cancel()
    
    // FpSlots 压力
    if r.FpSlotsManager != nil && candidate.FpSlotLimit != nil {
        fpPressure, _ = r.FpSlotsManager.GetPressure(
            ctx,
            candidate.CredentialID,
            *candidate.FpSlotLimit,
        )
    }
    
    // Limiter 压力
    if r.Limiter != nil {
        limiterPressure = r.Limiter.GetPressure(
            candidate.CredentialID,
            candidate.ProviderID, // poolID
            "", // identityKey（路由阶段还未分配）
        )
    }
    
    return fpPressure, limiterPressure
}
```

### 3.2 在 URSM v2 Scorer 中使用

```go
// domains/ursm/v2/scorer.go

func (s *Scorer) Score(candidate provider.Candidate) float64 {
    baseScore := s.calculateBaseScore(candidate)
    
    // 获取压力信号
    fpPressure, limiterPressure := s.router.getPressureSignals(candidate)
    
    // 计算压力惩罚
    pressurePenalty := calculatePressurePenalty(fpPressure, limiterPressure)
    
    return baseScore * (1 - pressurePenalty)
}
```

---

## 四、性能分析

### 4.1 FpSlots 压力查询

**时间复杂度**:
- Redis SCAN: O(N)，N = 槽位数量
- 典型情况：fp_slot_limit = 100，扫描耗时 ~5-10ms

**优化措施**:
- ✅ 缓存 5 秒（大幅减少 Redis 查询）
- ✅ 异步更新缓存（非阻塞路由决策）
- ✅ 批量扫描（SCAN count=100）

**预估影响**:
- 无缓存：+5-10ms/请求
- 有缓存：+0.1ms/请求（内存读取）

### 4.2 Limiter 压力查询

**时间复杂度**:
- O(1) atomic.Load() 操作
- 典型耗时：< 0.1ms

**优化措施**:
- ✅ 无需缓存（已在内存中）

**预估影响**:
- +0.1ms/请求（可忽略）

---

## 五、实施步骤

### Step 1: 实现 FpSlots GetPressure（带缓存）
- [ ] 添加 `GetPressure()` 方法到 `FingerprintSlotManager`
- [ ] 实现缓存机制（sync.Map + TTL）
- [ ] 处理 Redis 不可用的情况（返回 0）

### Step 2: 实现 Limiter GetPressure
- [ ] 添加 `GetPressure()` 方法到 `Limiter`
- [ ] 遍历 4 层 Semaphore，取最大压力
- [ ] 处理未初始化层的情况（返回 0）

### Step 3: 单元测试
- [ ] 测试 FpSlots 压力计算（模拟 Redis 数据）
- [ ] 测试 Limiter 压力计算（模拟并发占用）
- [ ] 测试缓存机制（TTL 过期）
- [ ] 测试边界条件（无限制、Redis 不可用）

### Step 4: 集成测试
- [ ] 在 Router 层调用压力查询接口
- [ ] 验证性能影响（< 1ms）
- [ ] 部署到 245 观察

---

## 六、风险和缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| Redis SCAN 性能问题 | 中 | 中 | 5秒缓存 + 异步更新 |
| 缓存过期导致压力不准 | 低 | 低 | TTL 5秒足够（路由决策周期 < 100ms） |
| Limiter 锁竞争 | 低 | 低 | RWMutex 读锁，影响极小 |
| 压力计算错误导致节点闲置 | 中 | 中 | 单元测试覆盖 + A/B 测试验证 |

---

## 七、下一步

1. **实现 GetPressure() 方法**
   - `domains/ursm/fp_slot_manager.go`
   - `domains/credential/limiter.go`

2. **编写单元测试**
   - `domains/ursm/fp_slot_manager_test.go`
   - `domains/credential/limiter_test.go`

3. **性能验证**
   - 基准测试（Benchmark）
   - 部署到 245 观察延迟

---

**文档状态**: 设计完成，准备实施  
**预估工作量**: 2-3 天  
**下一步**: 开始实现 FpSlots GetPressure() 方法

# 已知限制：跨进程状态同步

**日期**: 2026-07-03  
**影响组件**: Circuit Breaker (漏洞1) 和 Concurrency Limiter (漏洞11)  
**影响场景**: 多实例部署

---

## 问题描述

### 漏洞1: Circuit Breaker跨进程失同步

**现状**:
- `credential.Manager`（circuit breaker）使用**进程内存**(`sync.Map`)存储状态
- 状态包括：`closed/open/half_open/quarantined` + 失败计数 + 冷却时间
- 多实例部署时，每个实例维护**独立的**circuit状态

**影响**:
- 实例A触发breaker→open（5分钟冷却），但实例B仍然closed
- 实例B继续路由到该凭据，产生5xx雪崩
- 单实例部署时**无影响**

**代码位置**:
- `domains/credential/breaker.go:372-410` (Manager.breakers map)
- `domains/streaming/executors/executor.go:839` (e.Circuit.Allow)

**为什么不立即修复**:
1. 需要将breaker状态移到Redis，涉及性能权衡（每次Allow()读Redis vs 内存）
2. 需要设计状态合并策略（实例A认为open，实例B认为closed → 如何决策）
3. DB有`credentials.circuit_state`列，但目前只有bg任务写，router不读

---

### 漏洞11: Concurrency Limiter跨进程不收敛

**现状**:
- `credential.Limiter`使用**进程内存**(`sync.Map`)存储5层semaphore计数
- 多实例部署时，每个实例独立计数

**影响**:
- 单实例配置50并发限制，3实例变150并发（3×50）
- 但由于limiter只是软限制（identity/keyID层可bypass），实际影响较小

**代码位置**:
- `domains/credential/limiter.go` (全局/pool/credential层blocking，identity/keyID层soft)

---

## 缓解方案

### 短期（当前已实施）

1. **单实例部署**: 在关键业务场景使用单实例，避免跨进程问题
2. **监控告警**: 监控circuit open事件，手动干预
3. **DB状态为准**: 定期bg任务将DB的circuit_state写为closed（60s间隔）

### 中期（建议）

1. **Circuit Breaker移到Redis** (优先级: HIGH):
   ```go
   // 伪代码
   func (m *Manager) Allow(providerID, credentialID int) bool {
       key := fmt.Sprintf("circuit:%d:%d", providerID, credentialID)
       state, _ := m.redis.HGet(ctx, key, "state").Result()
       if state == "open" {
           coolingUntil, _ := m.redis.HGet(ctx, key, "cooling_until").Int64()
           if time.Now().Unix() < coolingUntil {
               return false
           }
           // 尝试进入half_open
           m.redis.HSet(ctx, key, "state", "half_open")
       }
       return state != "quarantined"
   }
   ```
   - 使用Redis Hash存储：`{state, fail_count, cooling_until}`
   - 10秒本地缓存减少Redis压力
   - 使用Lua脚本保证原子性

2. **Limiter移到Redis** (优先级: MEDIUM):
   ```go
   // 使用Redis计数器 + Lua脚本
   func (l *Limiter) AcquireCredential(credID int) bool {
       key := fmt.Sprintf("lim:cred:%d", credID)
       current := l.redis.Incr(ctx, key).Val()
       if current > limit {
           l.redis.Decr(ctx, key)
           return false
       }
       return true
   }
   ```

### 长期（架构演进）

1. **引入分布式协调**: 使用etcd/Consul存储全局状态
2. **Actor模型**: 每个credential由单一实例负责（consistent hashing）
3. **去中心化**: 通过gossip协议同步状态（类似Consul serf）

---

## 影响评估

| 场景 | 单实例 | 2-3实例 | 10+实例 |
|------|-------|---------|---------|
| Circuit不同步 | ✅ 无影响 | ⚠️  偶现雪崩 | ❌ 频繁雪崩 |
| Limiter倍增 | ✅ 无影响 | ⚠️  1.5-2x超额 | ❌ 10x超额 |
| 推荐方案 | 当前架构 | 监控+手动干预 | **必须修复** |

---

## 监控指标

部署多实例时，监控以下指标判断影响：

1. **Circuit不同步检测**:
   ```promql
   # 同一凭据在不同实例的circuit状态不一致
   count by (credential_id) (circuit_breaker_state{state="open"}) != 
   count by (credential_id) (circuit_breaker_state)
   ```

2. **Limiter超额检测**:
   ```promql
   # 凭据总并发数 > 配置限制 × 1.2
   sum by (credential_id) (credential_concurrent_requests) > 
   credential_concurrency_limit * 1.2
   ```

3. **雪崩检测**:
   ```promql
   # 5分钟内5xx率 > 30%
   rate(http_requests_total{status=~"5.."}[5m]) / 
   rate(http_requests_total[5m]) > 0.3
   ```

---

## 附录：漏洞1修复草案（未实施）

```go
// domains/credential/breaker.go

type Manager struct {
    mu       sync.RWMutex
    breakers map[string]*Breaker
    redis    *redis.Client  // 新增
    db       *pgxpool.Pool  // 新增
    cache    *sync.Map      // 10s缓存 {key: {state, expiresAt}}
}

func (m *Manager) Allow(providerID, credentialID int) bool {
    key := fmt.Sprintf("%d/%d", providerID, credentialID)
    
    // 1. 检查本地缓存（10s TTL）
    if cached, ok := m.cache.Load(key); ok {
        entry := cached.(cacheEntry)
        if time.Now().Before(entry.expiresAt) {
            return entry.state != StateOpen && entry.state != StateQuarantined
        }
    }
    
    // 2. 从Redis读取
    ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
    defer cancel()
    
    state, err := m.redis.HGet(ctx, "circuit:"+key, "state").Result()
    if err == redis.Nil {
        // Redis无数据，从DB读取
        state = m.loadFromDB(ctx, credentialID)
    }
    
    // 3. 更新本地缓存
    m.cache.Store(key, cacheEntry{
        state:     parseState(state),
        expiresAt: time.Now().Add(10 * time.Second),
    })
    
    return state != "open" && state != "quarantined"
}

func (m *Manager) RecordFailure(providerID, credentialID int, kind ErrorKind) {
    key := fmt.Sprintf("%d/%d", providerID, credentialID)
    
    // Lua脚本原子递增失败计数
    script := `
        local fails = redis.call('HINCRBY', KEYS[1], 'fail_count', 1)
        if fails >= tonumber(ARGV[1]) then
            redis.call('HSET', KEYS[1], 'state', 'open')
            redis.call('HSET', KEYS[1], 'cooling_until', ARGV[2])
        end
        return fails
    `
    
    coolingUntil := time.Now().Add(5 * time.Minute).Unix()
    m.redis.Eval(context.Background(), script, 
        []string{"circuit:" + key}, 
        circuitFailureThreshold, coolingUntil).Val()
    
    // 失效本地缓存
    m.cache.Delete(key)
}
```

**预估性能影响**:
- 无缓存命中时：增加50ms Redis查询（可接受）
- 有缓存命中时：纯内存操作（无影响）
- 10秒缓存周期：每个凭据每10秒查询1次Redis

---

**作者**: AI Agent  
**文档版本**: v1.0  
**最后更新**: 2026-07-03

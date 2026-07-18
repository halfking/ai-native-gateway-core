# 13 — 跨进程 RPM 限流实施方案

> 日期：2026-07-18
> 版本：v1.0
> 状态：实施中
> 前置：12-二次审计报告.md 审计 2

---

## 1. 问题

**当前状态**（commit 6f3e405db）：
- RPM 限流基于进程内存 `map[string]*rpmWindow`
- 单实例部署准确
- 多实例（K8s 3 副本）实际 RPM = limit × 3
- 水平扩容导致 RPM 线性增长

**目标**：
- 实现跨网关实例的全局 RPM 限流
- 保持单实例内存限流作为降级策略
- 性能损耗 ≤ 2ms P99
- 100% 向后兼容

---

## 2. 技术方案

### 2.1 架构设计

```
                    ┌─────────────────┐
                    │  Limiter (Go)   │
                    └────────┬────────┘
                             │
              ┌──────────────┴──────────────┐
              │                             │
    ┌─────────▼─────────┐       ┌──────────▼─────────┐
    │ MemoryRPMLimiter  │       │  RedisRPMLimiter   │
    │  (Fallback)       │       │   (Primary)        │
    └───────────────────┘       └──────────┬─────────┘
                                           │
                                  ┌────────▼────────┐
                                  │  Redis Cluster  │
                                  │  (Lua Script)   │
                                  └─────────────────┘
```

### 2.2 Redis 滑动窗口 Lua 脚本

**核心逻辑**：
1. 使用 ZSET 存储时间戳（member = timestamp, score = timestamp）
2. `ZREMRANGEBYSCORE` 删除 60s 前的旧记录
3. `ZCARD` 计数当前窗口内的请求数
4. 如果未超限，`ZADD` 添加当前时间戳
5. `EXPIRE` 设置 key 过期时间 65s（60s 窗口 + 5s 余量）

**Lua 脚本**：

```lua
-- rpm_sliding_window.lua
-- KEYS[1] = "rpm:providerID:credentialID"
-- ARGV[1] = limit (int)
-- ARGV[2] = now (unix seconds, float)
-- ARGV[3] = window_seconds (default 60)

local key = KEYS[1]
local limit = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local window = tonumber(ARGV[3])

-- 1. 删除窗口外的旧记录
local cutoff = now - window
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)

-- 2. 计数当前窗口内的请求数
local count = redis.call('ZCARD', key)

-- 3. 判断是否超限
if count >= limit then
    -- 超限，返回当前计数
    return {0, count}
end

-- 4. 未超限，添加当前时间戳
-- member 使用 now + 微秒随机数避免冲突
local member = string.format("%.6f:%s", now, redis.call('TIME')[2])
redis.call('ZADD', key, now, member)

-- 5. 设置过期时间（窗口 + 5s 余量）
redis.call('EXPIRE', key, window + 5)

-- 返回 {允许, 新计数}
return {1, count + 1}
```

**返回值**：
- `{1, count}` — 允许，count 为预留后的总数
- `{0, count}` — 拒绝，count 为当前窗口内总数

### 2.3 Go 接口设计

```go
// RPMLimiter 是 RPM 限流器的抽象接口
type RPMLimiter interface {
    // CheckAndReserve 检查并预留一个 RPM slot
    // 返回 (允许, 当前计数, error)
    CheckAndReserve(ctx context.Context, providerID, credentialID int, limit int) (bool, int, error)
}

// MemoryRPMLimiter 是现有的进程内存实现
type MemoryRPMLimiter struct {
    credsRPM map[string]*rpmWindow
    mu       sync.Mutex
}

// RedisRPMLimiter 是新的跨进程实现
type RedisRPMLimiter struct {
    client    *redis.Client
    luaScript *redis.Script
    fallback  *MemoryRPMLimiter  // Redis 不可用时降级
}
```

### 2.4 双模式策略

```go
// Limiter 根据配置选择 RPM 实现
type Limiter struct {
    // ... 现有字段 ...
    
    rpmLimiter RPMLimiter  // 统一接口
}

// 初始化时根据环境变量选择
func NewLimiter(...) *Limiter {
    var rpmLimiter RPMLimiter
    
    if redisURL := os.Getenv("RPM_REDIS_URL"); redisURL != "" {
        // 跨进程模式
        rpmLimiter = NewRedisRPMLimiter(redisURL)
    } else {
        // 单实例模式（默认）
        rpmLimiter = NewMemoryRPMLimiter()
    }
    
    return &Limiter{
        rpmLimiter: rpmLimiter,
        // ...
    }
}
```

---

## 3. 性能分析

### 3.1 延迟预估

| 操作 | 延迟 | 说明 |
|------|------|------|
| **内存限流** | ~100ns | 本地 map + mutex |
| **Redis Lua（本地）** | ~0.5ms | 同机房 Redis |
| **Redis Lua（远程）** | ~2ms | 跨 AZ Redis Cluster |

### 3.2 吞吐量预估

**单 Redis 实例**：
- 理论 QPS：~50,000（Lua 脚本执行）
- 实际可用：~30,000（考虑网络开销）
- 网关需求：~10,000 QPS（3 副本 × 3,333 QPS/副本）
- **结论**：单 Redis 实例足够

**Redis Cluster**（如需扩展）：
- 通过 hash slot 分片
- Key 格式：`rpm:{providerID}:{credentialID}` 保证同 credential 同 slot

### 3.3 降级策略

```go
func (r *RedisRPMLimiter) CheckAndReserve(ctx context.Context, providerID, credentialID int, limit int) (bool, int, error) {
    // 1. 尝试 Redis
    allowed, count, err := r.checkRedis(ctx, providerID, credentialID, limit)
    if err == nil {
        return allowed, count, nil
    }
    
    // 2. Redis 失败，降级到内存限流
    slog.Warn("redis rpm limiter failed, fallback to memory",
        "error", err,
        "provider_id", providerID,
        "credential_id", credentialID,
    )
    
    return r.fallback.CheckAndReserve(ctx, providerID, credentialID, limit)
}
```

---

## 4. 配置与部署

### 4.1 环境变量

```bash
# 跨进程 RPM（可选，默认单实例内存限流）
export RPM_REDIS_URL="redis://localhost:6379/2"

# Redis 连接池配置
export RPM_REDIS_POOL_SIZE=10
export RPM_REDIS_TIMEOUT=100ms

# Redis 不可用时的降级行为
export RPM_REDIS_FALLBACK=memory  # memory | fail-open
```

### 4.2 灰度部署策略

**Phase 1**（本地验证）：
1. 单实例部署，`RPM_REDIS_URL` 指向本地 Redis
2. 运行集成测试验证正确性
3. 压测验证性能（对比内存模式）

**Phase 2**（kaixuan-1 测试）：
1. 部署单副本，开启 Redis RPM
2. 观察 7 天，监控延迟 + 错误率
3. 对比 Redis 模式 vs 内存模式的 RPM 准确性

**Phase 3**（245 预生产）：
1. 部署 3 副本，开启 Redis RPM
2. 验证多实例场景 RPM = limit（不再是 limit × 3）
3. 压测验证多实例负载均衡

**Phase 4**（154 生产）：
1. 灰度 50% 流量（单个 provider 维度）
2. 观察 3 天，确认无异常
3. 全量切换

### 4.3 回滚方案

**紧急回滚**（< 5 分钟）：
```bash
# 移除环境变量，重启服务即回退到内存模式
unset RPM_REDIS_URL
systemctl restart llm-gateway-go
```

**降级回滚**（不重启）：
- Redis 自动降级到内存限流（已内置）
- 监控告警触发后人工介入

---

## 5. 监控与告警

### 5.1 Prometheus 指标

```go
// 新增指标
var (
    rpmLimiterMode = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llmgw_rpm_limiter_mode",
            Help: "RPM limiter mode (0=memory, 1=redis)",
        },
        []string{"mode"},
    )
    
    rpmRedisLatency = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "llmgw_rpm_redis_duration_seconds",
            Help: "Redis RPM check latency",
            Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05},
        },
        []string{"result"}, // "allowed" | "denied" | "error"
    )
    
    rpmRedisFallback = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llmgw_rpm_redis_fallback_total",
            Help: "Redis RPM fallback to memory count",
        },
        []string{"reason"}, // "timeout" | "connection" | "script_error"
    )
)
```

### 5.2 告警规则

```yaml
# prometheus/alerts/rpm.yml
groups:
  - name: rpm_limiter
    interval: 30s
    rules:
      - alert: RPMRedisHighLatency
        expr: histogram_quantile(0.99, rate(llmgw_rpm_redis_duration_seconds_bucket[5m])) > 0.005
        for: 5m
        annotations:
          summary: "RPM Redis P99 延迟 > 5ms"
          
      - alert: RPMRedisFallbackHigh
        expr: rate(llmgw_rpm_redis_fallback_total[5m]) > 10
        for: 2m
        annotations:
          summary: "Redis RPM 降级频繁（> 10/s）"
```

---

## 6. 测试计划

### 6.1 单元测试

- `TestRedisRPMLimiter_BasicWindow` — 基础 60s 窗口
- `TestRedisRPMLimiter_Concurrent` — 并发安全
- `TestRedisRPMLimiter_Fallback` — 降级逻辑
- `TestRedisRPMLimiter_MultiInstance` — 模拟多实例（3 个 client）

### 6.2 集成测试

- `TestRedisRPM_Integration` — 真实 Redis 实例
- `TestRedisRPM_MultiGateway` — 3 个 Limiter 实例共享 Redis

### 6.3 性能测试

```bash
# Benchmark 对比
go test -bench=BenchmarkRPM -benchtime=10s ./domains/credential

# 预期结果
# BenchmarkRPM/Memory-8    10000000    120 ns/op
# BenchmarkRPM/Redis-8      1000000   1500 ns/op  (1.5ms)
```

---

## 7. 风险与缓解

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|---------|
| Redis 单点故障 | 全部降级到内存限流 | 低 | 1. 自动降级 2. Redis 主从 3. 监控告警 |
| 延迟增加影响体验 | P99 从 100ns → 2ms | 中 | 1. 异步预留 2. 批量检查 3. 本地缓存 |
| Lua 脚本 bug | RPM 计数错误 | 低 | 1. 充分单测 2. 灰度验证 3. 快速回滚 |
| Key 过期清理不及时 | Redis 内存增长 | 低 | 1. 每个 key 带 EXPIRE 2. 监控 key 数量 |

---

## 8. 实施清单

- [ ] 编写 `rpm_sliding_window.lua` 脚本
- [ ] 实现 `RPMLimiter` 接口
- [ ] 实现 `RedisRPMLimiter` + 降级逻辑
- [ ] 重构 `MemoryRPMLimiter` 为独立实现
- [ ] 修改 `Limiter.CheckCredentialRPM` 委托给 `rpmLimiter`
- [ ] 编写单元测试（5 个）
- [ ] 编写集成测试（2 个）
- [ ] 编写性能对比测试
- [ ] 增加 Prometheus 指标
- [ ] 更新 `11-实现审计与整改边界.md`
- [ ] 部署到 kaixuan-1 验证

---

## 9. 下一步

**Phase 2 优化**（v2.0）：
- Provider-specific 窗口配置（`rpm_window_seconds` 字段）
- 解析 429 `Retry-After` 头动态调整窗口
- 批量 RPM 检查（pipeline 优化）
- 本地 LRU 缓存减少 Redis 调用

---

**审计人**: ACC Agent (claude-opus-4)  
**实施时间**: 2026-07-18  
**依据**: 12-二次审计报告.md 审计维度 2

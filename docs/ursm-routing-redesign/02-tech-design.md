# URSM 技术设计文档

## 1. 架构概览

### 1.1 系统分层

```
┌─────────────────────────────────────────────────────────────┐
│                      API Layer                              │
│  RecordRequest / UpdateProvider / UpdateCredential          │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│                   URSM Manager                              │
│  ┌─────────────┬──────────────┬──────────────┬───────────┐ │
│  │ Provider    │ Credential   │ Model        │ Node      │ │
│  │ Cache       │ Cache        │ Cache        │ Cache     │ │
│  └─────────────┴──────────────┴──────────────┴───────────┘ │
│  ┌─────────────┬──────────────┬──────────────────────────┐ │
│  │ FpSlot Mgr  │ Conc Mgr     │ Cost Scorer              │ │
│  └─────────────┴──────────────┴──────────────────────────┘ │
│  ┌─────────────────────────────────────────────────────────┤
│  │ Batch Writer (原子更新 + 级联传播)                       │
│  └─────────────────────────────────────────────────────────┘
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│                   Storage Layer                             │
│  ┌───────────┬───────────┬─────────────────────────────┐   │
│  │ Memory    │ Redis     │ PostgreSQL                  │   │
│  │ (10s TTL) │ (5min TTL)│ (权威源)                    │   │
│  └───────────┴───────────┴─────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

### 1.2 核心组件

#### Manager
- **职责**: 统一状态管理入口
- **依赖**: DB, Redis, 四层缓存, 资源管理器
- **接口**: 提供状态查询和更新API

#### LayerCache[T]
- **职责**: 三层缓存（mem/redis/db）
- **泛型**: 支持Provider/Credential/Model/Node四种状态
- **策略**: L1命中率>80%, L2命中率>95%

#### FingerprintSlotManager
- **职责**: 管理指纹槽的获取、释放、抢占
- **策略**: Pin复用 → 空闲槽 → LRU抢占
- **存储**: Redis (Lua脚本保证原子性)

#### ConcurrencySlotManager
- **职责**: 管理并发槽的获取、释放
- **策略**: 全局计数 + 会话计数
- **存储**: Redis (Lua脚本保证原子性)

#### CostScorer
- **职责**: 计算节点的综合成本分数
- **权重**: 价格30% + 速度40% + 稳定性30%
- **输出**: 0-100分，越高越优

#### BatchWriter
- **职责**: 批量原子更新 + 级联传播
- **策略**: 按层级排序 → 事务写入 → 异步缓存失效
- **保证**: 原子性（全成功或全失败）

---

## 2. 数据结构设计

### 2.1 状态结构

#### ProviderState
```go
type ProviderState struct {
    ProviderID      int       `json:"provider_id"`
    Enabled         bool      `json:"enabled"`
    ManualDisabled  bool      `json:"manual_disabled"`
    DisplayName     string    `json:"display_name"`
    UpdatedAt       time.Time `json:"updated_at"`
}

func (s *ProviderState) IsAvailable() bool {
    return s.Enabled && !s.ManualDisabled
}
```

#### CredentialState
```go
type CredentialState struct {
    CredentialID       int       `json:"credential_id"`
    ProviderID         int       `json:"provider_id"`
    Status             string    `json:"status"`              // active/inactive
    LifecycleStatus    string    `json:"lifecycle_status"`    // active/retired
    AvailabilityState  string    `json:"availability_state"`  // ready/degraded/...
    HealthStatus       string    `json:"health_status"`       // healthy/warning/...
    QuotaState         string    `json:"quota_state"`         // ok/exhausted/...
    ManualDisabled     bool      `json:"manual_disabled"`
    ConsecutiveFailures int      `json:"consecutive_failures"`
    RecoverAt          *time.Time `json:"recover_at,omitempty"`
    UpdatedAt          time.Time `json:"updated_at"`
}

func (s *CredentialState) IsAvailable() bool {
    return s.Status == "active" &&
           s.LifecycleStatus == "active" &&
           !s.ManualDisabled &&
           s.AvailabilityState != "auth_failed" &&
           s.AvailabilityState != "suspended" &&
           s.QuotaState != "permanently_exhausted" &&
           s.QuotaState != "balance_exhausted"
}

func (s *CredentialState) UnavailableReason() string {
    if s.Status != "active" {
        return "credential_inactive"
    }
    if s.LifecycleStatus != "active" {
        return "credential_retired"
    }
    if s.ManualDisabled {
        return "credential_manual_disabled"
    }
    if s.AvailabilityState == "auth_failed" {
        return "credential_auth_failed"
    }
    if s.AvailabilityState == "suspended" {
        return "credential_suspended"
    }
    if s.QuotaState == "permanently_exhausted" || s.QuotaState == "balance_exhausted" {
        return "credential_quota_exhausted"
    }
    return "credential_unavailable"
}
```

#### ModelState
```go
type ModelState struct {
    CredentialID     int       `json:"credential_id"`
    RawModel         string    `json:"raw_model"`
    OfferAvailable   bool      `json:"offer_available"`
    OfferReason      string    `json:"offer_reason,omitempty"`
    BindingAvailable bool      `json:"binding_available"`
    BindingReason    string    `json:"binding_reason,omitempty"`
    ProbeState       string    `json:"probe_state"` // unknown/recovering/healthy_confirmed/broken_confirmed
    UpdatedAt        time.Time `json:"updated_at"`
}

func (s *ModelState) IsAvailable() bool {
    return s.OfferAvailable && 
           s.BindingAvailable && 
           s.ProbeState != "broken_confirmed"
}

func (s *ModelState) UnavailableReason() string {
    if !s.OfferAvailable {
        return "model_offer_unavailable: " + s.OfferReason
    }
    if !s.BindingAvailable {
        return "model_binding_unavailable: " + s.BindingReason
    }
    if s.ProbeState == "broken_confirmed" {
        return "model_broken"
    }
    return "model_unavailable"
}
```

#### NodeState
```go
type NodeState struct {
    CredentialID        int        `json:"credential_id"`
    RawModel            string     `json:"raw_model"`
    ConsecutiveFailures int        `json:"consecutive_failures"`
    SuccessCount        int64      `json:"success_count"`
    FailureCount        int64      `json:"failure_count"`
    LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
    LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
    Disabled            bool       `json:"disabled"`
    DisabledUntil       *time.Time `json:"disabled_until,omitempty"`
    RecoverAt           *time.Time `json:"recover_at,omitempty"`
    LastError           string     `json:"last_error,omitempty"`
    UpdatedAt           time.Time  `json:"updated_at"`
}

func (s *NodeState) IsAvailable() bool {
    if s.ConsecutiveFailures >= 3 {
        return false
    }
    if s.Disabled && s.DisabledUntil != nil && time.Now().Before(*s.DisabledUntil) {
        return false
    }
    return true
}

func (s *NodeState) UnavailableReason() string {
    if s.ConsecutiveFailures >= 3 {
        return fmt.Sprintf("node_consecutive_failures: %d", s.ConsecutiveFailures)
    }
    if s.Disabled {
        return "node_disabled"
    }
    return "node_unavailable"
}
```

#### RouteNode
```go
type RouteNode struct {
    // 标识
    CredentialID int    `json:"credential_id"`
    RawModel     string `json:"raw_model"`
    ProviderID   int    `json:"provider_id"`
    ProviderName string `json:"provider_name"`
    
    // 状态（查询时填充）
    Available         bool   `json:"available"`
    UnavailableReason string `json:"unavailable_reason,omitempty"`
    HealthStatus      string `json:"health_status"`
    
    // 资源（路由时分配）
    FpSlotIndex     int  `json:"fp_slot_index"`
    ConcurrencyHeld bool `json:"concurrency_held"`
    
    // 成本因素（用于排序）
    PriceInPer1M    float64 `json:"price_in_per_1m"`
    PriceOutPer1M   float64 `json:"price_out_per_1m"`
    Currency        string  `json:"currency"`
    SuccessRate     float64 `json:"success_rate"`
    P95LatencyMs    int     `json:"p95_latency_ms"`
    CompositeScore  float64 `json:"composite_score"`
    
    // 限额配置
    FpSlotLimit      int `json:"fp_slot_limit"`
    ConcurrencyLimit int `json:"concurrency_limit"`
}
```

### 2.2 缓存Key设计

```go
func providerKey(providerID int) string {
    return fmt.Sprintf("ursm:provider:%d", providerID)
}

func credentialKey(credentialID int) string {
    return fmt.Sprintf("ursm:credential:%d", credentialID)
}

func modelKey(credentialID int, model string) string {
    return fmt.Sprintf("ursm:model:%d:%s", credentialID, model)
}

func nodeKey(credentialID int, model string) string {
    return fmt.Sprintf("ursm:node:%d:%s", credentialID, model)
}

// 指纹槽
func fpSlotKey(credentialID, slotIndex int) string {
    return fmt.Sprintf("llmgw:cred_fp_slot:%d:%d", credentialID, slotIndex)
}

func fpPinKey(sessionID string, credentialID int) string {
    return fmt.Sprintf("llmgw:sess_cred_fp:%s:%d", sessionID, credentialID)
}

// 并发槽
func concSlotKey(credentialID int) string {
    return fmt.Sprintf("llmgw:conc_slot:%d", credentialID)
}

func concSessionKey(credentialID int, sessionID string) string {
    return fmt.Sprintf("llmgw:conc_session:%d:%s", credentialID, sessionID)
}
```

---

## 3. 核心算法

### 3.1 四层级联可用性检查

```go
func (m *Manager) IsAvailable(ctx context.Context, credentialID int, model string) (bool, string) {
    // Layer 1: Provider
    cred, err := m.credentialCache.Get(ctx, credentialKey(credentialID))
    if err != nil {
        return true, "" // fail-open
    }
    
    provider, err := m.providerCache.Get(ctx, providerKey(cred.ProviderID))
    if err != nil {
        return true, ""
    }
    
    if !provider.IsAvailable() {
        return false, "provider_disabled"
    }
    
    // Layer 2: Credential
    if !cred.IsAvailable() {
        return false, cred.UnavailableReason()
    }
    
    // Layer 3: Model
    modelState, err := m.modelCache.Get(ctx, modelKey(credentialID, model))
    if err != nil {
        return true, ""
    }
    
    if !modelState.IsAvailable() {
        return false, modelState.UnavailableReason()
    }
    
    // Layer 4: Node
    nodeState, err := m.nodeCache.Get(ctx, nodeKey(credentialID, model))
    if err != nil {
        return true, ""
    }
    
    if !nodeState.IsAvailable() {
        return false, nodeState.UnavailableReason()
    }
    
    return true, ""
}
```

### 3.2 指纹槽LRU抢占

```redis
-- acquireLRUScript
local prefix = KEYS[1]  -- llmgw:cred_fp_slot:{credentialID}
local limit  = tonumber(ARGV[1])
local holder = ARGV[2]
local slotTTL = tonumber(ARGV[3])
local pinTTL  = tonumber(ARGV[4])
local gate    = tonumber(ARGV[5])  -- 300秒（5分钟）
local pinKey  = ARGV[6]
local credID  = tonumber(ARGV[7])

local bestSlot = -1
local bestIdle = -1
local bestOldHolder = nil

for slot = 0, limit - 1 do
    local key = prefix .. ':' .. tostring(slot)
    local current = redis.call('GET', key)
    
    if current == false then
        -- 空闲槽，直接获取
        redis.call('SET', key, holder, 'EX', slotTTL)
        if pinKey ~= '' then
            redis.call('SET', pinKey, tostring(slot), 'EX', pinTTL)
        end
        return {1, slot, ''}
    end
    
    if current == holder then
        -- 已持有，刷新TTL
        redis.call('EXPIRE', key, slotTTL)
        if pinKey ~= '' then
            redis.call('SET', pinKey, tostring(slot), 'EX', pinTTL)
        end
        return {1, slot, ''}
    end
    
    local remaining = redis.call('TTL', key)
    if remaining == -1 or remaining == -2 then
        -- 无TTL，直接获取
        redis.call('SET', key, holder, 'EX', slotTTL)
        if pinKey ~= '' then
            redis.call('SET', pinKey, tostring(slot), 'EX', pinTTL)
        end
        return {1, slot, ''}
    end
    
    local idle = slotTTL - remaining
    if idle >= gate and idle > bestIdle then
        -- 超过活跃阈值，且空闲时间最长
        bestSlot = slot
        bestIdle = idle
        bestOldHolder = current
    end
end

if bestSlot == -1 then
    return {0, '', ''}
end

-- 抢占LRU槽
local bestKey = prefix .. ':' .. tostring(bestSlot)
redis.call('SET', bestKey, holder, 'EX', slotTTL)
if pinKey ~= '' then
    redis.call('SET', pinKey, tostring(bestSlot), 'EX', pinTTL)
end

-- 删除旧holder的pin
if bestOldHolder then
    local oldPinKey = 'llmgw:sess_cred_fp:' .. bestOldHolder .. ':' .. tostring(credID)
    if redis.call('GET', oldPinKey) == tostring(bestSlot) then
        redis.call('DEL', oldPinKey)
    end
end

return {1, bestSlot, bestOldHolder or ''}
```

### 3.3 并发槽原子获取

```redis
-- acquireConcurrencyScript
local limKey = KEYS[1]   -- llmgw:conc_slot:{credentialID}
local sessKey = KEYS[2]  -- llmgw:conc_session:{credentialID}:{sessionID}
local limit = tonumber(ARGV[1])
local sessionID = ARGV[2]

local current = tonumber(redis.call('GET', limKey) or '0')

if current >= limit then
    return 0
end

redis.call('INCR', limKey)
redis.call('EXPIRE', limKey, 300)

redis.call('INCR', sessKey)
redis.call('EXPIRE', sessKey, 300)

return 1
```

### 3.4 成本评分算法

```go
func (s *CostScorer) CalculateCompositeScore(node RouteNode) float64 {
    // 价格分数 (0-100, 越低越好)
    priceScore := s.calculatePriceScore(node)
    
    // 速度分数 (0-100, 越高越好)
    speedScore := s.calculateSpeedScore(node)
    
    // 稳定性分数 (0-100, 越高越好)
    stabilityScore := s.calculateStabilityScore(node)
    
    // 综合分数 (0-100, 越高越好)
    composite := 
        s.weights.PriceWeight * (100 - priceScore) +  // 价格反转
        s.weights.SpeedWeight * speedScore +
        s.weights.StabilityWeight * stabilityScore
    
    return composite
}

func (s *CostScorer) calculatePriceScore(node RouteNode) float64 {
    avgPrice := (node.PriceInPer1M + node.PriceOutPer1M) / 2
    if avgPrice == 0 {
        return 0  // 免费最优
    }
    // 归一化到0-100（假设$50/1M为中位数）
    normalized := (avgPrice / 50.0) * 100
    return math.Min(100, normalized)
}

func (s *CostScorer) calculateSpeedScore(node RouteNode) float64 {
    // P95延迟: 100ms=100分, __PORT_8__ms=0分
    if node.P95LatencyMs <= 100 {
        return 100
    }
    if node.P95LatencyMs >= __PORT_8__ {
        return 0
    }
    score := 100 - float64(node.P95LatencyMs - 100) / 49.0
    return math.Max(0, score)
}

func (s *CostScorer) calculateStabilityScore(node RouteNode) float64 {
    return node.SuccessRate * 100
}
```

### 3.5 批量原子更新

```go
func (w *BatchWriter) ApplyUpdates(ctx context.Context, updates []StateUpdate) error {
    // 1. 开启事务
    tx, err := w.db.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx)
    
    // 2. 按层级排序
    sorted := sortUpdatesByLayer(updates)
    
    // 3. 逐层应用
    for _, update := range sorted {
        switch update.Layer {
        case LayerProvider:
            if err := w.applyProviderUpdate(ctx, tx, update); err != nil {
                return err
            }
        case LayerCredential:
            if err := w.applyCredentialUpdate(ctx, tx, update); err != nil {
                return err
            }
        case LayerModel:
            if err := w.applyModelUpdate(ctx, tx, update); err != nil {
                return err
            }
        case LayerNode:
            if err := w.applyNodeUpdate(ctx, tx, update); err != nil {
                return err
            }
        }
    }
    
    // 4. 提交事务
    if err := tx.Commit(ctx); err != nil {
        return err
    }
    
    // 5. 异步失效缓存
    go w.invalidateCaches(ctx, sorted)
    
    return nil
}

func sortUpdatesByLayer(updates []StateUpdate) []StateUpdate {
    sorted := make([]StateUpdate, len(updates))
    copy(sorted, updates)
    
    sort.SliceStable(sorted, func(i, j int) bool {
        return sorted[i].Layer < sorted[j].Layer
    })
    
    return sorted
}
```

---

## 4. 并发控制

### 4.1 锁策略

- **Provider更新**: 使用DB行锁（FOR UPDATE）
- **Credential更新**: 使用DB行锁（FOR UPDATE）
- **指纹槽获取**: Redis Lua脚本原子操作
- **并发槽获取**: Redis Lua脚本原子操作
- **缓存读写**: sync.Map无锁读，写时复制

### 4.2 死锁预防

按固定顺序获取锁：Provider → Credential → Model → Node

### 4.3 超时控制

- API调用超时: 5秒
- 缓存查询超时: 100ms (mem 10ms, redis 50ms, db 100ms)
- Redis Lua脚本超时: 50ms
- DB事务超时: 5秒

---

## 5. 性能优化

### 5.1 缓存策略

```
L1 (Memory):
  - TTL: 10秒
  - 容量: 10000条
  - 命中率目标: >80%
  
L2 (Redis):
  - TTL: 5分钟
  - 容量: 无限制
  - 命中率目标: >95%
  
L3 (PostgreSQL):
  - 权威源
  - 索引覆盖
```

### 5.2 批量操作

- 批量加载候选节点: 单次SQL查询
- 批量失效缓存: Pipeline操作
- 批量更新状态: 事务批处理

### 5.3 连接池

- DB连接池: 50 (max)
- Redis连接池: 100 (max)

### 5.4 异步操作

- 缓存失效: 异步goroutine
- 探测触发: 异步channel
- 审计日志: 异步批量写入

---

## 6. 容错设计

### 6.1 Fail-Open原则

所有查询失败时返回 `(true, "")` 而不是阻塞请求。

### 6.2 降级策略

```
Redis宕机 → 直接查DB
DB慢查询 → 使用过期缓存
缓存全失效 → 允许通过（fail-open）
```

### 6.3 熔断器

- 探测器连续失败10次 → 暂停探测1小时
- Redis连续超时50次 → 降级到DB only

### 6.4 数据一致性

- 使用DB事务保证原子性
- 缓存失效采用异步策略，允许短暂不一致
- 最终一致性：缓存TTL内收敛

---

## 7. 监控与告警

### 7.1 关键指标

```
# 性能
ursm_routing_decision_duration_seconds (P50/P95/P99)
ursm_cache_hit_ratio{layer,level}
ursm_db_query_duration_seconds

# 资源
ursm_fp_slot_usage_ratio{credential_id}
ursm_conc_slot_usage_ratio{credential_id}

# 错误
ursm_state_update_errors_total{layer}
ursm_routing_decision_errors_total{reason}
```

### 7.2 告警规则

```
# P0告警
- ursm_routing_decision_errors_total > 100/min
- ursm_cache_hit_ratio < 0.5 (持续5分钟)
- ursm_db_query_duration_seconds P99 > 1s

# P1告警
- ursm_fp_slot_saturation_total > 1000/min
- ursm_state_update_errors_total > 10/min
```

---

## 8. 安全性

### 8.1 输入验证

所有API参数使用 `go-playground/validator` 验证。

### 8.2 SQL注入防护

使用参数化查询，禁止字符串拼接SQL。

### 8.3 权限控制

- RecordRequest: 仅内部服务（Bearer Token）
- UpdateProvider/Credential: superAdmin角色
- RecordProbeResult: 探测器专用Token

### 8.4 审计日志

所有状态修改记录到 `routing_audit_log` 表，保留90天。

---

## 9. 测试策略

### 9.1 单元测试

- 覆盖率目标: 90%
- 测试框架: testify/assert
- Mock工具: gomock

### 9.2 集成测试

- 完整路由流程测试
- 状态级联更新测试
- 资源抢占测试

### 9.3 压力测试

- 目标QPS: 10k
- P99延迟: <50ms
- 并发连接: 1000

### 9.4 故障注入

- Redis宕机
- DB慢查询
- 网络分区

---

## 10. 部署架构

### 10.1 服务拓扑

```
┌─────────────┐
│   Gateway   │ (llm-gateway-go)
│   + URSM    │
└─────────────┘
      ↓ ↓ ↓
┌─────┴─┴─┴─────┐
│   Redis       │ (缓存 + 资源锁)
└───────────────┘
      ↓ ↓ ↓
┌─────┴─┴─┴─────┐
│  PostgreSQL   │ (权威数据源)
└───────────────┘
```

### 10.2 扩展性

- 水平扩展: Gateway无状态，可多实例部署
- Redis: 使用Redis Cluster
- DB: 使用Citus分布式PostgreSQL

---

## 11. 迁移计划

见 [05-migration-guide.md](./05-migration-guide.md)

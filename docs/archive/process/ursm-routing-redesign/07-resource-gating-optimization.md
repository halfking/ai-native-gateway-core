# Resource Gating Optimization Plan — URSM Phase 4

> ⚠️ 本文档已被 `08-unified-resource-pipeline.md` 取代为统一方案。
> 保留作为 Phase 3 的历史记录, 新设计以 08 为准。

> 本文档基于 2026-07-13 全量代码审计。
> 覆盖 FP slot、Concurrency slot、API Key 限流三大子系统的现状分析与优化设计。

> 前置文档: `06-state-optimization-plan.md`（状态管理优化） → `08-unified-resource-pipeline.md`（统一方案）

## 1. 现状全景

### 1.1 三层资源门禁

```
客户端请求
  │
  ├─► Layer 0: 全局 RPM Gate (ratelimit/gate.go)
  │     开关: rate_limit.enabled (settings_kv, 热加载)
  │     关闭后: 跳过以下所有限流
  │
  ├─► Layer 1: API Key 限流
  │     ├── RPM: Redis 滑动窗口 (ratelimit/redis_sliding.go)
  │     │    键: rl:rpm:{keyID}, 窗口: 60s
  │     │    配置: api_keys.rate_limit_rpm (DB, 可空 → 层级默认值)
  │     │    层级默认值: system=300, production=60, default=12, applicant=6
  │     └── TPM: 同上 (rl:tpm:{keyID})
  │
  ├─► Layer 2: 凭据并发限制 (Limiter 5 层)
  │     Global(1000) → Pool(100) → Credential(50) → Identity(10) → Key(DB)
  │     acquireWaitTimeout=5s, 软上限非阻塞
  │
  ├─► Layer 3: FP Slot (指纹槽)
  │     凭据级虚拟身份池, 默认 20 槽
  │     Key: llmgw:cred_fp_slot:{credID}:{slotIndex} (TTL 30min)
  │     Pin: llmgw:sess_cred_fp:{holder}:{credID} (TTL 24h)
  │     ActiveGate: 5min 内不可抢占
  │     降级: 全部饱和 → fpSlotDegraded=true → 跳过槽检查
  │
  └─► Layer 4: Circuit Breaker
        Per-credential 状态机
        Close → (fail ≥ 2) → Open(cooling) → (probe success) → HalfOpen → Close
```

### 1.2 当前痛点

| # | 问题 | 影响 | 涉及模块 |
|---|------|------|---------|
| 1 | **FP slot 无会话时长跟踪** — 无法知道槽被占用了多久、上次活跃时间 | 运维排障困难, 无法判断槽是否"死锁" | credentialfpslot/slot.go |
| 2 | **Concurrency slot 无自动超时回收** — 只有 5min TTL 兜底, 没有显式的"请求超时自动归还"机制 | 极端情况下并发计数器泄漏 | ursm/conc_slot_manager.go |
| 3 | **提供者级别无独立开关** — `rate_limit.enabled` 全局控制, 无法针对单个 provider 关闭 FP 或并发限制 | 无法灰度切换、无法为特定供应商绕过限制 | settings/, credentialfpslot/ |
| 4 | **API Key 并发限制缺失** — Limiter 5 层的 Key 层是**非阻塞软上限**, 且不检查 `rate_limit_concurrent` | 单一 API Key 可以打满所有凭据并发 | credential/limiter.go |
| 5 | **两套 FP slot 实现** — `credentialfpslot/` (993 行, 生产) vs `ursm/fp_slot_manager.go` (168 行, 备用) | 维护成本翻倍, 配置漂移 (默认限制 20 vs 5) | credentialfpslot/, ursm/ |
| 6 | **Pin TTL 过长** — 24h 的 pin 周期远超会话生命周期 | 会话结束后 pin 仍残留, 导致下次请求误绑旧凭据 | credentialfpslot/slot.go |
| 7 | **降级路径不完整** — fpSlotDegraded 后跳过槽, 但不记录持续时间、不限制降级请求的并发 | 降级时无保护, 可能打垮凭据 | executor.go |

### 1.3 当前数据流时序

```
executor.go 候选循环:

  1. pre-filter: RoutingEligible (hasPin || freeCount > 0)
     → 全淘汰时 fpSlotDegraded=true

  2. FpSlots.Acquire(credID, limit, holder)
     ├─ Pin 命中: refresh TTL (slot 30min, pin 24h)
     └─ Pin 未命中:
          ├─ 有空槽 → 获取 + 设置 pin
          └─ 全满:
               ├─ 有槽空闲 ≥ 5min → LRU 抢占
               └─ 全部活跃 → ErrSlotSaturated
                    ├─ fpSlotDegraded? → 跳过 (lease=nil)
                    └─ !fpSlotDegraded? → 跳过此候选

  3. Limiter.AcquireAll(5 层)
     ├─ Global(1000) → Pool(100) → Credential(50) → Identity(10) → Key(DB)
     ├─ 阻塞等待 (5s timeout)
     └─ 非阻塞层超限仅 log, 不拒绝

  4. execute upstream

  defer:
     release()      → Limiter 释放 (DECR 各层)
     releaseFpLease → 异步释放槽 (刷新 TTL, 不删除)
```

## 2. 目标架构

### 2.1 核心变更

```
优化前:                                优化后:

三层独立门禁:                           统一门禁管线:
  API Key RPM/TPM                        API Key RPM/TPM + Concurrent
  + 5 层 Limiter                         + FP Slot (with duration)
  + FP Slot (无时长)                     + Concurrency Slot (with auto-timeout)
  + Circuit Breaker                       + Circuit Breaker
                                          + Provider-level switches
                                          + API Key concurrent limit (阻塞)
```

### 2.2 端到端流程（优化后）

```
客户端请求
  │
  ├─ 1. API Key RPM Check (Redis 滑动窗口)
  │     超出 → 429 Too Many Requests
  │
  ├─ 2. API Key Concurrent Check (新增, Redis 计数器)
  │     超出 → 429 (阻塞硬上限)
  │
  ├─ 3. Provider FP Slot Check (可开关)
  │     disabled → 跳过
  │     ├─ 3a. Pin 重用 → 记录 last_active_at
  │     └─ 3b. 获取新槽 → 记录 lease_started_at + holder
  │
  ├─ 4. Provider Concurrency Slot (可开关, 在 FP 之后)
  │     disabled → 跳过
  │     获取 count++ → 设置 acquire_timeout
  │     超时未归还 → 自动递减 (定时器/expire callback)
  │
  ├─ 5. Circuit Breaker
  │     open → 跳过此候选
  │
  ├─ 6. Execute Upstream
  │
  └─ 7. 资源归还
        ├─ Concurrency Slot: 立即归还 (响应/超时)
        ├─ FP Slot: 刷新 TTL + 更新 last_active_at
        └─ API Key Concurrent: 立即递减
```

### 2.3 数据模型变更

```redis
# FP Slot (增强)
Key: llmgw:cred_fp_slot:{credID}:{slotIndex}
Type: HASH (当前: STRING)
Fields:
  holder          → "{sessionID}"    # 持有者标识
  lease_started_at → "1718000000"    # 槽获取时间 (unix ts) ← 新增
  last_active_at  → "1718000500"     # 最后活跃时间 (unix ts) ← 新增
  request_id      → "req_xxx"        # 当前请求 ID ← 新增
  session_started_at → "1718000000"  # 会话开始时间 ← 新增
TTL: 1800 (30min, 当前)

# FP Slot Pin (不变)
Key: llmgw:sess_cred_fp:{holder}:{credID}
Type: STRING → slotIndex
TTL: 3600 (1h, 从 24h 缩短)

# Concurrency Slot (增强)
Key: llmgw:conc_slot:{credID}
Type: 当前 STRING(counter) → HASH
Fields:
  count          → "5"              # 当前并发数
  acquired_at    → "1718000000"     # 最近获取时间 ← 新增
  timeout_sec    → "300"            # 自动超时 (可配置) ← 新增

Key: llmgw:conc_session:{credID}:{sessionID}:{requestID}  (新增, 粒度到请求)
Type: HASH
Fields:
  acquired_at → "1718000000"
  timeout_at  → "1718000300"        # = acquired_at + timeout_sec
TTL: timeout_sec + 60

# API Key Concurrent (新增)
Key: llmgw:key_concurrent:{keyID}
Type: STRING (counter)
TTL: 60 (续期模式)
```

### 2.4 Provider 级别开关

```sql
-- provider_settings 表扩展 (已有表)
INSERT INTO provider_settings (provider_id, setting_key, setting_value, enabled)
VALUES
  (1, 'fp_slot.enabled', 'true', true),         -- 默认开启
  (1, 'conc_slot.enabled', 'true', true),        -- 默认开启
  (1, 'fp_slot.active_gate_sec', '300', true),   -- 5min 活跃门限
  (1, 'conc_slot.timeout_sec', '300', true);     -- 5min 并发超时
```

```go
// ProviderResourceConfig — 运行时缓存
type ProviderResourceConfig struct {
    FpSlotEnabled      bool          // 此 provider 是否启用 FP slot
    ConcSlotEnabled    bool          // 此 provider 是否启用并发 slot
    ActiveGateSec      int           // FP slot 活跃门限 (秒)
    ConcTimeoutSec     int           // 并发超时 (秒)
    KeyConcurrentLimit int           // API Key 并发上限
    KeyRPMLimit        int           // API Key RPM 上限
}
```

## 3. 详细设计

### 3.1 FP Slot 增强 — 会话时长跟踪

#### 3.1.1 存储变更

当前: `SET llmgw:cred_fp_slot:{credID}:{slotIndex} "{holder}" EX 1800`
目标: `HMSET llmgw:cred_fp_slot:{credID}:{slotIndex} holder "{holder}" lease_started_at "{ts}" last_active_at "{ts}" request_id "{reqID}" session_started_at "{ts}"`

通过 Lua 脚本原子升级, 兼容旧格式 (读 STRING 时自动迁移到 HASH)。

#### 3.1.2 获取流程

```lua
-- KEYS[1] = llmgw:cred_fp_slot:{credID}:{slotIndex}
-- KEYS[2] = llmgw:sess_cred_fp:{holder}:{credID}
-- ARGV[1] = holder (sessionID)
-- ARGV[2] = now
-- ARGV[3] = slotTTL
-- ARGV[4] = pinTTL
-- ARGV[5] = activeGate
-- ARGV[6] = requestID
-- ARGV[7] = sessionStartedAt

local raw = redis.call('GET', KEYS[1])
local holder = nil
local leaseStarted = nil

if raw then
    -- 旧格式: STRING → 升级到 HASH
    local t = redis.call('TYPE', KEYS[1])
    if t.ok == 'string' then
        holder = raw
        redis.call('DEL', KEYS[1])
        redis.call('HMSET', KEYS[1],
            'holder', holder,
            'lease_started_at', '0',
            'last_active_at', '0',
            'request_id', '',
            'session_started_at', '0')
    else
        -- 新格式: HASH
        local h = redis.call('HGETALL', KEYS[1])
        for i = 1, #h, 2 do
            if h[i] == 'holder' then holder = h[i+1] end
            if h[i] == 'lease_started_at' then leaseStarted = h[i+1] end
        end
    end
end
-- ... (复用现有获取逻辑)
```

#### 3.1.3 读取/监控接口

```go
// SlotInfo 槽详细信息 (用于 Admin 监控)
type SlotInfo struct {
    SlotIndex        int    `json:"slot_index"`
    Holder           string `json:"holder"`
    LeaseStartedAt   int64  `json:"lease_started_at"`
    LastActiveAt     int64  `json:"last_active_at"`
    RequestID        string `json:"request_id"`
    SessionStartedAt int64  `json:"session_started_at"`
    IdleSeconds      int64  `json:"idle_seconds"`       // 当前空闲时间
    LeaseDurationSec int64  `json:"lease_duration_sec"` // 已占用时长
}
```

### 3.2 Concurrency Slot 增强 — 自动超时回收

#### 3.2.1 设计

当前: 简单 INCR/DECR 计数器, 5min TTL 兜底
目标: 请求级粒度 + 可配置超时 + 到期自动归还

**核心变更:**
1. 获取时记录 `timeout_at = now + timeout_sec`
2. 使用 Redis **带过期通知**的 key — 超时后 Redis 自动删除, app 监听 `__keyevent@0__:expired` (或被动清理)
3. 不依赖过期事件 (精度不够), 改为**主动定时扫描 + TTL 兜底**双保险

```go
// Acquire — 获取并发槽
func (m *ConcurrencySlotManager) Acquire(ctx context.Context, credID int, sessionID, requestID string, timeoutSec int) (bool, error) {
    // 原子 INCR + 检查上限
    // 同时设置请求级 key: llmgw:conc_session:{credID}:{sessionID}:{requestID}
    // 字段: acquired_at, timeout_at, status (active|released|timedout)
    // TTL: timeoutSec + 60 (冗余)
}

// Release — 显式归还 (正常响应/失败时调用)
func (m *ConcurrencySlotManager) Release(ctx context.Context, credID int, sessionID, requestID string) error {
    // 原子 DECR 全局计数
    // 删除请求级 key, 记录 release_at
}

// ScanExpired — 定时清理过期槽 (background worker, 每 30s)
func (m *ConcurrencySlotManager) ScanExpired(ctx context.Context) {
    // SCAN llmgw:conc_session:* 模式
    // 检查 timeout_at < now
    // 原子 DECR 全局计数 + 删除过期 key
}
```

**为什么需要定时扫描而不是单纯依赖 TTL:**
- Redis key expiration 不精确 (被动检查 + 每 100ms 采样)
- 并发计数器需要精确递减, 不依赖 expire callback
- TTL 作为安全网, 定时扫描作为主回收路径

#### 3.2.2 Lua 脚本

```lua
-- acquireConcurrencySlot.lua
-- KEYS[1] = llmgw:conc_slot:{credID}
-- KEYS[2] = llmgw:conc_session:{credID}:{sessionID}:{requestID}
-- ARGV[1] = limit
-- ARGV[2] = timeout_sec
-- ARGV[3] = now
-- ARGV[4] = requestID

local count = tonumber(redis.call('GET', KEYS[1]) or '0')
if count >= tonumber(ARGV[1]) then
    return {0, count, 'limit_exceeded'}
end

redis.call('INCR', KEYS[1])
redis.call('EXPIRE', KEYS[1], 300)

redis.call('HMSET', KEYS[2],
    'acquired_at', ARGV[3],
    'timeout_at', tonumber(ARGV[3]) + tonumber(ARGV[2]),
    'request_id', ARGV[4],
    'status', 'active')
redis.call('EXPIRE', KEYS[2], tonumber(ARGV[2]) + 60)

return {1, count + 1, ''}
```

```lua
-- releaseConcurrencySlot.lua
-- KEYS[1] = llmgw:conc_slot:{credID}
-- KEYS[2] = llmgw:conc_session:{credID}:{sessionID}:{requestID}
-- ARGV[1] = now

local count = tonumber(redis.call('GET', KEYS[1]) or '0')
if count > 0 then
    redis.call('DECR', KEYS[1])
end

local raw = redis.call('HGETALL', KEYS[2])
if raw and #raw > 0 then
    local args = {}
    for i = 1, #raw, 2 do
        table.insert(args, raw[i])
        table.insert(args, raw[i+1])
    end
    table.insert(args, 'released_at')
    table.insert(args, ARGV[1])
    table.insert(args, 'status')
    table.insert(args, 'released')
    redis.call('HMSET', KEYS[2], unpack(args))
    redis.call('EXPIRE', KEYS[2], 3600)  -- 保留 1h 供排查
else
    redis.call('DEL', KEYS[2])
end

return 1
```

### 3.3 API Key 并发限制 (阻塞硬上限)

#### 3.3.1 设计

当前: Limiter 第 5 层 (Per-Key) 是**非阻塞**软上限 — 超限仅日志, 不拒绝。
目标: 新增**阻塞硬上限**, 在 `checkGatewayRateLimit()` 后/FP Slot 前检查。

```go
// KeyConcurrencyManager — 独立于 Limiter 的 API Key 并发管理器
type KeyConcurrencyManager struct {
    redis    redis.UniversalClient
}

// Acquire 原子 INCR + 检查上限
// 返回: (acquired, current, limit)
func (m *KeyConcurrencyManager) Acquire(ctx context.Context, keyID int, limit int) (bool, int, int) {
    if limit <= 0 {
        return true, 0, 0  // unlimited
    }
    key := fmt.Sprintf("llmgw:key_concurrent:%d", keyID)
    // Lua: GET → if >= limit return 0 → INCR → EXPIRE 60
}

// Release 原子 DECR
func (m *KeyConcurrencyManager) Release(ctx context.Context, keyID int) {
    key := fmt.Sprintf("llmgw:key_concurrent:%d", keyID)
    // Lua: GET → if > 0 DECR → if == 0 DEL
}
```

#### 3.3.2 集成到请求流程

```go
// handler.go — checkGatewayRateLimit 之后, Executor 之前

func (h *ChatHandler) checkResourceGates(params *ExecParams) error {
    // 1. API Key RPM (已有)
    if outcome := h.checkGatewayRateLimit(params); outcome.Blocked {
        return ErrRateLimited
    }

    // 2. API Key Concurrent (新增)
    if params.KeyConcurrentLimit > 0 {
        acquired, current, limit := h.keyConcMgr.Acquire(params.Ctx, params.KeyID, params.KeyConcurrentLimit)
        if !acquired {
            return ErrConcurrentLimitExceeded
        }
        // 注入 release 到 params cleanup
        params.AddCleanup(func() {
            h.keyConcMgr.Release(params.Ctx, params.KeyID)
        })
    }

    return nil
}
```

#### 3.3.3 API Key 并发配置路径

```
DB: api_keys.rate_limit_concurrent (int, nullable)
  ├─ >0  → 使用该值 (阻塞硬上限)
  ├─ =0  → 无限制 (跳过检查)
  └─ nil → 使用层级默认值:
              system:     50
              production: 20
              default:    6
              applicant:  2

KeyInfo.EffectiveConcurrent() 已有此逻辑, 只需将其从"非阻塞软上限"升级为"阻塞硬上限"
```

### 3.4 Provider 级别开关设计

#### 3.4.1 设置项定义

| 设置键 | 类型 | 默认值 | 作用域 | 说明 |
|--------|------|--------|--------|------|
| `provider.{id}.fp_slot.enabled` | bool | true | Provider | 是否启用该提供者的 FP slot |
| `provider.{id}.fp_slot.active_gate_sec` | int | 300 | Provider | 活跃门限 (秒) |
| `provider.{id}.fp_slot.slot_limit` | int | 20 | Provider | 该提供者默认槽数 (覆盖 credentials 级别) |
| `provider.{id}.conc_slot.enabled` | bool | true | Provider | 是否启用该提供者的并发 slot |
| `provider.{id}.conc_slot.timeout_sec` | int | 300 | Provider | 并发超时 (秒) |
| `provider.{id}.conc_slot.limit` | int | 50 | Provider | 该提供者默认并发上限 |
| `provider.{id}.rate_limit.rpm` | int | 0 | Provider | 该提供者 RPM 上限 (0=不限) |
| `provider.{id}.rate_limit.concurrent` | int | 0 | Provider | 该提供者并发上限 (0=不限) |

#### 3.4.2 运行时缓存

```go
// ProviderResourceGate — 每个 provider 的资源门禁配置
type ProviderResourceGate struct {
    ProviderID      int
    FpSlotEnabled   atomic.Bool
    ConcSlotEnabled atomic.Bool
    ActiveGateSec   atomic.Int64
    ConcTimeoutSec  atomic.Int64
    SlotLimit       atomic.Int64
    ConcLimit       atomic.Int64
    RPMLimit        atomic.Int64
    ConcLimitKey    atomic.Int64
}

// Registry — 全局缓存, 热加载
var providerGates = sync.Map{} // providerID → *ProviderResourceGate

func GetProviderGate(providerID int) *ProviderResourceGate {
    v, _ := providerGates.LoadOrStore(providerID, &ProviderResourceGate{
        ProviderID: providerID,
    })
    return v.(*ProviderResourceGate)
}

// Hot-reload hook: 监听 provider_settings 变更, 刷新缓存
```

### 3.5 执行器流程变更

```go
// executor.go — 优化后候选循环

type Executor struct {
    // 已有
    FpSlots   *credentialfpslot.Manager
    Limiter   *credential.Limiter
    Breaker   *credential.BreakerManager

    // 新增
    ConcSlots  *ConcurrencySlotManager    // 从 ursm 移入或独立
    KeyConcMgr *KeyConcurrencyManager     // API Key 并发管理器
    ProvGates  *ProviderGateRegistry      // Provider 开关缓存
}

// 候选循环核心变更:

for _, cand := range candidates {
    gate := GetProviderGate(cand.ProviderID)

    // Step 1: Provider-level FP slot (可开关)
    if gate.FpSlotEnabled.Load() {
        lease, err := e.FpSlots.Acquire(ctx, cand.CredentialID, limit, holder)
        if err == ErrSlotSaturated {
            if !fpSlotDegraded { continue }
            // degraded → 跳过槽, 但记录
        }
        // 获取成功: 已自动记录 lease_started_at
    }

    // Step 2: Provider-level concurrency (可开关, 在 FP 之后)
    if gate.ConcSlotEnabled.Load() {
        timeout := gate.ConcTimeoutSec.Load()
        acquired, err := e.ConcSlots.Acquire(ctx, cand.CredentialID, sessionID, requestID, int(timeout))
        if !acquired {
            // 释放 FP slot, 跳过此候选
            e.FpSlots.Release(ctx, lease)
            continue
        }
        defer e.ConcSlots.Release(ctx, cand.CredentialID, sessionID, requestID)
    }

    // Step 3: Circuit breaker
    // Step 4: Execute
    // Step 5: defer release (FP + concurrency + key conc)
}
```

## 4. 迁移步骤

```
Phase A: Provider 开关 + 存储升级 (无损)
  A1. ProviderResourceGate 注册表 + 热加载
  A2. FP slot STRING→HASH 迁移 Lua 脚本 (兼容读写)
  A3. 在 executor.go 中注入 provider 开关检查
  A4. 验证: 旧行为不变, 新开关生效

Phase B: 时长跟踪 + Concurrency 超时回收
  B1. FP slot HASH 写入 lease_started_at, last_active_at, session_started_at
  B2. ConcSlotManager 增强: 请求级粒度 + timeout_at
  B3. ScanExpired 后台 worker (30s)
  B4. Admin 监控接口: /api/admin/fp-slots/{credID}

Phase C: API Key 并发硬上限
  C1. KeyConcurrencyManager 实现
  C2. handler.go 集成: checkResourceGates
  C3. 移除 Limiter 第 5 层软上限 (Key 层), 统一到新管理器
  C4. 验证: 旧软上限日志消失, 新阻塞上限生效

Phase D: 清理 + 文档
  D1. 移除 credentialfpslot/ 中的遗留代码 (如果已完全迁移到 ursm)
  D2. 更新 provider_settings 文档和 Admin UI
  D3. 监控面板: 槽占用率、并发使用率、超时统计
```

## 5. 兼容性保障

| 阶段 | 兼容性 |
|------|--------|
| Phase A | 旧 FP slot STRING 格式被自动识别并升级到 HASH; 新代码可读旧格式 |
| Phase B | 旧 conc_slot 计数器仍然有效; 新请求级 key 独立存在 |
| Phase C | 旧 Limiter 第 5 层软上限与新 KeyConcurrencyManager 可共存; 逐渐移除旧逻辑 |
| 总回滚 | `KILL_FP_SLOT=1` + `KILL_RATE_LIMITER=1` 可瞬间回到无门禁状态 |

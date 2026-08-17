# 统一资源管线方案 — URSM Phase 4

> 整合 Phase 3 (状态管理优化) + Phase 4 (资源门禁优化) 为单一架构。
> 本文档是 URSM 重构的最终纲领, 覆盖请求生命周期所有阶段。
> 2026-07-13 最终修订: 引入模型索引双 key 模型。

## 1. 统一架构全景

### 1.1 当前 4 条独立管线

```
写路径:                   读路径:                   门禁路径:
  状态 A (cred.Writer)      → router.go 4层缓存       → API Key RPM (Redis)
  状态 B (credentialstate)  → FpSlots.GetNodeState    → Limiter 5 层
  状态 C (credentialhealth) → credentialstate.IsAvail → FP Slot (credentialfpslot)
  状态 D (URSM Record, 在建)                           → Circuit Breaker
  → 4 条写路径, 3 条读路径, 4 层门禁, 互不感知
```

### 1.2 目标: 单一管线

```
客户端请求
  │
  ├─ Phase 1: Auth + RPM (handler.go)
  │    RPM/TPM 滑动窗口 + KeyConcurrencyManager (阻塞硬上限)
  │
  ├─ Phase 2: 路由 (router.go)
  │    正常: DB 查询 → (credID, stdModel) → HGETALL node 状态
  │    降级: 先读会话路由绑定；仅未绑定会话才从 model 索引发现候选
  │
  ├─ Phase 3: 候选循环 (executor.go)
  │    已绑定会话: 仅对绑定路由执行 Gate → FP Slot → Conc Slot → CB → Execute
  │    → StateRecorder.Record (单次写入)
  │      ├─ HMSET node: 状态 + rawModel + metadata
  │      └─ HSET model 索引: credID → rawModel
  │
  └─ Phase 4: 清理
       KeyConcurrent Release + Telemetry
```

## 2. Redis 数据模型 (核心)

### 2.1 状态 + 路由数据 (双 key 原子写入)

```
Key 1: ursm:node:{credID}:{stdModel}          → HASH    TTL: 7d
  用途: 路由时判断可用性 + 获取 rawModel 调 API + 获取评分元数据
  Fields:
    available           → "1" / "0"           # 路由核心决策
    consecutive_fail    → "0"                 # 连续失败次数
    last_error          → "rate_limited"      # 最近错误类型
    last_success_at     → "1718000000"        # unix ts
    last_failure_at     → "1718000100"        # unix ts
    disabled_until      → "1718000500"        # 冷却到期时间
    latency_avg_ms      → "850"               # 指数移动平均延迟
    updated_at          → "1718000100"        # 最后更新

    # ↓ 路由元数据 (新增, 支持降级模式路由)
    raw_model           → "gpt-4o-2024-08-06" # 供应商实际模型名
    provider_id         → "42"                # 供应商 ID
    fp_slot_limit       → "20"                # 指纹槽上限
    concurrency_limit   → "50"                # 并发上限

Key 2: ursm:model:{stdModel}                  → HASH    TTL: 7d
  用途: 模型→凭据发现索引 (DB 降级时替代 DB 查询)
  字段: {credID} → {rawModel}
  示例: HSET ursm:model:gpt-4o 1 "gpt-4o-2024-08-06" 2 "gpt-4o-latest"
```

### 2.2 资源门禁 (不变)

```
llmgw:cred_fp_slot:{credID}:{slotIndex} → HASH   TTL: 30min  (原 STRING)
  holder, lease_started_at, last_active_at, request_id, session_started_at

llmgw:sess_cred_fp:{holder}:{credID}    → STRING TTL: 1h (原 24h)

llmgw:conc_slot:{credID}                → STRING TTL: 300s (计数器)
llmgw:conc_session:{credID}:{sessionID}:{requestID} → HASH TTL: timeout+60
  acquired_at, timeout_at, request_id, status

llmgw:key_concurrent:{keyID}            → STRING TTL: 60s (计数器)

ursm:session_route:{sessionID}:{stdModel} → HASH TTL: 会话 TTL
  credential_id, provider_id, std_model, raw_model, fp_slot_index,
  bound_at, last_success_at, last_error_at, failure_count
  用途: 强会话路由绑定。没有来自该绑定路由的上游错误时，禁止改选节点。
```

## 3. RecordRequest 与 StateRecorder

### 3.1 RecordRequest (最终版)

```go
type RecordRequest struct {
    CredentialID int
    StdModel     string       // 标准化模型名 → key: ursm:node:{credID}:{stdModel}
    RawModel     string       // 供应商实际模型名 → 写入 HASH + 模型索引
    ProviderID   int          // 供应商 ID → 路由评分用
    FpSlotLimit  int          // 指纹槽上限 → 路由评分用
    ConcLimit    int          // 并发上限 → 路由评分用
    Success      bool
    LatencyMs    int
    ErrorKind    string
    Source       string       // "request" | "probe_v2" | "model_probe" | "manual"
}

type NodeReadState struct {
    Available       bool
    ConsecutiveFail int
    LastError       string
    RawModel        string     // ← 新增: 用于 API 调用
    ProviderID      int        // ← 新增
    FpSlotLimit     int        // ← 新增
    ConcurrencyLimit int       // ← 新增
    LastSuccessAt   int64
    LastFailureAt   int64
    DisabledUntil   int64
    LatencyAvgMs    int
}
```

### 3.2 Record 方法 (Lua 脚本增强)

```
KEYS[1] = ursm:node:{credID}:{stdModel}        (节点状态 key)
KEYS[2] = ursm:model:{stdModel}                (模型索引 key, 新增)

ARGV[1] = kind          ("success" | "failure")
ARGV[2] = error_kind
ARGV[3] = now           (unix seconds)
ARGV[4] = latency_ms
ARGV[5] = fail_threshold   (default 2)
ARGV[6] = cooldown_sec     (default 300)
ARGV[7] = raw_model     ← 新增
ARGV[8] = provider_id   ← 新增
ARGV[9] = fp_slot_limit ← 新增
ARGV[10] = conc_limit   ← 新增

Lua 逻辑:
  1. HMSET ursm:node:{credID}:{stdModel} (原子状态更新 + metadata)
  2. HSET ursm:model:{stdModel} {credID} rawModel (模型索引)
  3. EXPIRE 两个 key 均为 7d
```

### 3.3 状态转换表 (同 Phase 3)

| 事件 | Redis 操作 | DB 操作 |
|------|-----------|---------|
| 请求成功 | HMSET available=1, consecFail=0; HSET 模型索引 | 转换时 INSERT state_change_log |
| 请求失败 (< 2) | HMSET consecFail++, last_failure_at=now | 无 |
| 请求失败 (≥ 2) | HMSET available=0, disabledUntil=now+300 | 转换时 INSERT |
| 探测成功 | 同请求成功 | 同上 |
| 冷却到期 | Lua 读时自动恢复 | 无 |
| 管理员切换 | HMSET available=0/1 | INSERT |

## 4. 路由读路径 (Phase 2 详细)

### 4.1 正常模式 (DB 可用)

```
1. 优先 HGETALL ursm:session_route:{sessionID}:{stdModel}
   → 命中且未被明确失效: 只构造该绑定的候选，不参与 DB 候选重排、P2C 或协议亲和。
   → 未命中: 继续步骤 2，执行首次选路。

2. DB 查询: SELECT credID, rawModel, providerID, fp_slot_limit, conc_limit
     FROM provider_models + credentials + model_offers
     WHERE standardized_name = $1 AND lifecycle_status = 'active'
   → 返回候选列表 (同当前)

3. 对每个候选:
     HGETALL ursm:node:{credID}:{stdModel}
     → 解析: available, consecutive_fail, raw_model, ...
     → 过滤不可用节点
     → P2C 评分: latency_avg_ms, fp_slot_limit(槽压力), ...

4. 首次候选成功后，原子写入 session route binding；后续请求使用绑定的 rawModel 调供应商 API。
```

### 4.2 降级模式 (DB 不可用)

```
1. 优先 HGETALL ursm:session_route:{sessionID}:{stdModel}
   → 命中且未被明确失效: 使用绑定的 credID + providerID + rawModel；不重新选路。
   → 未命中: 继续步骤 2。

2. HGETALL ursm:model:{stdModel}
   → {credID: rawModel, credID: rawModel, ...}
   → 获取该模型下所有凭据

3. 对每个 credID:
     HGETALL ursm:node:{credID}:{stdModel}
     → 拿到: available, raw_model, provider_id, fp_slot_limit, latency_avg_ms
     → 过滤不可用
     → 简易评分: 仅用 latency + 成功率 (无 price 数据)

4. rawModel 用于上游 API 调用
   无 price 数据 → 使用 provider 级默认价格 (或跳过价格排序)
   首次成功后写入 session route binding；后续请求优先读取该绑定
```

### 4.3 一致性保障

```
正常模式:
  DB 返回 rawModel_A, Redis 返回 rawModel_B
  → 未绑定会话: 拒绝本次候选并记录 warning，下一次状态写入修正缓存；不得猜测选 A 或 B。
  → 已绑定会话: 使用 session binding 内的 rawModel，直到该绑定路由发生上游错误或被显式失效。
  → 管理员修改映射时: 只影响新会话；不得静默改写存量 session binding。

降级模式:
  DB 不可用, 仅依赖 Redis
  → Redis 数据可能过期 (TTL 7d), 但可用性判断一定准确
  → rawModel 在降级期间也可能过时, 但可用性数据优先
```

## 5. Resource Gating (Phase 4 整合)

### 5.0 会话路由不变性 (强契约)

```
契约:
  对同一 (sessionID, stdModel)，首次成功上游响应后必须创建 route binding。
  在该绑定路由没有产生上游错误前，credential、provider、rawModel、fp_slot_index 均不得变化。

允许解除/重建绑定的唯一条件:
  1. 该绑定路由实际发起上游调用，且收到可归因于上游的失败结果；
  2. 会话显式结束、过期，或客户端显式请求 reset route；
  3. 管理员显式终止该会话路由（返回可见的 route-reset 结果，不能静默切换）。

不是解除条件:
  FP slot / 并发 slot 饱和、P2C 重排、价格变化、协议亲和、DB/Redis 降级、
  探测结果、后台状态变化、rawModel 配置刷新、普通客户端错误。

资源不足处理:
  已绑定会话只可等待绑定资源至 resource_wait_deadline；到期返回 429/503。
  不得将资源不足伪装成上游失败，或以此切换到其他凭据/供应商/原生模型。

实现约束:
  现有 FP/Concurrency Acquire 是立即失败接口，不能直接满足上述等待语义。
  新接口必须为 AcquireUntil(ctx, binding, deadline)，在同一绑定资源上有限重试；
  绝不通过在候选列表中选择另一个节点来实现等待。
```

```go
type SessionRouteBinding struct {
    CredentialID int
    ProviderID   int
    StdModel     string
    RawModel     string
    FPSlotIndex  int
    BoundAt      time.Time
    LastSuccessAt time.Time
}

// Router contract:
// 1. binding hit: return exactly one bound candidate.
// 2. binding miss: P2C/protocol affinity selects an initial candidate.
// 3. only an actual upstream error from the bound candidate may invoke Failover.
```

### 5.1 Provider 级别开关

| 设置键 | 默认值 | 影响 |
|--------|--------|------|
| `provider.{id}.fp_slot.enabled` | true | 跳过 FP slot |
| `provider.{id}.fp_slot.active_gate_sec` | 300 | 活跃门限 |
| `provider.{id}.fp_slot.slot_limit` | 20 | 覆盖 credentials |
| `provider.{id}.conc_slot.enabled` | true | 跳过并发 slot |
| `provider.{id}.conc_slot.timeout_sec` | 300 | 并发超时 |
| `provider.{id}.conc_slot.limit` | 50 | 并发上限 |
| `provider.{id}.rate_limit.rpm` | 0 (=不限) | 提供者级 RPM |
| `provider.{id}.rate_limit.concurrent` | 0 (=不限) | 提供者级并发 |
| `session_route.resource_wait_deadline_ms` | 5000 | 绑定会话等待自身资源的最大时长 |

配置热加载: `ProviderResourceGate` 缓存, 监听 `provider_settings` 变更。

### 5.2 降级路径矩阵

| 降级场景 | 行为 | 指标 |
|---------|------|------|
| 绑定会话的 FP slot 饱和 | 等待同一绑定槽至 `resource_wait_deadline`；超时返回 429/503，不换节点 | llmgw_sticky_resource_wait_total |
| 未绑定会话的 FP slot 全饱和 | `fpSlotDegraded=true` 后可受控降级尝试 | llmgw_fp_slot_degraded_total |
| 绑定会话的并发 slot 饱和 | 等待同一绑定凭据至 `resource_wait_deadline`；超时返回 429/503，不换节点 | llmgw_sticky_resource_wait_total |
| 未绑定会话的并发 slot 饱和 | 释放 FP，尝试下一候选 | (待新增) |
| API Key 并发超限 | 429 拒绝请求 | llmgw_key_concurrent_exceeded_total |
| 绑定会话的 Circuit Open | 不换节点；等待冷却/半开窗口或返回 503，不将历史错误隐藏为换路由 | llmgw_sticky_circuit_block_total |
| 未绑定会话的 Circuit Open | 跳过此候选 | llmgw_circuit_open_total |
| DB 不可用 (路由) | 先读 session binding；仅未绑定会话使用 HGETALL `ursm:model:{stdModel}` | (待新增) |
| Redis 不可用 | 读 fail-open, 写跳过 | (待新增) |

## 6. 端到端请求生命周期 (完整版)

```
Client Request (model = "gpt-4o")
  │
  ├─ Phase 1: Auth + RPM (handler.go)
  │    1.1 认证 → KeyInfo {RPM, Concurrent, Tier}
  │    1.2 RPM check: rl:rpm:{keyID} Redis 滑动窗口
  │    1.3 KeyConcurrencyManager.Acquire: INCR llmgw:key_concurrent:{keyID}
  │        ≥ limit → 429
  │        defer Release (请求结束)
  │
   ├─ Phase 2: 路由 (router.go)
   │    2.0 HGETALL ursm:session_route:{sessionID}:gpt-4o
   │        命中 → 只执行 cred:1/raw:"gpt-4o-2024-08-06"，不再查找替代候选
   │    2.1 未命中才 DB 查询候选:
  │        SELECT credID, rawModel, providerID, price, fp_slot_limit, concurrency_limit
  │        WHERE standardized_name = 'gpt-4o' AND active
  │        → [(cred:1,raw:"gpt-4o-2024-08-06",prov:42), (cred:2,raw:"gpt-4o-latest",prov:43)]
  │        │
  │        ├─ DB OK → 正常读路径
  │        │    HGETALL ursm:node:1:gpt-4o  → available=true, raw_model="gpt-4o-2024-08-06"
  │        │    HGETALL ursm:node:2:gpt-4o  → available=false, consecutive_fail=3
   │        │    P2C 评分 → 选 cred:1 → 首次成功后创建 session route binding
  │        │
  │        └─ DB FAIL → 降级读路径
  │             HGETALL ursm:model:gpt-4o  → {1:"gpt-4o-2024-08-06", 2:"gpt-4o-latest"}
  │             HGETALL ursm:node:1:gpt-4o  → available=true, raw_model: match
  │             HGETALL ursm:node:2:gpt-4o  → available=false
   │             简易评分 → 选 cred:1 → 首次成功后创建 session route binding
  │
  ├─ Phase 3: 候选循环 (executor.go)
   │    3.1 Gate: 检查 provider 42 的 fp_slot/conc_slot 开关；绑定会话资源不足只能等待/拒绝
   │    3.2 FP: Acquire llmgw:cred_fp_slot:1 → 成功 (slot 3)
   │    3.3 Conc: Acquire llmgw:conc_slot:1 → count=5/50
   │    3.4 CB: Allow provider:42/credential:1 → closed
  │    3.5 Execute: gpt-4o-2024-08-06 → ✅ 200
  │    3.6 StateRecorder.Record:
  │        KEYS[1] ursm:node:1:gpt-4o
  │        KEYS[2] ursm:model:gpt-4o
  │        → HMSET node: available=1, consecFail=0, raw_model="gpt-4o-2024-08-06", ...
  │        → HSET model: 1 "gpt-4o-2024-08-06"
  │        → 状态未转换: 不写 DB
   │    3.7 defer Release: Conc → FP → Limiter；成功仅刷新 binding 的 last_success_at
  │
  └─ Phase 4: Cleanup (handler.go)
        KeyConcurrencyManager.Release(keyID)
        Telemetry (request_logs + SSE)
```

## 7. 迁移路线图

```
Phase 0: 准备 (1-2 天)
  P0.1 ProviderResourceGate 注册表 + 热加载
  P0.2 KeyConcurrencyManager 实现
  P0.3 state_change_log 表 (已完成)

Phase 1: 状态合并 + 双 key 写入 (3-5 天)
  P1.1 StateRecorder 实现 (ursm/recorder.go, 结构体已完成, 需扩展 Lua)
  P1.2 executor.go 注入 StateRecorder (双写)
  P1.3 router.go 切换读路径 → StateRecorder.IsAvailable + GetNodeState
  P1.4 模型索引 HSET 写入 (Lua 内同步维护)
  P1.5 创建 `ursm:session_route:{sessionID}:{stdModel}` 强绑定及读写接口
  P1.6 验证状态一致性 (新旧双写对比)

Phase 2: 资源门禁增强 (3-5 天)
  P2.1 FP slot STRING→HASH 迁移 + 时长跟踪
  P2.2 ConcSlotManager 请求级粒度 + timeout_at
  P2.3 ScanExpired 后台 worker
  P2.4 executor.go 集成 ConcSlotManager
  P2.5 Provider 开关注入候选循环
  P2.6 已绑定会话: 资源饱和改为 wait-or-reject，禁止 candidate fallback
  P2.7 修正 sticky 优先级: binding 命中后跳过 protocol affinity、tier 截断和 P2C
  P2.8 新增 FP/Concurrency `AcquireUntil`，限制同一绑定资源的等待重试

Phase 3: API Key 并发硬上限 (2 天)
  P3.1 handler.go 集成 KeyConcurrencyManager
  P3.2 移除 Limiter 第 5 层软上限

Phase 4: 降级路由 (1 天)
  P4.1 HGETALL ursm:model:{stdModel} 降级读路径
  P4.2 DB 降级时自动切换读路径

Phase 5: 清理 (1-2 天)
  P5.1 废弃代码 → _to-be-deprecated/
  P5.2 DB 清理 (credential_state_log, passive_probe_state)
  P5.3 监控面板
```

## 8. 兼容性与回滚

| Phase | 兼容 | 回滚 |
|-------|------|------|
| 0 | 无行为变更 | 无需回滚 |
| 1 | 双写, 旧路径仍活跃 | `KILL_FP_SLOT=1` |
| 2 | 旧 STRING 自动升级 HASH | `KILL_FP_SLOT=1` |
| 3 | 新旧共存 | `KILL_RATE_LIMITER=1` |
| 4 | 仅 DB 降级时激活 | 关闭降级检测 |

## 9. 审计清单

### 9.1 数据一致性审计

| # | 检查项 | 结论 |
|---|--------|------|
| 1 | node HASH 与 model 索引 HASH 是否原子写入？ | ✅ 同一 Lua 脚本 HMSET + HSET + EXPIRE |
| 2 | rawModel 在 node 和 model 索引是否一致？ | ✅ 同一 Lua 脚本写入同一值 |
| 3 | TTL 是否同步？ | ✅ 两个 key 设置相同 TTL=7d |
| 4 | 删除节点时是否清理模型索引？ | ❌ 需要: Lua 删除 node 时 SREM/HDEL 模型索引 |
| 5 | 模型索引是否无限增长？ | ✅ TTL 7d 自动过期, 活跃节点被续期 |

### 9.2 降级路径审计

| # | 检查项 | 结论 |
|---|--------|------|
| 1 | DB 不可用时路由能否正常工作？ | ✅ HGETALL model 索引 → 遍历 credID → HGETALL node |
| 2 | 降级时 rawModel 是否可用？ | ✅ model 索引存储 credID→rawModel |
| 3 | 降级时路由评分是否有兜底？ | ✅ 无 price 时跳过价格排序 |
| 4 | 降级时 sticky 是否可用？ | ✅ 先读 Redis session route binding；命中后不重新选路 |
| 5 | DB 恢复后是否自动切回？ | ✅ DB 恢复检测 + 自动切换读路径 |

### 9.3 并发安全审计

| # | 检查项 | 结论 |
|---|--------|------|
| 1 | 并发写入同一 node 会丢数据吗？ | ✅ Lua 单线程原子 HGETALL→判断→HMSET |
| 2 | FP slot 并发获取有竞态吗？ | ✅ 现有 Lua 脚本已处理 |
| 3 | Conc slot 超时后自动 DECR 安全吗？ | ✅ ScanExpired 定时扫描 + TTL 双保险 |
| 4 | KeyConcurrency acquire/release 匹配？ | ✅ defer Release 确保配对 |

### 9.4 迁移兼容性审计

| # | 检查项 | 结论 |
|---|--------|------|
| 1 | 旧 STRING 格式 FP slot 如何兼容？ | ✅ Lua 检测 TYPE→自动升级到 HASH |
| 2 | 旧 credentialstate 缓存是否仍在？ | ✅ Phase 1 双写期间正常运行 |
| 3 | 旧 router.go 4 层缓存如何替换？ | ✅ Phase 1.3 逐步切流 |
| 4 | 模型索引首次写入需要数据预热？ | ✅ StateRecorder 写时自动创建, 无需预热 |

### 9.5 遗漏分析

| # | 发现 | 处理 |
|---|------|------|
| 1 | **model 索引缺少删除维护** — 节点被删除/过期时, `ursm:model:{stdModel}` 中的字段不会自动清理 | Lua 脚本在写入时覆盖, 旧字段残留但 TTL 到期自动过期; 可接受 |
| 2 | **降级路由无 sticky** — 会破坏厂商缓存命中 | 已修正: DB 降级时 session binding 仍为权威路由 |
| 3 | **P0.1 ProviderResourceGate 需要 DB 查询** — 如果 DB 都不可用, 开关也无法读取 | 设默认值 (全部 enabled), 降级期间不过滤 |
| 4 | **模型索引 key 与路由 DB 查询结果可能不一致** — 7d TTL 内若凭据被停用, Redis 还标记为活跃 | 正常模式以 DB 为准; 降级模式以 Redis 为准 (过时但可用) |
| 5 | **ScanExpired 并发扫描 redis 性能** — 全量 SCAN `llmgw:conc_session:*` 可能影响性能 | 使用 COUNT=100 分批, 30s 间隔, 低影响 |

### 9.6 会话路由不变性审计 (2026-07-13)

| 严重性 | 当前代码/原方案问题 | 证据 | 修正要求 |
|--------|--------------------|------|----------|
| P0 | 协议亲和在 sticky 提前后重新排序，可能让非绑定候选先执行并覆盖 sticky | `executors/router.go:151-157,569-593` | binding 命中立即返回单一候选，禁止 protocol affinity 重排 |
| P0 | FP 槽预过滤/Acquire 失败会在未调用上游前跳过粘性凭据 | `executors/executor.go:939-994,1065-1087` | binding 命中时改为 wait-or-reject，禁止 fallback |
| P0 | 并发饱和与 Circuit Open 会在未调用上游前改选下一候选 | `executors/executor.go:1093-1129` | binding 命中时改为等待同一资源或返回 429/503 |
| P1 | sticky 只存 credentialID，不能保证 rawModel 连续性 | `executors/sticky.go:69-92,140-199` | 改存 `credential + provider + stdModel + rawModel + fpSlotIndex` |
| P1 | async retry 以 nil sticky 重新规划，会静默改路由 | `executors/executor.go:2728-2743` | async retry 传递 binding；无上游错误不得创建新 binding |
| P1 | DB 降级 Redis 模型索引读路径尚未接入实际 provider/handler | `provider/client.go:410-439`, `handler.go:1414-1423` | Phase 4 实现前不得宣称已支持 DB 降级路由 |

验收测试:
  1. 同 session + stdModel 连续 100 次成功请求，credential/provider/rawModel/fpSlotIndex 全程不变。
  2. 绑定凭据 FP/并发饱和时，验证未发起其他候选上游请求，最终为等待成功或 429/503。
  3. binding 命中后改变候选价格、tier、协议排序或 rawModel 配置，验证当前会话仍使用绑定路由。
  4. DB 降级期间 binding 命中，验证仍使用绑定 rawModel；仅新会话才从模型索引首次选路。
  5. 绑定路由上游失败后，才允许 failover，并写明 `route_change_reason=upstream_error`。

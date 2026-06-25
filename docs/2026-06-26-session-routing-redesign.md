# Client / Session / RouteNode 三层路由设计（V3.1，2026-06-26）

> 本文档定义 llm-gateway-go 中客户端、会话、路由节点、反爬伪装四者的关系与流转规则。
> 这是经过 V1（会话级 sticky）/ V2（凭据级 sticky）/ V3（slot=并发许可位）三轮校准的最终版本。
> 所有后续 Phase 实施都以本文档为"真理之源"。

---

## 一、问题陈述

### 1.1 业务诉求

LLM Gateway 作为多供应商、多凭据的反向代理，需要在以下约束下提供稳定的请求转发：

1. **会话连续性**：同一客户端在同一会话内应当尽量路由到相同的上游凭据，避免 OpenAI/Anthropic 的 prompt cache 失效。
2. **偶发错误容忍**：同一会话不应因偶发错误（网络抖动、超时）而频繁切换上游凭据。
3. **连续失败切换**：当路由节点真的不可用（连续失败 ≥ 3 次）时，应当切到其他可用节点，5 分钟后可恢复。
4. **反爬伪装**：上游供应商对每个账号看到的"并发用户数"有限制（典型 codeplan 凭据 3-5 个并发），gateway 必须把多个真实客户端折叠到少数几个固定的"虚拟身份"上。
5. **客户端零 ID 友好**：很多客户端 SDK 不发稳定会话 ID，gateway 需要在 5 分钟窗口内复用系统分配的会话 ID。

### 1.2 设计决策矩阵

| 决策点 | 选择 | 依据 |
|--------|------|------|
| 客户端（Client）标识 | `api_keys` 行 + `KeyInfo{ID, TenantID, ...}` | 现有架构，无独立 clients 表 |
| 会话（Session）ID 来源 | 多 header + 5 分钟内系统赋值复用 | 兼容性 + 连续性 |
| Sticky Key 粒度 | client 级 `{tenant:app:key:profile}` | 与 fp_slot 持有者对齐，保持反爬稳定 |
| 路由节点（RouteNode）状态维度 | `(credID, model)` | 状态属于节点本身，不属于会话 |
| 失败计数归属 | 节点上累计，不在会话上 | 切会话/换模型保留旧节点状态 |
| 连续失败阈值 | 3 次（5 分钟窗口内） | 偶发错误不触发切换 |
| Slot 语义 | 并发许可位（非独占身份） | `concurrency_limit ≥ fp_slot_limit` |
| Slot 双层展示 | 指纹槽 + 并发槽 | 互不重叠、各自独立计数 |

---

## 二、整体架构

```
                            ┌─────────────────────────────────┐
                            │  上游供应商（OpenAI/Anthropic）    │
                            │  只看到 N 个固定"虚拟客户"在并发   │
                            │  （N = fp_slot_limit，典型 3-5）   │
                            └─────────────────────────────────┘
                                              ↑
                注入 X-Device-Seed / X-Virtual-Client-Id / UA
                                              ↑
┌────────────────────────────────────────────────────────────────────┐
│ Layer 4: Outbound Headers 注入                                      │
│   ApplyEgressHeaders(lease.Egress)   // 来自指纹槽                 │
│   HeadersForSlot(slotIndex)  → User-Agent, Accept-Language         │
└────────────────────────────────────────────────────────────────────┘
                                              ↑
┌────────────────────────────────────────────────────────────────────┐
│ Layer 3: 双层 Slot（指纹槽 + 并发槽）                                │
│   指纹槽 (fp_slot_limit, 通常 3-5):                                 │
│     - 每个 slot 绑定固定 EgressIdentity                             │
│     - 同 fingerprint 可承载多 in-flight 请求                       │
│   并发槽 (concurrency_limit, 通常 20):                              │
│     - 每个 slot = 一个"并发许可证"                                  │
│     - 纯计数，无身份信息                                            │
│   关系: concurrency_limit ≥ fp_slot_limit                          │
└────────────────────────────────────────────────────────────────────┘
                                              ↑ (credential 由 Layer 2 决定)
┌────────────────────────────────────────────────────────────────────┐
│ Layer 2: 路由选择（含 (credID, model) 健康状态）                    │
│   - 过滤 RouteNodeState.IsUsable()==false 的候选                   │
│   - 优先 SessionPreferredCredential                                  │
│   - 其余按 P2C / tier / billing 排序                               │
│   - 失败计数归属 (credID, model)，跨会话累计                        │
└────────────────────────────────────────────────────────────────────┘
                                              ↑
┌────────────────────────────────────────────────────────────────────┐
│ Layer 1: Client → Session 识别 + 5 分钟复用                        │
│   - Client = api_keys 行（HMAC-SHA256 + LRU 缓存）                  │
│   - Session = Redis 持久化（多 header 来源识别）                    │
│   - 5 分钟内同 client 无 id 请求复用 last_system_session            │
└────────────────────────────────────────────────────────────────────┘
```

---

## 三、核心实体

### 3.1 Client（客户端）

```go
// auth/verifier.go:27-43
type KeyInfo struct {
    ID         int    // = ClientID（主键）
    TenantID   string
    ApplicationID *int
    KeyPrefix  string
    // ... 限流、tier、budget 等
}
```

Client **不需要**单独的 `clients` 表——它由 `api_keys` 一行承载。
当 `auth.KeyVerifier.Verify(rawKey)` 成功后即可得到 Client。

### 3.2 Session（会话）

```go
// sessions/session.go:34-46
type Session struct {
    SessionID      string    // "gw_<uuid>"（系统赋值）或客户端提供的 ID
    SessionKey     string
    APIKeyID       int       // = ClientID（归属）
    TenantID       string
    TaskID         string
    Namespace      string    // "gw" 标识系统赋值
    Devices        []Device
    ProviderCache  CacheInfo
    CreatedAt      time.Time
    LastActive     time.Time
    ExpiresAt      time.Time
}
```

**Redis 存储**：`session:<sessionID>` (Hash), TTL 默认 7 天。

### 3.3 LastSystemSessionIndex（5 分钟复用索引）

**用途**：客户端无 session id 时，在 5 分钟窗口内复用系统分配的上一个 session。

```go
// sessions/last_system_session.go
type LastSystemSessionEntry struct {
    SessionID      string    `json:"session_id"`
    LastAssignedAt time.Time `json:"last_assigned_at"`
    DeviceSeed     string    `json:"device_seed,omitempty"`
    TaskID         string    `json:"task_id,omitempty"`
}
```

**Redis Key**：`client:<apiKeyID>:last_system_session` (String JSON), TTL = 5 分钟。

### 3.4 SessionPreferredCredential（会话级偏好）

**用途**：会话级"应该用哪个 credential"的轻量级映射。

```go
// Redis Key
//   session_pref:<sessionID>  (String)
//   value = "<credentialID>"
//   TTL = 7 days
```

**语义**：保留 = 优先复用；删除 = 强制重新选择。
切模型时清空（因为旧 model 的偏好 credential 在新 model 下可能不适用）。

### 3.5 RouteNodeState（路由节点健康状态）

**核心实体**。状态维度 = `(credID, model)`，**不**依附于会话。

```go
// routing/route_node_state.go
type RouteNodeState struct {
    CredentialID  int                  `json:"credential_id"`
    Model         string               `json:"model"`        // credential 匹配后的模型名（cand.RawModel）
    SuccessCount  int64                `json:"success_count"`
    FailureCount  int64                `json:"failure_count"`
    SlideWindow   []RouteNodeRecord    `json:"slide_window"` // 5 分钟滑动窗口
    LastSuccessAt time.Time            `json:"last_success_at"`
    LastFailureAt time.Time            `json:"last_failure_at"`
    // 连续 3 次失败 → 冷却 5 分钟
    Disabled         bool              `json:"disabled"`
    DisabledUntil    time.Time         `json:"disabled_until"`
    DisabledReason   string            `json:"disabled_reason,omitempty"`
}

type RouteNodeRecord struct {
    RequestID string    `json:"request_id"`
    Success   bool      `json:"success"`
    ErrorKind string    `json:"error_kind,omitempty"`
    Timestamp time.Time `json:"timestamp"`
}
```

**Redis Key**：`route_node:<credID>:<model>` (String JSON), TTL = 1 小时。

#### IsUsable 判定规则

```go
func (n *RouteNodeState) IsUsable(now time.Time) bool {
    // 1. 冷却中 → 不可用
    if n.Disabled && now.Before(n.DisabledUntil) {
        return false
    }
    // 2. 冷却到期 → 自动恢复，重置计数
    if n.Disabled && !now.Before(n.DisabledUntil) {
        n.Disabled = false
        n.FailureCount = 0
    }
    // 3. 滑动窗口末尾连续 3 次失败 → 不可用
    return n.ConsecutiveFailureStreak() < 3
}

func (n *RouteNodeState) ConsecutiveFailureStreak() int {
    n.PruneOldRecords(time.Now(), 5*time.Minute)
    streak := 0
    for i := len(n.SlideWindow) - 1; i >= 0; i-- {
        if !n.SlideWindow[i].Success {
            streak++
        } else {
            break
        }
    }
    return streak
}
```

#### 失败计数归属原则

**核心**：状态属于路由节点本身，不属于使用它的会话。

| 场景 | 行为 |
|------|------|
| 同一会话切换到另一个 credential | 旧 credential 上的 RouteNodeState 不变 |
| 同一 credential 被不同会话使用 | 所有会话的失败都累计在该 (credID, model) 上 |
| 同一 credential 切到不同 model | 不同 model 是不同 RouteNodeState，各自独立 |
| 同会话切模型 | 旧 model 的 RouteNodeState 保留；session_pref 清空 |

### 3.6 Slot 双层视图

#### 并发槽（Concurrency slots）
- **数量** = `credentials.concurrency_limit`（默认 20）
- **含义**：每次请求占一个 slot，纯计数许可证
- **状态**：0/1（占用 / 空闲）
- **无身份信息**：不绑定 fingerprint，不绑定 session

#### 指纹槽（FpSlots / Fingerprint slots）
- **数量** = `credentials.fp_slot_limit`（默认 20，老凭据 3-5）
- **含义**：每个 slot 绑定固定 EgressIdentity（IP/MAC/ClientID/UA）
- **共享语义**：同 fingerprint 的多个 in-flight 请求**可共享同一 slot**
- **状态**：`in_flight_count`（可 ≥ 1）
- **绑定信息**：
  - 当前 fingerprint（StickyKey）
  - 当前会话列表（同一 fingerprint 下的多个 session）
  - 占用起始时间
  - 每个 session 的占用时长

#### 关系
- `concurrency_limit ≥ fp_slot_limit`
- 关系示例：`concurrency_limit=20, fp_slot_limit=5`
  - 并发槽有 20 个格子
  - 指纹槽有 5 个格子（"虚拟客户"）
  - 一次请求：占 1 个并发槽 + 占 1 个指纹槽的 inflight
  - 同 fingerprint 的 5 个并发会话：占 5 个并发槽 + 占 1 个指纹槽（in-flight=5）

---

## 四、完整请求流程

### 4.1 入口与认证

```
ChatHandler.ServeHTTP (relay/handler.go)
│
├─ 1. 提取 APIKey (Authorization Bearer / x-api-key)
│     → auth.KeyVerifier.Verify → KeyInfo{ID, TenantID, ...}
│
├─ 2. 【状态/RPM 限流/Budget 检查】
```

### 4.2 会话识别与复用

```
├─ 3. 多 header 解析 sessionID（优先级）
│     X-Gw-Session-Id > X-Session-Id > X-Conversation-Id
│     > X-Chat-Session-Id > X-Thread-Id
│
├─ 4. 【5 分钟复用】sessionID == "" 且 keyInfo != nil
│     ├─ 查 LastSystemSessionIndex.Get(apiKeyID)
│     ├─ 命中 + Redis session 仍存在 → 复用，refresh TTL
│     │   写 X-Gw-Session-Id-Resume / X-Gw-Session-Reused
│     └─ 未命中 → 继续
│
├─ 5. 解析请求 body → ClientModel（客户端请求的模型名）
│
├─ 6. 【同会话切模型检测】
│     读 session_pref:<sessionID> 上一值中的 model
│     ├─ 命中且 model 变化 → 删 session_pref（让 P2C 重选）
│     └─ 未命中或 model 未变 → 保留 session_pref
│
├─ 7. 【no-id 兜底】sessionID == "" 且 sessionInfo == nil
│     ├─ CreateV2 生成 gw_<uuid>
│     ├─ 写 LastSystemSessionIndex（让下一个 no-id 请求能复用）
│     └─ 写 X-Gw-Session-Id-Resume / X-Gw-Session-Auto
```

### 4.3 路由选择

```
├─ 8. provider.GetCandidates(ClientModel) → []Candidate
│     每个 Candidate = { ProviderID, CredentialID, RawModel, ... }
│
├─ 9. 【RouteNodeState 过滤】
│     对每个 Candidate，查 route_node:<credID>:<cand.RawModel>
│     ├─ IsUsable()==true → 保留
│     └─ IsUsable()==false → 排除（记日志）
│
├─ 10.【会话偏好】读 session_pref:<sessionID>
│     ├─ 命中 → 该 credential 排到候选首位（prioritize）
│     └─ 未命中 → 走 P2C 排序
│
└─ 11.输出 ordered candidates → Executor.Execute
```

### 4.4 Slot 分配（双层）

```
└─ 12.【并发槽分配】Limiter.AcquireAll
      ├─ 成功 → 持有 concurrency permit
      └─ 失败 → 拒绝（503 / 排队）
      
└─ 13.【指纹槽分配】FpSlots.Acquire(holder=StickyKey)
      ├─ pin 路径 → 复用同 slot → 同 EgressIdentity
      ├─ 空闲 slot → 占
      └─ 无空闲 + concurrency 还有余量 → 不需要抢（同 fingerprint 复用同一 slot）
      
      关键：同 fingerprint 的多并发会话共享同一 slot（in-flight > 1）
```

### 4.5 上游调用与状态写入

```
└─ 14.注入 egress headers + disguise UA → 上游调用
      │
      ├─ 15.【结果处理】
      │   成功：
      │     ├─ route_node:<credID>:<model>.SuccessCount++
      │     ├─ SlideWindow append 成功记录
      │     ├─ 若 Disabled → 自动恢复
      │     └─ session_pref:<sessionID> = credentialID
      │
      │   失败（credential-level kind）：
      │     ├─ route_node:<credID>:<model>.FailureCount++
      │     ├─ SlideWindow append 失败记录
      │     └─ ConsecutiveFailureStreak >= 3 → Disabled=true
      │
      │   失败（偶发 kind）：
      │     └─ 不影响 RouteNodeState
      │
      │   致命错误（auth/quota permanent）：
      │     ├─ FpSlots.ForceUnpin(holder, credID)
      │     └─ RouteNodeState 不必删（其他 session 还会用到）
      │
      └─ 16.并发槽 Limiter.Release
           指纹槽 FpSlots.Release（不删 slot，只 refresh TTL）
```

---

## 五、数据结构与 Redis Schema

### 5.1 完整 Redis Key 清单（新增项用 🆕 标记）

| Key | 类型 | TTL | 用途 |
|-----|------|-----|------|
| `session:<sessionID>` | Hash | 7d | 会话详情 |
| `session:key:<sessionKey>` | String | 7d | sessionKey → sessionID 反查 |
| `session:apiKey:<id>:active` | Set | 7d | 该 client 的活跃 session 列表 |
| `client:<apiKeyID>:last_system_session` 🆕 | String (JSON) | 5min | 系统赋值 session 5 分钟复用 |
| `session_pref:<sessionID>` 🆕 | String | 7d | 会话偏好的 credentialID |
| `route_node:<credID>:<model>` 🆕 | String (JSON) | 1h | 路由节点健康状态 |
| `sticky_sessions:<stickyKey>` | String | 30min | client 级 sticky 绑定 |
| `llmgw:cred_fp_slot:<credID>:<slotIdx>` | String | 30min | 指纹槽 holder |
| `llmgw:sess_cred_fp:<holder>:<credID>` | String | 24h | pin（24h 内同 holder 复用同 slot） |
| `llmgw:cred_fp_inflight:<credID>:<slotIdx>` 🆕 | Integer | 30min | 指纹槽 in-flight 计数 |

### 5.2 关键 Redis 操作 Lua 脚本

#### acquireSlotScript（V3 改造）

```lua
-- V3: slot 可被同 holder 多 in-flight 请求共享
-- 抢占只在"holder 已主动释放（无 pin）"时发生
local currentHolder = GET slotKey
if not currentHolder then
    -- 空 slot：直接占
    SET slotKey=holder EX=slotTTL
    SET pinKey slotIndex EX=pinTTL
    INCR inflightKey
    return {1, slotIndex, ""}
end
if currentHolder == holder then
    -- 自己的 slot：共享（V3 关键变化）
    EXPIRE slotKey slotTTL
    SET pinKey slotIndex EX=pinTTL
    INCR inflightKey
    return {1, slotIndex, ""}
end
-- 别人的 slot：检查 pin
local pinExists = EXISTS pinKey_for_other_holder
if pinExists then
    -- 别人的 pin 仍存在 → 不抢
    return {0, "", currentHolder}
end
-- 别人已释放（无 pin）：可抢
SET slotKey=holder EX=slotTTL
SET pinKey slotIndex EX=pinTTL
INCR inflightKey
return {1, slotIndex, currentHolder}
```

#### releaseSlotScript（V3 改造）

```lua
-- V3: Release 减少 inflight，inflight==0 时清 pin
local slotKey = KEYS[1]
local pinKey = KEYS[2]
local inflightKey = KEYS[3]
local holder = ARGV[1]

local current = GET slotKey
if current ~= holder then
    return 0  -- 已被抢
end

local remaining = DECR inflightKey
if remaining < 0 then
    SET inflightKey 0
    remaining = 0
end
EXPIRE inflightKey slotTTL

if remaining == 0 then
    -- V3: 所有请求都已完成，清 pin（让其他人可以抢）
    -- 但保留 slot key 30min TTL，给同 holder 复用
    DEL pinKey
end

EXPIRE slotKey slotTTL
return remaining
```

---

## 六、界面展示

### 6.1 双层 Slot 图示

凭据详情页（如 `https://llmgateway.internal.example.com/providers/{id}/credentials/{credID}`）展示：

```
┌─────────────────────────────────────────────────────────┐
│  并发槽位（Concurrency Slots, 总数 = concurrency_limit） │
├─────────────────────────────────────────────────────────┤
│  [█][█][█][█][█][█][ ][█][█][ ][█][█][█][ ][ ][█][█][█][ ][█][█]│
│   0  1  2  3  4  5  6  7  8  9 10 11 12 13 14 15 16 17 18 19│
│  已用: 14 / 20                                          │
└─────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────┐
│  指纹槽位（Fingerprint Slots, 总数 = fp_slot_limit）    │
├─────────────────────────────────────────────────────────┤
│  Slot 0  FP:fp-A  In-flight: 2                         │
│    ├─ 客户 A (key_42) 会话 s1  占用 12m                 │
│    └─ 客户 A (key_42) 会话 s2  占用 8m                  │
│                                                         │
│  Slot 1  FP:fp-B  In-flight: 0  (空闲)                 │
│                                                         │
│  Slot 2  FP:fp-C  In-flight: 5                         │
│    ├─ 客户 B (key_77) 会话 s5  占用 25m                 │
│    ├─ 客户 B (key_77) 会话 s6  占用 25m                 │
│    ├─ 客户 D (key_99) 会话 s7  占用 18m                 │
│    ├─ 客户 B (key_77) 会话 s8  占用 10m                 │
│    └─ 客户 D (key_99) 会话 s11 占用 2m                  │
│                                                         │
│  Slot 3  FP:fp-D  In-flight: 0  (空闲)                 │
│                                                         │
│  Slot 4  FP:fp-E  In-flight: 1                         │
│    └─ 客户 C (key_55) 会话 s9  占用 5m                  │
└─────────────────────────────────────────────────────────┘
```

### 6.2 每个指纹槽显示字段

| 字段 | 来源 | 含义 |
|------|------|------|
| Slot Index | `llmgw:cred_fp_slot:<credID>:<idx>` | 槽位编号 0..fp_slot_limit-1 |
| FP EgressSeed | `egress.EgressSeed = "llmgw-cred%d-fp%d"` | 该 slot 绑定的指纹 |
| In-flight Count | `llmgw:cred_fp_inflight:<credID>:<idx>` | 当前 in-flight 请求数 |
| Client 信息 | `holder` 反查 → api_keys | 客户 ID / key 前缀 |
| Session ID | session 关联查询 | 该 slot 上活跃的会话 |
| 占用时长 | session.LastActive - now | 已占用时间 |

---

## 七、关键决策与权衡

### 7.1 为什么状态维度是 `(credID, model)` 而不是 `(sessionID, ...)`

**理由**：失败计数反映的是"这个路由节点本身的健康度"，不是"使用它的会话的健康度"。

反例：如果失败计数在 session 上，session 切换时旧节点会被清空，掩盖真实问题。

### 7.2 为什么 Sticky Key 保持 client-scoped

**理由**：
1. Sticky Key 同时作为 fp_slot holder，决定反爬伪装身份
2. 同一 client 在反爬层面必须稳定（防止 TLS/UA 指纹漂移）
3. 模型/会话级别路由通过 `(credID, model)` 维度的 RouteNodeState + `session_pref` 实现

### 7.3 Slot 语义为什么改为"并发许可位"

**理由**：
1. `concurrency_limit=20, fp_slot_limit=5` 的凭据需要 5 个指纹承担 20 个并发
2. 同一个指纹的多个 in-flight 请求应共享同一 slot（避免身份数量膨胀）
3. 抢占只在"原 holder 完全释放（无 pin）"时进行

### 7.4 为什么失败计数归属在节点上而非全局

**理由**：
1. 同一个 (credID, model) 上不同请求的失败反映该节点的真实健康度
2. 全局 HealthTracker（`credentialhealth`）已有，但它是粗粒度的（80% over 1h）
3. RouteNodeState 是细粒度的（5min 窗口 + 连续 3 次），用于会话内的快速切换

---

## 八、配置项

```go
// 路由节点健康状态（新增）
RouteNodeWindowSeconds       = 300   // 滑动窗口大小
RouteNodeFailStreakLimit     = 3     // 连续失败阈值
RouteNodeDisabledCooldown    = 300   // 禁用后冷却时间
RouteNodeStateTTLSeconds     = 3600  // RouteNodeState Redis TTL

// 会话偏好（新增）
SessionPreferenceTTLHours    = 168   // 与 session 同步

// LastSystemSession（新增）
LastSystemSessionTTLSeconds  = 300   // 5 分钟复用窗口

// 多 header 会话识别（新增）
SessionHeadersPriority = []string{
    "X-Gw-Session-Id",
    "X-Session-Id",
    "X-Conversation-Id",
    "X-Chat-Session-Id",
    "X-Thread-Id",
}
```

---

## 九、实施计划

| Phase | 内容 | 状态 |
|-------|------|------|
| Phase 1 | LastSystemSessionIndex + 多 header 会话识别 | ✅ 已完成 |
| Phase 2 | RouteNodeState 数据结构 + Redis 存储 + IsUsable 判定 | ⏳ 进行中 |
| Phase 3 | PlanCandidates 集成 RouteNodeState 过滤 + session 偏好 | ⏳ 待开始 |
| Phase 4 | recordRouteNodeSuccess/Failure 替换部分 recordStickyFailure | ⏳ 待开始 |
| Phase 5 | 同会话切模型检测 + 清 session_pref | ⏳ 待开始 |
| Phase 6 | FpSlot V3 重设计（slot=并发许可位 + 共享语义 + 双层计数） | ⏳ 待开始 |
| Phase 7 | 双层 SlotInfo（指纹槽 + 并发槽）+ admin API + SlotInfoCard.vue | ⏳ 待开始 |
| Phase 8 | 测试 + 监控指标 + 文档 | ⏳ 待开始 |

---

## 十、风险与缓解

| 风险 | 缓解措施 |
|------|---------|
| RouteNodeState Redis 不可用 | 降级为"无状态"，仅依赖 HealthTracker 全局健康度 |
| SessionPref Redis 不可用 | 降级为无偏好，P2C 自由选择 |
| LastSystemSession Redis 不可用 | 降级为每次都新建 sessionID |
| FpSlot in-flight 计数与实际 in-flight 不一致（异常退出） | TTL=slotTTL(30min)，最终一致性；Release 用 Lua 原子 |
| 同 fingerprint 多 in-flight 导致反爬不严 | 通过 fp_slot_limit 控制"虚拟用户数"，与并发数解耦 |
| inflight 计数 key 与 slot key TTL 不同步 | 共用 30min TTL，EXPIRE 同步 |
| 双层 SlotInfo 查询慢 | 加 5s 缓存；SCAN 限定 prefix |

---

## 十一、附录

### 11.1 现有架构参考

- `relay/handler.go:579-650`：现有 session 识别（X-Gw-Session-Id + X-Session-Id）
- `routing/sticky.go:176-189`：现有 `BuildClientStickyKey`（client-scoped）
- `routing/router.go:31-80`：现有 `PlanCandidates`
- `routing/executor.go:1617-1661`：现有 `recordStickySuccess/Failure`
- `credentialfpslot/slot.go`：现有 fp_slot 实现
- `auth/verifier.go:27-43`：现有 KeyInfo 结构

### 11.2 相关历史文档

- `docs/2026-06-23-fp-slot-reset-feature.md`：指纹槽复位功能
- `docs/2026-06-15-auto-route-mode-design.md`：自动路由模式设计
- `docs/session-to-memora-pipeline.md`：会话到 Memora 流水线
- 代码注释 `routing/sticky.go:156-175`：client-scoped 决策记录（2026-06-24）
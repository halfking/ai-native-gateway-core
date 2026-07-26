# Token 资源管理体系说明

> **文档版本**: v1.1（2026-07-27 二次审计修正）
> **创建日期**: 2026-07-27
> **适用范围**: FpSlot（凭证身份池）+ credential.Limiter（凭证并发限流）的客户端 token 化语义层
> **关联文档**: [00-完整方案文档.md](./00-完整方案文档.md)、[31-当前实现基线与修正决策.md](./31-当前实现基线与修正决策.md)、[42-客户端类型识别与unknown回退.md](./42-客户端类型识别与unknown回退.md)
>
> ⚠️ **v1.1 勘误**：v1.0 版本错误地把 `domains/ursm/conc_slot_manager.go`（`ConcurrencySlotManager`）
> 描述为生产中生效的"并发 token"载体。经二次审计确认：该模块的 `CheckAndAcquire` **在生产代码中
> 从未被调用**（仅在自身单元测试中使用），实际生效的并发限流是 `domains/credential/limiter.go`
> 的 `Limiter.AcquireAll`（内存 semaphore：global → provider pool → credential → 可选 identity
> soft cap → 可选 per-key soft cap，见 `executor.go:2011`）。本版本已修正全部相关描述。

## 1. 背景与目标

### 1.1 现有体系回顾

LLM Gateway 在路由层已经维护资源池，用于防止"虚拟身份泄露"（同一凭证被高频识别为同一用户触发厂商风控）以及"凭证并发过载"（同一凭证被过多请求挤占）：

| 池 | 模块 | 维度 | 作用 |
|----|------|------|------|
| **FpSlot**（指纹槽，Redis） | `credentialfpslot/slot.go` | `(holder, credentialID)` | 一个凭证能模拟多少个虚拟身份 |
| **Limiter**（并发限流，内存） | `domains/credential/limiter.go` | `(providerID, credentialID)` 级 semaphore，叠加 `identityHash` soft cap、`keyID` soft cap | 一个凭证同时能服务多少在飞请求 |

> ⚠️ **注意**：`domains/ursm/conc_slot_manager.go` 定义了一套 Redis 版并发槽（`ConcurrencySlotManager.CheckAndAcquire`），维度为 `(sessionID, credentialID)`。**该模块未被任何生产路径调用**（`grep` 确认仅出现在其自身单元测试中），`domains/ursm/routing.go` 的注释也明确说明"实际资源门控仍在成熟的 executor 路径（FpSlots + Limiter）"。本文档后续涉及"并发 token"均指 `credential.Limiter`，不涉及这套未接入的 ConcSlot 代码。是否复活/接入 ConcSlot 是独立的架构决策，不在本轮范围。

### 1.2 现有体系的不足

1. **holder 仅绑定 session**：用户从 Cursor 切换到 Claude-Code 会被视为同一 holder，挤占同一身份槽
2. **没有"客户端"概念**：运维无法回答"该凭证被哪种客户端占用最多"、"是否某客户端抢占了过多槽位"
3. **配额粒度粗**：无法独立调节"每凭证对 Cursor 的并发上限"和"每凭证对 claude-code 的并发上限"

### 1.3 Token 资源管理目标

**在不重写 Redis 脚本的前提下**，引入"客户端 token"语义层：

- 客户端 = `userKey + clientType` 二元组
- 同一用户在多种客户端之间互不挤占槽位
- 未识别客户端类型归类 "unknown"，汇总到通用统计
- 为后续"每凭证每客户端独立配额"提案留出扩展点

---

## 2. Token 模型

### 2.1 三种 Token 的定义

```
┌──────────────────────────────────────────────────────────────┐
│                     Token 资源体系（v2.1）                    │
│                                                              │
│   请求 token                                                  │
│   ─────────                                                  │
│   含义：一次 HTTP 请求生命周期内对凭证资源的占用权             │
│   持有者：FpSlot.Acquire 返回的 Lease                         │
│   生命周期：单次请求；Release 在请求结束 / 失败 / 取消时释放   │
│   代码位置：executor.go:1964 (Acquire)                        │
│            executor.go:2005/2026/2058 (Release)               │
│                                                              │
│   客户端 token                                                │
│   ─────────                                                  │
│   含义：跨多次请求持续存在的"客户端身份"，可复用 slot          │
│   持有者：FpSlot Pin (24h TTL)                                │
│   生命周期：连续 24h 活跃；过期后下一次请求重拿 slot           │
│   格式：{userKey}|{clientType}                                │
│                                                              │
│   并发 token                                                  │
│   ─────────                                                  │
│   含义：单请求在飞行期内占用的并发额度                        │
│   持有者：credential.Limiter 内存 semaphore                  │
│           （global → provider pool → credential，            │
│            叠加 identityHash / keyID 的非阻塞 soft cap）      │
│   生命周期：与请求同步；ReleaseFunc 在请求结束时释放           │
│   代码位置：domains/credential/limiter.go:376 (AcquireAll)    │
│            executor.go:2011 (调用点)                          │
└──────────────────────────────────────────────────────────────┘
```

> 本文档不涉及 `identityHash` 的 holder 拼接改造——`Limiter.AcquireAll` 的 `identityHash` 参数直接使用 `params.ClientID.IdentityHash`（未改动），与本轮 FpSlot holder 改造是两个独立维度，互不影响。

### 2.2 客户端的精确定义

**客户端 = `userKey + clientType`**

| 字段 | 来源 | 兜底 |
|------|------|------|
| `userKey` | `params.StickyKey`（`buildRouteStickyKey` 派生的 `{tenant}:{app}:{apiKeyID}:{profile}` 稳定身份）→ `X-Request-Id` 头 | `"anon"` |
| `clientType` | `extractClientType(r)`（X-Gw-Client-Type 头 → User-Agent 匹配） | `"unknown"` |

> ⚠️ **不使用 `params.ClientID.IdentityHash`**：IdentityHash 是设备/环境指纹哈希（`PrimarySeed` 优先取 DeviceSeed/MachineID，否则退化为 UA+OS+Arch+RuntimeName+RuntimeVersion 组合哈希），会随客户端版本升级、系统更新等环境变化而漂移。若用它做 userKey，会导致同一用户在环境指纹变化时意外换 holder，丢失 24h Pin 复用，与"同用户复用同一身份槽"目标相悖。IdentityHash 仍用于 `IdentityPool`（全局身份数量上限）与 `Limiter`（并发限流），是与 holder 拼接独立的维度。

**拼接格式**：`{userKey}|{clientType}`，例：
- `alice1234|cursor`
- `bob|claude-code`
- `anon|unknown`

### 2.3 Holder 拼接规则

```go
// domains/streaming/executors/executor.go (2026-07-27)
userKey := params.StickyKey
if userKey == "" {
    userKey = params.R.Header.Get("X-Request-Id")
}
clientType := extractClientType(params.R)
holder := clientTokenOf(userKey, clientType)  // = "{userKey}|{clientType}"
```

同一个 helper 同时在 `domains/streaming/client_fingerprint.go:ClientTokenOf` 与 `domains/streaming/executors/executor.go:clientTokenOf` 存在。两份实现刻意重复以保持 `executors` 包零外部依赖；维护时需同步演进。

---

## 3. Redis Key 格式（不变）

FpSlot 相关 Redis key 沿用既有 `credentialfpslot` 模板，**唯一变化是 holder 字符串内容**：

| Key | 模板 | 示例 |
|-----|------|------|
| FpSlot 槽位 | `llmgw:tenant:{tenant}:cred_fp_slot:{credID}:{slotIndex}` | `llmgw:tenant:default:cred_fp_slot:42:3` |
| FpSlot Pin | `llmgw:tenant:{tenant}:sess_cred_fp:{holder}:{credID}` | `llmgw:tenant:default:sess_cred_fp:alice1234\|cursor:42` |

**注意**：holder 字符串含 ASCII `|`，Redis Lua 脚本将其视为普通字符串，无须转义。

`credential.Limiter` 的并发限流是纯内存 semaphore（无 Redis key），因此不受本轮 holder 拼接改造影响。`domains/ursm/keys.go` 中定义的 `concSlotKey` / `concSessionKey` 模板属于未接入生产的 `ConcurrencySlotManager`（见 §1.1 勘误），不在本方案讨论范围。

---

## 4. Lua 脚本（不变）

FpSlot 相关脚本 `acquireSlotScript`、`acquireLRUScript`、`releaseSlotScript`（均在 `credentialfpslot/slot.go`）全部沿用既有版本，未做任何修改。holder 字符串变长不影响脚本语义：

- `GET slotKey` → 字符串匹配仍按 bytewise
- TTL 计算仍按 `(slotTTL - remaining)`
- Pin SET/EX 仍按单一字符串

（`domains/ursm/conc_slot_scripts.go` 中的 `acquireConcurrencyScript` 属于未接入生产的 `ConcurrencySlotManager`，见 §1.1 勘误，不受本轮改动涉及。）

---

## 5. 用户行为变化

### 5.1 同一用户、同一客户端（最常见）

请求序列：
```
T+0:   alice|cursor  → 占 slot 0, pin TTL 24h
T+30:  alice|cursor  → 命中 pin, 复用 slot 0
T+1h:  alice|cursor  → 命中 pin, 复用 slot 0
```

**行为**：与之前一致，pin 复用机制保证 24h 内 identity 稳定。

### 5.2 同一用户、切换客户端（新行为）

请求序列：
```
T+0:   alice|cursor         → 占 slot 0, pin alice|cursor=0
T+1h:  alice|claude-code    → 无 pin，新占 slot 1, pin alice|claude-code=1
T+2h:  alice|cursor         → 命中旧 pin, 复用 slot 0
T+3h:  alice|claude-code    → 命中新 pin, 复用 slot 1
```

**行为**：两个客户端各占独立 slot。这避免了原来"切换客户端挤占同一 slot"导致的厂商风控风险。

### 5.3 未识别客户端（新行为）

请求序列：
```
T+0:   alice|unknown  → 占 slot 0, pin alice|unknown=0
T+1h:  alice|unknown  → 命中 pin, 复用 slot 0
T+2h:  alice|         → 同上，因为空字符串被兜底为 "unknown"
```

**行为**：所有未识别客户端汇总到 `unknown` 槽位，可监控但不影响识别客户端的独立性。

---

## 6. 可观测性

### 6.1 Prometheus 指标（设计目标，本轮未实施）

```yaml
# Counter
gateway_client_token_requests_total{tenant_id, client_type, outcome}
  # outcome = acquired | saturated | error | degraded

gateway_client_token_holder_changes_total{tenant_id, client_type}
  # 同一 userKey 切 clientType 时累计

# Gauge
gateway_client_token_active_slots{tenant_id, credential_id, client_type}

gateway_client_token_unknown_ratio
  # 未识别 clientType 在所有请求中的占比（应该 < 5%）

# Histogram
gateway_client_token_pin_age_seconds{tenant_id, client_type}
  # pin 复用率指标：age 越小复用越多
```

### 6.2 结构化日志（已部分实施）

所有 `cred_fp_slot` 相关日志已经/应该增加 `client_type` 字段：

```go
slog.Info("cred_fp_slot acquired",
    "credential_id", lease.CredentialID,
    "slot", lease.SlotIndex,
    "client_type", clientType,    // NEW
    "holder", lease.Holder,       // 现在是 "{userKey}|{clientType}"
)
```

### 6.3 告警规则建议

| 告警 | 表达式 | 触发条件 |
|------|--------|---------|
| `ClientTokenUnknownHigh` | `rate(...outcome="acquired", client_type="unknown") / rate(...outcome="acquired") > 0.3` | 未识别客户端占比超 30%，可能客户端 UA 已变更 |
| `ClientTokenPinMiss` | `rate(gateway_client_token_pin_age_seconds{quantile="0.5"} > 86400)` | Pin 复用率下降 |

---

## 7. 后续提案：每凭证每客户端配额表

### 7.1 设计动机

Token 资源管理仅做到"客户端独立 holder"是不够的。当某凭证被某客户端过度占用，仍可能挤占其他用户。**下一步可设计独立配额表**：

```sql
CREATE TABLE gateway.credential_client_quota (
    credential_id INT NOT NULL,
    client_type VARCHAR(64) NOT NULL,
    max_concurrent INT NOT NULL DEFAULT 50,
    max_fp_slots INT NOT NULL DEFAULT 20,
    PRIMARY KEY (credential_id, client_type)
);
```

### 7.2 运行时计数（设计）

```yaml
# Redis Hash, 5min 滑动窗口
llmgw:cred_client_count:{credID}:{clientType} → INCR / DECR
```

### 7.3 调度顺序（设计）

```
1. 提取 clientType
2. 查 credential_client_quota → 得到 max_concurrent
3. Redis INCR llmgw:cred_client_count:{credID}:{clientType}
4. 若超过 max_concurrent → 拒绝路由 / 降级到下一候选
5. 请求结束 → DECR
```

### 7.4 与现有 Limiter 的关系

- **Limiter（现有，生产生效）**：`domains/credential/limiter.go`，以 `(providerID, credentialID)` 维度做内存 semaphore，限"凭证并发"；叠加 `identityHash`/`keyID` 的非阻塞 soft cap
- **ClientQuota（提案）**：以 `clientType × credentialID` 维度计数，限"客户端并发"
- **两者并存**：客户端配额命中但 Limiter 还有余 → 通过；Limiter 命中但客户端配额还有余 → 仍会在 Limiter 层拒绝（Limiter 是阻塞式硬限）

**当前状态**：本轮不做此实现，作为 `32-审计发现与改进计划.md §3 Phase 4` 跟踪。ClientQuota 若要落地，需先决定其在调度顺序中相对 Limiter 的位置（建议 FpSlot → ClientQuota → Limiter，让客户端级配额先行拦截，减少 Limiter 层的无效等待）。

---

## 8. 测试覆盖

### 8.1 已覆盖

| 测试 | 文件 | 验证 |
|------|------|------|
| `TestClientTokenOf_Defaults` | `client_fingerprint_test.go` | 空 userKey / 空 clientType 回退 anon/unknown |
| `TestClientTokenOf_StableFormat` | `client_fingerprint_test.go` | 同一输入多次拼接结果一致 |
| `TestExtractClientType_*` | `client_fingerprint_test.go` | header / UA / 关键词匹配 |

### 8.2 测试缺口（建议）

| 场景 | 建议测试 |
|------|---------|
| executor holder 拼接集成 | mock params + 调 Execute，验证传给 Acquire 的 holder 格式 |
| 同一用户切换客户端 | 连续两次 Execute with different UA，验证得到不同 slot |
| Pin 复用仍按 holder 字符串 | 模拟 Redis 中已存在 pin=0，调 Acquire 验证仍拿到 slot 0 |

---

## 9. 回滚策略

- 代码改动独立 commit `feat(token-client): holder = userKey|clientType`
- 文档改动与代码同 PR，但代码可独立回滚
- 回滚后 holder 退化为 `params.StickyKey`（旧行为），无数据迁移需求

---

**最后更新**: 2026-07-27
**下次审查**: Phase 4（ClientQuota 表）实现后
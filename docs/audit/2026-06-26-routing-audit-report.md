# llm-gateway-go 路由架构审计报告

> **审计日期**: 2026-06-26  
> **审计范围**: Client / Session / RouteNode 三层路由设计 vs 当前实现  
> **参考文档**: `2026-06-26-session-routing-redesign.md` (V3.1)

---

## 一、审计摘要

| 类别 | 设计要求 | 当前状态 | 差距 |
|------|----------|----------|------|
| **Sticky 三层路由** | L1(session+model) → L2(client+model) → L3(client) | ✅ 已实现 | 无 |
| **RouteNodeState** | `(credID, model)` 维度健康状态 | ❌ 未实现 | **缺失核心模块** |
| **LastSystemSession** | 5分钟会话复用索引 | ❌ 未实现 | **缺失核心模块** |
| **SessionPreferredCredential** | 会话级偏好凭据 | ❌ 未实现 | **缺失 Redis key** |
| **切模型检测** | 同会话切模型时清空 session_pref | ❌ 未实现 | **缺失逻辑** |
| **FpSlot V3** | 双层 slot（指纹槽 + 并发槽） | ⚠️ 需验证 | 待审查 credentialfpslot/ |
| **多 header 会话识别** | 5个 header 优先级解析 | ⚠️ 需验证 | 待审查 relay/handler.go |

---

## 二、详细差异分析

### 2.1 RouteNodeState（❌ 完全缺失）

**设计要求**：
```go
// routing/route_node_state.go (应有，但不存在)
type RouteNodeState struct {
    CredentialID  int
    Model         string  // credential 匹配后的模型名
    SuccessCount  int64
    FailureCount  int64
    SlideWindow   []RouteNodeRecord  // 5分钟滑动窗口
    LastSuccessAt time.Time
    LastFailureAt time.Time
    Disabled      bool
    DisabledUntil time.Time
}

// Redis Key: route_node:<credID>:<model>
// TTL: 1h
```

**当前实现**：
- ❌ 文件不存在：`routing/route_node_state.go`
- ❌ Redis key `route_node:<credID>:<model>` 未使用
- ⚠️ 现有的失败计数逻辑在 `routing/sticky.go` 的 `RecordFailure`，但绑定在 sticky key 上，不是 `(credID, model)` 维度

**影响**：
1. 无法按 `(credID, model)` 维度跟踪节点健康度
2. 会话切换或切模型时，旧节点的失败状态会丢失
3. 连续失败阈值（3次）→ 冷却5分钟的逻辑无法实施
4. `PlanCandidates` 无法过滤掉 `IsUsable()==false` 的节点

---

### 2.2 LastSystemSessionIndex（❌ 完全缺失）

**设计要求**：
```go
// sessions/last_system_session.go (应有，但不存在)
type LastSystemSessionEntry struct {
    SessionID      string
    LastAssignedAt time.Time
    DeviceSeed     string
    TaskID         string
}

// Redis Key: client:<apiKeyID>:last_system_session
// TTL: 5min
```

**当前实现**：
- ❌ 文件不存在：`sessions/last_system_session.go`
- ❌ Redis key `client:<apiKeyID>:last_system_session` 未使用
- ⚠️ 现有的 `session_v2.go` 有 `CreateV2` 方法，但没有5分钟复用逻辑

**影响**：
1. 客户端无 session id 时，每次都会新建 session（文档要求在5分钟内复用）
2. 无法满足"客户端零 ID 友好"的业务诉求
3. 会导致大量短期 session 碎片

---

### 2.3 SessionPreferredCredential（❌ 完全缺失）

**设计要求**：
```go
// Redis Key: session_pref:<sessionID>
// Value: "<credentialID>" (String)
// TTL: 7 days
```

**当前实现**：
- ❌ Redis key `session_pref:<sessionID>` 未使用
- ⚠️ 现有的 sticky 三层路由（L1/L2/L3）已实现，但与 session_pref 是**互补**关系，不是替代关系

**设计意图**：
- `session_pref` 用于**轻量级**会话偏好（只存 credentialID）
- sticky 三层用于**反爬伪装**（fp_slot holder，决定 TLS/UA 指纹）
- 两者配合：session_pref 优先级 > sticky L1 > L2 > L3

**影响**：
1. `PlanCandidates` 无法实现"优先 SessionPreferredCredential"逻辑
2. 同会话切模型时，无法通过清空 session_pref 来强制重新选择

---

### 2.4 同会话切模型检测（❌ 未实现）

**设计要求**（流程第 6 步）：
```go
// relay/handler.go 应在路由前检测模型变化
// 1. 读 session_pref:<sessionID> 上一值中的 model
// 2. 比较 clientModel vs 上一次的 model
// 3. 如果不同 → DEL session_pref:<sessionID>
```

**当前实现**：
- ❌ relay/handler.go 中无此逻辑
- ⚠️ 现有的 sticky 三层路由在切模型时会自然走到 L2（client+model），但不会清空 session_pref（因为 session_pref 根本不存在）

**影响**：
1. 切模型后，session_pref 仍指向旧 credential（可能不支持新 model）
2. 会导致路由失败或回退到次优选择

---

### 2.5 FpSlot V3 改造（⚠️ 需验证）

**设计要求**：
- Slot 语义从"独占身份"改为"并发许可位"
- 同 fingerprint 的多 in-flight 请求**共享同一 slot**
- 新增 Redis key: `llmgw:cred_fp_inflight:<credID>:<slotIdx>` (Integer, 30min TTL)
- Lua 脚本改造：acquireSlotScript / releaseSlotScript

**待审查文件**：
- `credentialfpslot/slot.go`
- `credentialfpslot/slot_test.go`

**审查要点**：
1. ✅ `acquireSlotScript` 是否支持同 holder 多次 acquire（V3 共享语义）？
2. ✅ `releaseSlotScript` 是否用 DECR 减少 inflight，而非直接清空？
3. ✅ `llmgw:cred_fp_inflight:<credID>:<slotIdx>` key 是否存在？
4. ✅ Redis TTL 是否统一为 30min（slot + inflight + pin）？

---

### 2.6 多 header 会话识别（⚠️ 需验证）

**设计要求**（流程第 3 步）：
```go
// 优先级：
SessionHeadersPriority = []string{
    "X-Gw-Session-Id",
    "X-Session-Id",
    "X-Conversation-Id",
    "X-Chat-Session-Id",
    "X-Thread-Id",
}
```

**待审查文件**：
- `relay/handler.go` (ChatHandler.ServeHTTP)

**审查要点**：
1. ✅ 是否按优先级解析多个 header？
2. ✅ 是否写回 `X-Gw-Session-Id-Resume` / `X-Gw-Session-Reused`？
3. ✅ 是否在 no-id 时调用 LastSystemSessionIndex.Get？

---

## 三、风险评估

| 风险项 | 严重性 | 影响 |
|--------|--------|------|
| **RouteNodeState 缺失** | 🔴 高 | 无法实现连续失败→冷却的核心逻辑；节点故障无法自动隔离 |
| **LastSystemSession 缺失** | 🟡 中 | 无 session id 的客户端会产生大量碎片会话；影响 prompt cache 命中率 |
| **SessionPref 缺失** | 🟡 中 | 切模型后可能路由到不支持的 credential；影响成功率 |
| **切模型检测缺失** | 🟡 中 | 与 SessionPref 缺失叠加，影响切模型场景的稳定性 |
| **FpSlot V3 未改造** | 🟠 中高 | 如果仍是 V2 独占语义，`concurrency_limit=20, fp_slot_limit=5` 的凭据只能承载 5 并发，浪费容量 |
| **多 header 识别未完善** | 🟢 低 | 兼容性问题，不影响核心功能 |

---

## 四、实施优先级

### Phase 0: 审查现有代码（当前任务）
- [x] router.go, sticky.go 已审查
- [ ] credentialfpslot/slot.go（FpSlot V3）
- [ ] relay/handler.go（会话识别 + 切模型检测）
- [ ] routing/executor.go（失败计数逻辑）

### Phase 1: 补齐缺失核心模块（P0，阻塞后续 Phase）
1. **RouteNodeState** (`routing/route_node_state.go`)
   - 数据结构 + Redis 存储 + IsUsable 判定
   - `recordRouteNodeSuccess/Failure` 方法
   - 单元测试（IsUsable / ConsecutiveFailureStreak / 冷却恢复）

2. **LastSystemSessionIndex** (`sessions/last_system_session.go`)
   - 数据结构 + Redis Get/Set
   - 5分钟 TTL 逻辑
   - 单元测试（命中/未命中/过期）

3. **SessionPreferredCredential** (Redis 操作封装)
   - `sessions/session_pref.go`（可选，或直接在 relay/handler.go 内联）
   - Get/Set/Delete 方法
   - 7天 TTL

### Phase 2: 集成到路由流程（P0，依赖 Phase 1）
1. **relay/handler.go 改造**
   - 多 header 会话识别（第 3 步）
   - 5分钟复用逻辑（第 4 步）
   - 同会话切模型检测（第 6 步）
   - no-id 兜底（第 7 步）

2. **routing/router.go 改造**
   - `PlanCandidates` 集成 RouteNodeState 过滤（第 9 步）
   - 优先 SessionPreferredCredential（第 10 步）

3. **routing/executor.go 改造**
   - `recordRouteNodeSuccess/Failure` 替换部分 `recordStickyFailure`（第 15 步）
   - 写 session_pref（成功时，第 15 步）
   - 清 session_pref（切模型时，第 6 步）

### Phase 3: FpSlot V3 改造（P1，独立分支）
1. **credentialfpslot/slot.go 改造**
   - Lua 脚本改为共享语义（acquireSlotScript / releaseSlotScript）
   - 新增 inflight 计数 key
   - 统一 TTL = 30min

2. **测试验证**
   - 同 holder 多次 acquire 测试
   - inflight 计数准确性测试
   - 抢占逻辑测试（pin 存在 vs 不存在）

### Phase 4: 测试与监控（P1，所有 Phase 完成后）
1. 单元测试覆盖率 ≥ 80%
2. e2e 测试场景：
   - 同会话同模型 → L1 命中
   - 同会话切模型 → session_pref 清空 → P2C 重选
   - 连续失败3次 → 节点冷却5分钟 → 自动恢复
   - no-id 客户端 5分钟内复用同一 session
3. 监控指标：
   - `sticky_level_hit_count{level=L1|L2|L3}`
   - `route_node_disabled_count`
   - `session_pref_clear_count{reason=model_switch}`
   - `last_system_session_reuse_count`

---

## 五、建议

### 5.1 立即行动
1. ✅ **Phase 0 完成审查**（credentialfpslot + relay/handler + routing/executor）
2. ✅ **确认设计文档为权威**（V3.1，2026-06-26）
3. ✅ **创建 Phase 1 实施分支**（feature/route-node-state）
4. ✅ **TDD 开发**（先写测试，再写实现）

### 5.2 风险缓解
1. **RouteNodeState 缺失风险**：当前依赖 `credentialhealth` 粗粒度健康度（80% over 1h），无法做到 5min 窗口 + 连续3次的细粒度切换 → **优先级 P0**
2. **FpSlot V2→V3 改造风险**：如果 V3 改造失败，回退到 V2 语义；但需要明确告知用户 `concurrency_limit > fp_slot_limit` 时的容量限制 → **优先级 P1，可延后**
3. **数据一致性风险**：RouteNodeState 与 Sticky 三层路由同时存在，需要明确两者的关系：
   - RouteNodeState：节点健康度（`(credID, model)` 维度）
   - Sticky 三层：反爬伪装 + 会话连续性（client-scoped holder）
   - 两者**互补**，不是替代关系

### 5.3 文档补充
1. 在 `docs/llm-gateway-go/` 新增：
   - `route-node-state-design.md`（RouteNodeState 详细设计）
   - `last-system-session-design.md`（5分钟复用详细设计）
   - `session-pref-vs-sticky.md`（两者关系澄清）
2. 在 `docs/architecture/ARCHITECTURE.md` 补充三层路由的架构图

---

## 六、附录：代码审查清单

### A. credentialfpslot/slot.go（待审查）
- [ ] `acquireSlotScript` 是否支持同 holder 多次 acquire？
- [ ] `releaseSlotScript` 是否用 DECR 减少 inflight？
- [ ] `llmgw:cred_fp_inflight:<credID>:<slotIdx>` key 是否存在？
- [ ] Redis TTL 是否统一为 30min？
- [ ] 抢占逻辑是否检查 pin 存在性？

### B. relay/handler.go（待审查）
- [ ] 多 header 会话识别是否按优先级解析？
- [ ] 是否写回 `X-Gw-Session-Id-Resume` / `X-Gw-Session-Reused`？
- [ ] no-id 时是否调用 LastSystemSessionIndex.Get？
- [ ] 是否在路由前检测模型变化？
- [ ] 切模型时是否清空 session_pref？

### C. routing/executor.go（待审查）
- [ ] 是否有 `recordRouteNodeSuccess/Failure` 方法？
- [ ] 失败计数是否归属 `(credID, model)` 而非 sticky key？
- [ ] 成功时是否写 session_pref？
- [ ] 是否调用 RouteNodeState.IsUsable() 过滤候选？

---

## 七、结论

当前实现与设计文档（V3.1）的差距主要在于**核心模块缺失**：
1. **RouteNodeState**（节点健康状态）
2. **LastSystemSessionIndex**（5分钟会话复用）
3. **SessionPreferredCredential**（会话偏好凭据）

这三个模块是设计文档的**核心**，缺失会导致：
- 无法实现连续失败→冷却的自动故障隔离
- 无 session id 的客户端会产生大量碎片会话
- 切模型场景的路由稳定性不足

**建议立即启动 Phase 1 实施**，优先补齐这三个核心模块，再进行 Phase 2 集成。Phase 3（FpSlot V3）可以延后到 P1。

---

**审计人**: AI Agent  
**审计时间**: 2026-06-26  
**下一步**: 审查 credentialfpslot/slot.go + relay/handler.go + routing/executor.go，完成 Phase 0

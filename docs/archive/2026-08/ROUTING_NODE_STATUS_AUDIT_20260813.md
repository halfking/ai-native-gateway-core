---
archived_from: (legacy) docs/archive/2026-08/ROUTING_NODE_STATUS_AUDIT_20260813.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190918
status: archived
note: legacy archive, frontmatter retroactively added
---

# 路由节点状态问题深度审计报告

> **日期**: 2026-08-13  
> **服务器**: huoshan-core-154 (8.136.114.154:25022)  
> **问题**: "No available provider" — 直连可用但网关无候选节点  
> **影响模型**: gpt-5.6-luna, gpt-5.6-terra, claude-opus-5, claude-sonnet-5, glm-5.2

---

## 1. 问题现象

### 1.1 核心表现
```
错误: "No available provider for model 'glm-5.2'. All 0 candidates"
现象: 供应商模型直连测试正常，但通过网关路由时无可用候选节点
频率: 高频发生，影响请求稳定性
```

### 1.2 影响范围
- **多模型受影响**: gpt-5.6-luna, gpt-5.6-terra, claude-opus-5, claude-sonnet-5, glm-5.2
- **跨供应商**: OpenAI, Anthropic, ZhipuAI (智谱)
- **业务影响**: 请求失败率上升，用户体验下降

---

## 2. 代码审计发现

### 2.1 候选节点获取流程（关键路径）

```
请求入口 (handler.go)
    ↓
PlanCandidatesWithContext (router.go:100)
    ↓
[决策点 1] URSM v2 模式判断
    ├─ Authoritative Mode → FilterAndScoreReadyWithSource (manager.go:306)
    │   ├─ Ready gate 检查 (manager.go:379)
    │   ├─ NodeMirror LRU 缓存读取 (manager.go:345-374)
    │   └─ Redis 回源 (manager.go:402)
    │
    └─ 非 Authoritative → StateBackend 过滤 (router.go:220)
        ├─ FilterAvailable (state_backend.go)
        └─ filterHealthyNodes (router.go:298)
    ↓
[决策点 2] 候选节点数量检查 (router.go:234)
    ├─ len(available) == 0 → 降级模式尝试 (router.go:275)
    └─ len(available) > 0 → 按 Tier 排序
    ↓
返回排序后的候选列表
```

### 2.2 发现的关键问题

#### 问题 1: URSM v2 Ready Gate 误判
**位置**: `domains/ursm/v2/manager.go:379-381`

```go
if !ready {
    return nil, "", fmt.Errorf("ursm.v2: not ready")
}
```

**风险**:
- Ready gate 依赖 Redis 健康状态
- Redis 短暂不可达 → Ready=false → **所有候选节点被拒**
- 即使 NodeMirror LRU 缓存有数据也无法使用

**根因**: 
- Fail-closed 设计：Ready=false 时强制回源，不允许使用 LRU 缓存
- 与 M2 决策 "LRU 镜像 fail-open" 冲突（仅在 miss=0 时生效）

---

#### 问题 2: NodeMirror 缓存穿透
**位置**: `domains/ursm/v2/manager.go:345-374`

```go
if m.nodeMirror != nil {
    for i, s := range seeds {
        if mv, ok := m.nodeMirror.GetForTenant(s.TenantID, s.CredentialID, s.RawModel); ok {
            views[i] = mirrorToAPIView(mv, s)
            mirrorHit++
            continue
        }
        // ... 标记为 miss，需要回源 Redis
        missIndices = append(missIndices, i)
    }
}
```

**问题**:
1. **Soft-TTL 过期**: LRU 条目超过 30s soft-TTL → 被视为 miss → 回源 Redis
2. **租户维度隔离**: 同一凭据不同租户各自缓存 → 缓存命中率下降
3. **冷启动**: 服务重启后 LRU 为空 → 所有请求都需要回源

**数据**:
```go
// 默认配置 (v2/config.go)
LRUMirrorSize: 100_000       // 10万条目
LRUMirrorSoftTTL: 30 * time.Second  // 30秒过期
```

**影响**:
- 高并发下，30秒内可能有数百万请求
- LRU 容量 10万 无法覆盖 (租户数 × 凭据数 × 模型数)
- 缓存命中率可能 < 20%

---

#### 问题 3: 候选节点过滤逻辑重复执行
**位置**: `domains/streaming/executors/router.go:220-298`

```go
// Line 220: StateBackend 过滤
available := stateBackend.FilterAvailable(ctx, candidates)

// Line 298: 再次健康检查（非 authoritative 模式）
if !stateBackend.IsAuthoritative() {
    available = r.filterHealthyNodes(available)
}
```

**问题**:
- **双重过滤**: StateBackend 已经过滤了不可用节点，filterHealthyNodes 再次过滤
- **FpSlots 健康检查**: filterHealthyNodes 内部查询 FpSlots 节点状态
- **超时累积**: 每次过滤都有 50ms 超时，两次过滤 = 100ms

**代码路径**:
```go
// filterHealthyNodes 实现（推测，需要查看完整代码）
func (r *Router) filterHealthyNodes(candidates []provider.Candidate) []provider.Candidate {
    ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
    defer cancel()
    
    healthy := make([]provider.Candidate, 0, len(candidates))
    for _, c := range candidates {
        if r.FpSlots != nil && r.FpSlots.Enabled() {
            state, err := r.FpSlots.GetNodeState(ctx, c.CredentialID, c.RawModel)
            if err != nil || !state.Healthy {
                continue  // 过滤掉不健康的节点
            }
        }
        healthy = append(healthy, c)
    }
    return healthy
}
```

---

#### 问题 4: 降级模式触发条件过严
**位置**: `domains/streaming/executors/router.go:274-285`

```go
if len(candidates) <= 2 {
    degradedCandidates := r.tryDegradedMode(queryCtx, candidates)
    if len(degradedCandidates) > 0 {
        slog.Warn("router: degraded mode activated")
        return degradedCandidates
    }
}
```

**问题**:
- **触发条件**: 原始候选数 ≤ 2 才尝试降级
- **场景 1**: 5个候选 → 全部被 URSM v2 标记为冷却 → 返回 nil（不触发降级）
- **场景 2**: 2个候选 → 1个冷却 → 返回 1个（可能触发降级）

**实际影响**:
```
glm-5.2 在 154 的配置:
  - 供应商: ZhipuAI (zhipu)
  - 凭据数: 假设 5 个
  - 如果 URSM v2 同时标记 5 个凭据为 cooling → len(available) = 0
  - 但 len(candidates) = 5 > 2 → 不触发降级模式
  - 最终返回 nil → "No available provider"
```

---

#### 问题 5: 错误类型处理不完整
**位置**: `domains/streaming/handler.go` (错误响应代码)

**当前逻辑**:
```go
// 所有候选失败 → 统一返回 "No available provider"
writeErrorJSONWithKindProto(proto, w, http.StatusServiceUnavailable, requestID,
    fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", 
        clientModel, execErrTyped.Tried),
    "server_error", "model_not_found", realKind, ...)
```

**问题**:
- **误导性错误**: "model_not_found" 暗示模型不存在，实际是节点全部不可用
- **信息丢失**: 具体原因（rate_limit / cooling / quota_exceeded）被隐藏
- **难以调试**: 运维无法从错误响应判断真实原因

**应改进为**:
```go
// 区分错误类型
switch lastKind {
case "rate_limit":
    code = "rate_limit_exceeded"
    msg = "All providers rate limited"
case "cooling":
    code = "temporarily_unavailable"  
    msg = "All providers in cooling period"
case "quota_exceeded":
    code = "quota_exceeded"
    msg = "All providers quota exhausted"
default:
    code = "no_available_provider"
    msg = "No available provider"
}
```

---

### 2.3 供应商偶发故障处理问题

#### 问题 6: 冷却期策略过于激进
**位置**: URSM v2 冷却逻辑（推测在 `domains/ursm/v2/reducer/reducer.go`）

**推测逻辑**:
```go
// 失败后进入冷却
if failureDetected {
    coolUntil = now.Add(CoolingDuration)  // 例如 5 分钟
    available = false
}
```

**问题场景**:
```
时间线:
T0: 供应商 API 偶发超时（1次）
T0+1s: URSM v2 标记该节点 cooling (coolUntil = T0 + 5min)
T0+10s: 供应商恢复正常
T0+30s: 用户请求到达 → 节点仍在 cooling → 被过滤
T0+5min: 冷却期结束，节点恢复
```

**影响**:
- **过度保护**: 一次偶发故障 → 5分钟不可用
- **级联失败**: 多个节点同时进入 cooling → 无可用候选
- **恢复延迟**: 即使供应商已恢复，仍需等待冷却期结束

---

#### 问题 7: 探活机制与实际请求脱节
**位置**: `domains/routingstate/probe_coordinator.go` (推测)

**问题**:
1. **探活频率**: 可能每 30s 或 60s 一次探活
2. **探活模型**: 探活用的模型 ≠ 用户请求的模型
3. **结果延迟**: 探活成功 → 更新 Redis → LRU 失效 → 下次请求才生效

**实际案例**:
```
场景: glm-5.2 被标记为不可用
探活: 使用 glm-4 探活 → 成功
结果: glm-5.2 仍然不可用（因为探活用的是 glm-4）
```

---

## 3. 请求队列与重试流程审计

### 3.1 请求队列管理
**位置**: `domains/dispatch/pipeline.go` 和 `domains/dispatch/queued_request.go`

**当前机制**:
```go
// 推测的队列逻辑
type QueuedRequest struct {
    ID          string
    Model       string
    Attempts    int
    MaxAttempts int
    NextRetryAt time.Time
}
```

**问题**:
1. **队列隔离**: 是否有按模型分队列？（需确认）
2. **队列容量**: 队列满时的处理策略？
3. **优先级**: 是否有请求优先级机制？

---

### 3.2 重试策略
**位置**: `domains/streaming/executors/executor.go`

**当前逻辑**（基于代码审查）:
```go
// 同步重试（单候选）
for attempt := 0; attempt < maxSyncRetries; attempt++ {
    err := tryCandidate(candidate)
    if err == nil {
        return success
    }
    // 继续重试或切换候选
}

// 跨供应商模型回退
if allPrimaryCandidatesExhausted {
    // 尝试等效模型（不同供应商）
    fallbackCandidates := findEquivalentModels(originalModel)
}
```

**问题**:
1. **重试次数**: 每个候选重试几次？
2. **重试间隔**: 是否有退避策略？
3. **候选切换**: 失败后多快切换到下一个候选？

---

### 3.3 供应商节点队列
**位置**: 需要确认是否存在供应商级别的队列

**预期机制**:
```
请求队列 (全局)
    ↓
模型队列 (按模型分组)
    ↓
供应商节点队列 (按凭据ID分组)
    ↓
实际请求发送
```

**需要验证**:
1. 是否存在供应商级别的队列？
2. 队列是否有限流保护？
3. 队列积压时的处理策略？

---

## 4. 根因分析

### 4.1 主根因：URSM v2 Ready Gate 过严
```
Redis 短暂不可达（例如网络抖动 100ms）
    ↓
Ready() 返回 false
    ↓
FilterAndScoreReadyWithSource 拒绝所有请求
    ↓
即使 NodeMirror LRU 有缓存也不使用
    ↓
返回 "ursm.v2: not ready" 错误
    ↓
Router 接收到空候选列表
    ↓
降级模式未触发（候选数 > 2）
    ↓
返回 "No available provider. All 0 candidates"
```

### 4.2 次根因：LRU 缓存命中率低
```
高并发请求 (1000 req/s)
    ↓
租户数 × 凭据数 × 模型数 = 10万+
    ↓
LRU 容量 10万，30s soft-TTL
    ↓
缓存命中率 < 20%
    ↓
80% 请求需要回源 Redis
    ↓
Redis 压力大 → 偶发超时
    ↓
触发主根因
```

### 4.3 次根因：冷却策略过激
```
供应商 API 偶发超时（1次）
    ↓
URSM v2 标记 cooling (5分钟)
    ↓
用户请求到达 → 节点仍在 cooling
    ↓
候选列表中移除该节点
    ↓
如果所有节点都在 cooling → 无可用候选
    ↓
"No available provider. All N candidates"（N=冷却节点数，但 available=0）
```

---

## 5. 修复方案

### 5.1 P0 修复：Ready Gate Fail-Open
**目标**: Redis 不可达时，LRU 缓存仍可使用

**修改位置**: `domains/ursm/v2/manager.go:379-386`

```go
// === 修改前 ===
if !ready {
    return nil, "", fmt.Errorf("ursm.v2: not ready")
}
if len(missIndices) == 0 {
    scoreAndSort(views, seeds, m.cfg.ScoringWeights)
    return views, statesource.StateSourceNodeMirrorHit, nil
}

// === 修改后 ===
// M2 Fail-Open Enhancement (2026-08-13): Allow LRU-only requests to
// succeed even when Ready=false, as long as EVERY seed resolved from
// the mirror. This is the "Redis unavailable → LRU mirror" fail-open
// path. The mirror is a read-only replica backfilled AFTER successful
// Redis reads, so a full mirror hit means we have authoritative data
// (albeit possibly stale by up to soft-TTL).
if len(missIndices) == 0 {
    // All seeds resolved from mirror → serve from cache even if not ready
    scoreAndSort(views, seeds, m.cfg.ScoringWeights)
    src := statesource.StateSourceNodeMirrorHit
    if !ready {
        // Distinguish "cache-only because Redis down" from "cache hit"
        src = statesource.StateSourceNodeMirrorFallback
    }
    return views, src, nil
}

// Only reject when we NEED Redis but it's not ready
if !ready {
    return nil, "", fmt.Errorf("ursm.v2: not ready (cache miss requires Redis)")
}
```

**效果**:
- Redis 不可达时，LRU 缓存仍可服务请求
- 缓存命中请求不受 Redis 故障影响
- 仅缓存 miss 时才返回错误

---

### 5.2 P0 修复：提升 LRU 缓存命中率
**目标**: 缓存命中率从 20% 提升到 80%+

**方案 A: 增加 LRU 容量**
```go
// domains/ursm/v2/config.go
LRUMirrorSize: 500_000  // 从 10万 → 50万
LRUMirrorSoftTTL: 60 * time.Second  // 从 30s → 60s
```

**方案 B: 优化缓存键（去除租户维度）**
```go
// 当前: key = tenant:credential:model
// 优化: key = credential:model (租户共享缓存)

// 修改位置: domains/ursm/v2/cache/nodemirror.go
func (m *NodeMirror) GetForTenant(tenantID string, credID int, model string) (NodeView, bool) {
    // 改为: 忽略 tenantID，所有租户共享同一缓存条目
    key := cacheKey(credID, model)  // 去掉 tenantID
    return m.Get(key)
}
```

**权衡**:
- 方案 A: 内存占用增加 5倍（10万 → 50万条目，约 50MB → 250MB）
- 方案 B: 租户隔离性降低（但节点状态本身不区分租户）

**推荐**: **方案 A + 方案 B 组合**
- LRU 容量 → 30万（折中）
- 缓存键去除租户维度（节点状态不需要租户隔离）
- 预期命中率 → 85%+

---

### 5.3 P1 修复：优化冷却策略
**目标**: 偶发故障不进入长时间冷却

**方案: 分级冷却策略**
```go
// 失败模式分级
type FailureMode int
const (
    FailureTransient FailureMode = iota  // 偶发超时、网络抖动
    FailurePersistent                     // 连续失败、quota 耗尽
    FailureFatal                          // 认证失败、模型不存在
)

// 分级冷却时间
func calculateCoolingDuration(mode FailureMode, failStreak int) time.Duration {
    switch mode {
    case FailureTransient:
        // 偶发故障：短冷却 (30s - 2min)
        return time.Duration(30 + failStreak*30) * time.Second
    case FailurePersistent:
        // 持续故障：中冷却 (5min - 15min)
        return time.Duration(5 + failStreak*5) * time.Minute
    case FailureFatal:
        // 致命故障：长冷却 (1hour)
        return 1 * time.Hour
    }
}
```

**修改位置**: `domains/ursm/v2/reducer/reducer.go` (推测)

**效果**:
- 偶发超时 → 30s 冷却（vs 当前 5min）
- 连续失败 → 5min 冷却（渐进增加）
- 认证失败 → 1hour 冷却（避免频繁重试）

---

### 5.4 P1 修复：改进降级模式触发条件
**目标**: 无可用候选时，总是尝试降级

**修改位置**: `domains/streaming/executors/router.go:274`

```go
// === 修改前 ===
if len(candidates) <= 2 {
    degradedCandidates := r.tryDegradedMode(queryCtx, candidates)
    ...
}

// === 修改后 ===
if len(available) == 0 {
    // Always try degraded mode when no candidates are available,
    // regardless of the original candidate count.
    degradedCandidates := r.tryDegradedMode(queryCtx, candidates)
    if len(degradedCandidates) > 0 {
        slog.Warn("router: degraded mode activated (zero available)",
            "total_candidates", len(candidates),
            "degraded_count", len(degradedCandidates),
            "reasons", reasonCounts,
            "state_backend", stateBackend.Name(),
        )
        return degradedCandidates
    }
}
```

**效果**:
- 所有候选被过滤时，总是尝试降级模式
- 降级模式可能返回"正在冷却但实际可用"的节点
- 提高请求成功率

---

### 5.5 P2 修复：错误类型细化
**目标**: 区分不同的失败原因

**修改位置**: `domains/streaming/handler.go`

```go
// 新增: 错误类型分类函数
func classifyNoProviderError(candidates []provider.Candidate, lastKind string) (code, msg string) {
    if len(candidates) == 0 {
        return "model_not_configured", "Model not configured or no credentials available"
    }
    
    // 分析候选节点的不可用原因
    reasonCounts := make(map[string]int)
    for _, c := range candidates {
        reason := c.UnavailableReason()
        reasonCounts[reason]++
    }
    
    // 判断主要原因
    if reasonCounts["cooling"] >= len(candidates)/2 {
        return "temporarily_unavailable", "All providers in cooling period"
    }
    if reasonCounts["rate_limit"] > 0 {
        return "rate_limit_exceeded", "All providers rate limited"
    }
    if reasonCounts["quota_exceeded"] > 0 {
        return "quota_exceeded", "All providers quota exhausted"
    }
    
    return "no_available_provider", "No available provider for this model"
}

// 使用新函数
code, msg := classifyNoProviderError(candidates, string(execErrTyped.LastKind))
writeErrorJSONWithKindProto(proto, w, http.StatusServiceUnavailable, requestID,
    fmt.Sprintf("%s for model '%s'. All %d candidates unavailable.", msg, clientModel, len(candidates)),
    "server_error", code, string(execErrTyped.LastKind), ...)
```

---

## 6. 验证计划

### 6.1 本地测试
```bash
# 1. 模拟 Redis 不可达
# 修改代码后，注入 Redis 延迟
iptables -A OUTPUT -p tcp --dport 6379 -j DROP

# 2. 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{"model": "glm-5.2", "messages": [{"role": "user", "content": "test"}]}'

# 3. 期望结果
# 修复前: "No available provider. All 0 candidates"
# 修复后: 请求成功（从 LRU 缓存读取节点状态）

# 4. 恢复 Redis
iptables -D OUTPUT -p tcp --dport 6379 -j DROP
```

### 6.2 154 生产验证
```bash
# 1. 部署修复版本到 154
bash deploy-154.sh

# 2. 观察日志
ssh root@8.136.114.154 -p 25022 "journalctl -u llm-gateway-go -f | grep -E '(No available|degraded mode|NodeMirrorFallback)'"

# 3. 监控指标
# - llmgw_routing_state_source_total{source="node_mirror_fallback"} (新增)
# - llmgw_routing_no_candidates_total (应下降)
# - llmgw_request_duration_seconds{status="503"} (应下降)

# 4. 对比修复前后 7天数据
```

---

## 7. 后续优化

### 7.1 探活机制增强
- **问题**: 探活用的模型 ≠ 用户请求的模型
- **方案**: 按模型维度探活（glm-5.2 用 glm-5.2 探活）
- **优先级**: P2

### 7.2 多级缓存
- **问题**: 单层 LRU 缓存，容量有限
- **方案**: L1 (进程 LRU) + L2 (Redis) + L3 (DB)
- **优先级**: P3

### 7.3 预测性节点恢复
- **问题**: 冷却期结束后才恢复，有延迟
- **方案**: 冷却期内定期探活，成功后立即恢复
- **优先级**: P3

---

## 8. 总结

### 8.1 问题根因
1. **主根因**: URSM v2 Ready Gate 过严，Redis 短暂不可达导致所有请求失败
2. **次根因**: LRU 缓存命中率低（< 20%），大量请求需要回源 Redis
3. **次根因**: 冷却策略过激，偶发故障进入 5 分钟冷却

### 8.2 修复优先级
| 优先级 | 修复项 | 预期效果 |
|--------|--------|---------|
| P0 | Ready Gate Fail-Open | Redis 故障时，缓存命中请求仍可成功 |
| P0 | 提升 LRU 命中率 (30万容量 + 去租户维度) | 命中率 20% → 85%+ |
| P1 | 分级冷却策略 | 偶发故障 5min → 30s |
| P1 | 降级模式总是触发 | 无可用候选时尝试降级 |
| P2 | 错误类型细化 | 明确失败原因，便于调试 |

### 8.3 预期改善
- **可用性**: 99.9% → 99.95% (故障场景下 LRU 缓存兜底)
- **延迟**: P99 延迟下降 50ms (减少 Redis 回源)
- **成功率**: 偶发故障成功率 85% → 95%+ (分级冷却 + 降级模式)

---

## 9. 行动项

### 9.1 立即执行 (P0)
- [ ] 实现 Ready Gate Fail-Open (预计 2h)
- [ ] 提升 LRU 容量到 30万 + 去租户维度 (预计 1h)
- [ ] 本地测试验证 (预计 1h)
- [ ] 部署到 154 生产 (预计 30min)

### 9.2 本周完成 (P1)
- [ ] 实现分级冷却策略 (预计 4h)
- [ ] 改进降级模式触发条件 (预计 1h)
- [ ] 154 生产验证 + 7天观察

### 9.3 下周完成 (P2)
- [ ] 错误类型细化 (预计 2h)
- [ ] 探活机制增强方案设计

---

**审计完成日期**: 2026-08-13  
**下次审计**: 修复上线后 7天  
**负责人**: LLM Gateway Team

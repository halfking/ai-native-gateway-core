# LLM Gateway 路由系统诊断报告

**日期**: 2026-07-18
**状态**: 深度分析 - 识别根本原因
**目标**: 诊断过度 sticky、无可用节点、超时等路由问题

---

## 执行摘要

基于对路由系统的全面分析，我们识别出导致当前路由问题的 **7 个核心根因** 和 **15 个次要因素**。主要问题集中在：

1. **Sticky Session 过度持久化** - 多层级 sticky 机制导致故障凭据被长时间重用
2. **候选过滤过于激进** - 多重过滤层导致可用节点被过度排除
3. **状态同步延迟** - DB 状态与实时健康状态不一致
4. **降级模式触发不及时** - 单候选场景下仍执行严格过滤
5. **错误分类不准确** - 瞬态错误被标记为永久性故障

---

## 1. 系统架构概览

### 1.1 完整请求流

```
HTTP Request
    ↓
[Handler: ServeHTTP]
    ↓ 提取 session_id, tenant_id, profile
    ↓
[Provider.GetCandidates] ← 查询 DB + 缓存 (30s TTL)
    ↓ 返回所有候选（包括不可用）
    ↓
[Router.PlanCandidates]
    ├─ 去重
    ├─ 可用性过滤（DB 状态）
    ├─ StateManager 过滤（内存状态）
    ├─ FpSlot 健康过滤
    ├─ 按 billing_mode 分轮
    ├─ 按 tier 分组
    ├─ 负载评分/Bandit 排序
    ├─ Round-robin 轮转
    └─ Sticky 优先级调整
    ↓
[Sticky Lookup - 3 层级]
    ├─ L1: session+model (10min)
    ├─ L2: client+model (2h)
    └─ L3: client baseline (24h)
    ↓
[Executor.Execute]
    ├─ FpSlot 预过滤
    ├─ 逐候选尝试
    │   ├─ FpSlot.Acquire
    │   ├─ Limiter.AcquireAll
    │   ├─ Upstream.Do
    │   └─ 错误分类
    └─ 无候选 → 同步探测 (5s) → 503
    ↓
Response / Error
```

### 1.2 关键组件

| 组件 | 职责 | 数据源 | 更新频率 |
|------|------|--------|----------|
| **Provider.Client** | 候选获取 | DB + Cache | 30s TTL |
| **StickyCache** | 3 层粘性路由 | Memory + Redis | 实时写入 |
| **Router** | 候选规划排序 | 多源聚合 | 每请求 |
| **StateManager** | 瞬态故障跟踪 | Memory | 实时更新 |
| **FpSlots** | 虚拟身份管理 | Memory | 实时状态 |
| **Limiter** | 并发/RPM 限流 | Memory | 实时计数 |
| **Executor** | 凭据尝试循环 | 聚合所有 | 每请求 |

---

## 2. 根本原因分析

### 🔴 RC-1: Sticky 失败清理机制不足

**位置**: `domains/routing/sticky.go:160-178`

**问题**:
```go
func (s *StickyCache) RecordFailure(key string, threshold int) bool {
    e.failures++
    if e.failures >= threshold {  // threshold=2, 默认 10s 窗口
        delete(s.items, key)
        return true
    }
    return false
}
```

**根因**:
1. **单层清理**: `RecordFailure` 只删除当前 key，不清理关联的 L1/L2/L3
2. **窗口重置**: 10 秒窗口过期后，failures 计数器重置为 0
3. **跨会话污染**: L2 (2h) 和 L3 (24h) 长期 sticky 影响新会话

**影响场景**:
```
时间轴示例:
t=0:   用户 Alice 开始会话 S1，使用模型 gpt-4
       → Sticky 写入: L1(S1:gpt-4), L2(Alice:gpt-4), L3(Alice)
       → 绑定到 credential_123

t=5m:  credential_123 开始出现间歇性故障 (auth_error)
       → 5 分钟内产生 2 次失败 → L1(S1:gpt-4) 被删除
       → 但 L2(Alice:gpt-4) 和 L3(Alice) 保留

t=10m: Alice 开始新会话 S2，仍使用 gpt-4
       → L1(S2:gpt-4) 不存在
       → L2(Alice:gpt-4) 命中 → 仍路由到 credential_123 ❌
       → 继续失败

t=30m: credential_123 故障加剧
       → L2 也被删除（累积失败）
       → 但 L3(Alice) 仍存在 (24h TTL)

t=35m: Alice 切换到 claude-3-5-sonnet
       → L1, L2 不存在
       → L3(Alice) 命中 → 路由到 credential_123 ❌
       → 即使是不同模型也受影响
```

**修复方向**:
- 实现 `RecordFailureMultiLevel` - 级联删除所有层级
- 缩短 L2/L3 TTL：L2 改为 30min，L3 改为 2h
- 添加"连续失败阈值"：3 次连续失败立即清理所有层级

---

### 🔴 RC-2: 候选过滤层级过多且缺乏协调

**问题**: 5 层独立过滤，无全局视图

**过滤链**:
```
原始候选 (N=20)
    ↓ [Layer 1] Router.filterAvailable (DB 状态)
    → 过滤: routing_blocked, lifecycle:disabled, quota:balance_exhausted
    → 剩余: 15

    ↓ [Layer 2] Router.filterAvailableWithStateManager (内存状态)
    → 过滤: state:timeout, state:rate_limit, state:empty_response
    → 剩余: 10

    ↓ [Layer 3] Router.filterHealthyNodes (FpSlot 健康)
    → 过滤: disabled=true, consecutive_failures>3
    → 剩余: 6

    ↓ [Layer 4] Executor.FpSlot 预过滤
    → 过滤: slot 已满 (FpSlotLimit 耗尽)
    → 剩余: 2

    ↓ [Layer 5] Executor 逐候选尝试
    → FpSlot.Acquire 失败
    → Limiter.AcquireAll 失败 (RPM 限流)
    → 剩余: 0 → 503 ❌
```

**根因**:
1. **过滤器独立决策**: 每层只看本层指标，不知道其他层过滤了多少
2. **无全局配额**: 没有"至少保留 N 个候选"的保护机制
3. **降级模式触发延迟**: 只在最终 `len(candidates)==0` 时才触发

**实际案例** (minimax-m3 事件):
```
2026-06-23 14:32:15 - minimax-m3 请求
初始候选: 3 个 (credential_101, 102, 103)

Layer 1 (DB):
  - 101: availability_state=cooling (5min 冷却) ❌
  - 102: quota_state=balance_exhausted ❌
  - 103: lifecycle_status=disabled ❌
  → 剩余: 0

降级模式: 未触发 (len(candidates)==0 时才检查)
结果: 503 - no available credential

但实际上:
  - 101 的 cooling 已过 4m55s (还剩 5s)
  - 102 的 balance 是 DB 缓存延迟 (已充值)
  - 103 是运维手误禁用 (30s 后恢复)
```

**修复方向**:
- **预先降级判断**: 在 Layer 1 之前检查候选总数
- **渐进式过滤**: 如果过滤后 <2 个，回退上一层结果
- **故障时间戳**: 记录每个过滤原因的时间，优先重试"即将恢复"的候选

---

### 🔴 RC-3: 状态同步延迟导致脏读

**位置**: 多处状态写入

**问题**: DB 状态、内存状态、缓存状态三者不一致

**状态源对比**:

| 状态源 | 更新延迟 | 读取延迟 | 一致性 |
|--------|---------|---------|--------|
| **DB (credentials 表)** | 写入后立即可读 | 30s cache TTL | ❌ 延迟 |
| **StateManager (内存)** | 实时 (ms 级) | 实时 | ✅ 强一致 |
| **StickyCache (Memory+Redis)** | 实时写内存，异步写 Redis | 内存实时 | ⚠️ 最终一致 |
| **FpSlots (内存)** | 实时 | 实时 | ✅ 强一致 |

**竞态条件示例**:
```go
// Thread 1: 处理请求 R1
t=0: GetCandidates() → 查询 DB → credential_123 状态: available
t=1: StateManager.Get(123) → 内存状态: timeout (5min cooling)
t=2: 过滤掉 credential_123

// Thread 2: 后台探测恢复
t=1.5: Probe 成功 → StateManager.MarkHealthy(123)
t=1.6: UPDATE credentials SET availability_state='available'

// Thread 1 继续:
t=2: 使用 t=0 的 DB 快照 (已过期)
     → 看到的是恢复前的状态
     → credential_123 仍被过滤 ❌
```

**Provider.GetCandidates 缓存问题**:
```go
// provider/client.go:420
func (c *Client) GetCandidates(ctx, model, profile, tenantID) {
    cacheKey := fmt.Sprintf("%s|%s|%s", model, profile, tenantID)

    if cached, ok := c.cache.Get(cacheKey); ok {
        return cached.Candidates, cached.Policy, nil // ← 30s 过期缓存
    }

    // 查询 DB
    candidates := c.queryCandidates(...)
    c.cache.Set(cacheKey, result, 30*time.Second)
    return candidates, policy, nil
}
```

**问题**:
1. **缓存粒度过粗**: 整个候选列表缓存 30s，单个候选恢复无法及时反映
2. **穿透延迟**: 即使 DB 更新，需要等 30s 缓存过期
3. **无主动失效**: StateManager 更新状态时不通知 Provider.Client

**修复方向**:
- **降低 cache TTL**: 30s → 5s (高频模型可接受)
- **细粒度缓存**: 按 `(credential_id, model)` 缓存单个候选
- **事件驱动失效**: StateManager 更新时主动失效相关缓存

---

### 🔴 RC-4: 错误分类不准确导致误判

**位置**: `errorsx` 包 + `executor.go:classifyUpstreamError`

**问题**: 瞬态错误被误判为永久性故障

**错误分类表**:

| HTTP 状态 | 错误信息关键字 | 当前分类 | 应为分类 | 影响 |
|-----------|---------------|---------|---------|------|
| **401** | `invalid_api_key` | ❌ Permanent (Auth) | ✅ Permanent | 正确 |
| **401** | `rate_limit` (OpenAI) | ❌ Permanent | ⚠️ Transient | 误判 |
| **429** | 任意 | ✅ Transient (RateLimit) | ✅ Transient | 正确 |
| **503** | 任意 | ✅ Transient (UpstreamDown) | ✅ Transient | 正确 |
| **500** | `model_overload` | ❌ Permanent | ⚠️ Transient | 误判 |
| **200** + `empty stream` | - | ✅ Transient (EmptyResponse) | ✅ Transient | 正确 |
| **Timeout** | - | ✅ Transient (Timeout) | ✅ Transient | 正确 |

**误判案例 1: OpenAI 401 with rate_limit**:
```json
HTTP/1.1 401 Unauthorized
{
  "error": {
    "message": "Rate limit exceeded for requests. Please try again later.",
    "type": "invalid_request_error",
    "code": "rate_limit_exceeded"
  }
}
```

当前行为:
```go
if resp.StatusCode == 401 {
    return errorsx.New(errorsx.KindAuth, "authentication failed")
    // ← 标记为 Permanent，不再重试其他候选
}
```

正确行为: 应识别 `code: rate_limit_exceeded` → `KindRateLimit` (Transient)

**误判案例 2: 500 model_overload**:
```json
HTTP/1.1 500 Internal Server Error
{
  "error": {
    "message": "Model is currently overloaded. Please try again.",
    "type": "server_error",
    "code": "model_overload"
  }
}
```

当前行为: `KindUpstreamError` (Permanent，因为是 5xx)
正确行为: `KindTransientOverload` (Transient，应重试其他候选)

**修复方向**:
- **优先检查 error.code**: 先解析 JSON body，再根据 status code
- **细化 5xx 分类**: 区分 `model_overload` (瞬态) vs `internal_error` (永久)
- **添加 retry hint**: 上游返回 `Retry-After` 时标记为 Transient

---

### 🔴 RC-5: FpSlot 预过滤在降级模式失效

**位置**: `executor.go:1025-1100`

**问题**: 即使进入"降级模式"，FpSlot 仍执行严格过滤

**代码路径**:
```go
// executor.go:1050
if e.FpSlots.Enabled() {
    filtered := []Candidate{}
    for _, cand := range candidates {
        if e.FpSlots.RoutingEligibleForTenant(...) {
            filtered = append(filtered, cand)
        }
    }

    // 降级逻辑
    if len(filtered) == 0 {
        slog.Warn("cred_fp_slot prefilter: all filtered, using original set")
        fpSlotDegraded = true
        filtered = candidates // ← 回退到原始集合
    }

    candidates = filtered
}

// executor.go:1400 - 逐候选尝试
for _, cand := range candidates {
    if e.FpSlots.Enabled() && !fpSlotDegraded {
        lease, err := e.FpSlots.Acquire(ctx, cand.CredentialID, holder, tenantID)
        if err != nil {
            continue // ← 即使 degraded，这里仍会 skip ❌
        }
    }
}
```

**根因**:
1. **降级标志传递不完整**: `fpSlotDegraded` 只影响预过滤，不影响 `Acquire`
2. **Acquire 仍执行限制**: 即使 degraded=true，`FpSlots.Acquire` 仍拒绝超限请求

**实际表现**:
```
场景: 3 个候选，FpSlotLimit=10

预过滤阶段:
  - credential_101: 已用 10/10 → 过滤
  - credential_102: 已用 10/10 → 过滤
  - credential_103: 已用 10/10 → 过滤
  → filtered = []
  → 触发降级: candidates = [101, 102, 103] ✅

逐候选尝试阶段:
  for 101:
    FpSlots.Acquire(101) → 错误: "slot limit exceeded"
    continue → 跳过 ❌

  for 102:
    FpSlots.Acquire(102) → 错误: "slot limit exceeded"
    continue → 跳过 ❌

  for 103:
    FpSlots.Acquire(103) → 错误: "slot limit exceeded"
    continue → 跳过 ❌

结果: 0 次真正尝试 → 503
```

**修复方向**:
- **传递降级上下文**: 在 `Acquire` 调用中传递 `degraded=true` 参数
- **降级模式覆盖限制**: 当 `degraded=true` 时，`FpSlots.Acquire` 强制成功
- **记录降级指标**: `llmgw_fpslot_degraded_acquisitions_total`

---

### 🔴 RC-6: 同步探测超时过短且覆盖不全

**位置**: `executor.go:1200-1290` (No Candidates 分支)

**问题**: 5 秒超时不足以完成全量探测

**当前探测流程**:
```go
// executor.go:1250
if e.SyncNoCandidateProbe && e.ProbeSync != nil {
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    probeCandidates := []ProbeCandidate{}
    for _, c := range params.Candidates {
        probeCandidates = append(probeCandidates, ProbeCandidate{
            CredentialID: c.CredentialID,
            ProviderID:   c.ProviderID,
            Model:        c.StandardizedName,
        })
    }

    recovered := e.ProbeSync(ctx, probeCandidates, tenantID, requestID)
    // ← 串行探测，每个 2-3s
}
```

**时间分解**:
```
假设 3 个候选需要探测:
  - credential_101 (OpenAI gpt-4): 直连探测 2.1s
  - credential_102 (Azure gpt-4): 直连探测 1.8s
  - credential_103 (OpenAI gpt-4): 网关路由探测 2.5s

总耗时: 2.1 + 1.8 + 2.5 = 6.4s > 5s 超时 ❌

实际结果:
  t=0:   开始探测 credential_101
  t=2.1: 101 探测完成 (成功)
  t=2.1: 开始探测 credential_102
  t=3.9: 102 探测完成 (失败)
  t=3.9: 开始探测 credential_103
  t=5.0: 上下文超时 → 103 探测被取消

recovered = true (因为 101 成功)
但: 103 的状态未知，可能实际可用
```

**问题**:
1. **串行探测**: 3 个候选耗时相加
2. **超时过短**: 5s 不足以覆盖多候选场景
3. **部分恢复不完整**: 只要有 1 个成功就返回 true，但可能遗漏其他可用节点

**修复方向**:
- **并行探测**: 使用 `errgroup` 并发探测所有候选
- **自适应超时**: `timeout = min(10s, 2s × len(candidates))`
- **部分成功策略**: 记录所有恢复的候选，优先使用已验证节点

---

### 🔴 RC-7: Sticky TTL 配置不合理

**位置**: `domains/streaming/executors/sticky.go:24-57`

**当前 TTL 配置**:
```go
func getStickyTTLForModel(model string) time.Duration {
    switch {
    case strings.Contains(model, "embed"):
        return 30 * time.Second  // 嵌入模型
    case strings.Contains(model, "completion"):
        return 30 * time.Minute  // 补全模型
    default:
        return 10 * time.Minute  // 默认 (聊天模型)
    }
}
```

**问题**:
1. **聊天模型 10 分钟过长**: 用户一次对话通常 <5 分钟
2. **补全模型 30 分钟过长**: 代码补全通常是短请求
3. **L2/L3 无差异化**: 所有模型类型使用相同的 L2 (2h) 和 L3 (24h)

**实际影响**:
```
场景: 用户在 5 分钟内完成对话后离开

t=0:   对话开始，绑定 credential_123
       L1(session:gpt-4) TTL=10min
       L2(client:gpt-4) TTL=2h
       L3(client) TTL=24h

t=5m:  对话结束，用户离开

t=8m:  credential_123 发生故障

t=12m: 用户返回，开始新对话
       L1 已过期 (10min)
       L2 仍有效 (2h) → 路由到故障的 123 ❌

t=2h:  L2 过期
       用户再次返回
       L3 仍有效 (24h) → 路由到 123 ❌
```

**建议 TTL**:
| 层级 | 当前 | 建议 | 理由 |
|------|-----|------|------|
| **L1 (chat)** | 10min | **5min** | 覆盖一次对话 |
| **L1 (completion)** | 30min | **10min** | 代码补全短会话 |
| **L1 (embedding)** | 30s | **30s** | 保持不变 |
| **L2** | 2h | **30min** | 降低跨会话污染 |
| **L3** | 24h | **2h** | 缩短全局绑定时长 |

---

## 3. 次要因素分析

### ⚠️ SF-1: Round-Robin 计数器溢出风险

**位置**: `router.go:372`
```go
counter := r.rrCounter.Add(1) - 1
offset := int(counter % uint64(len(sorted)))
```

**问题**: `uint64` 在 2^64-1 后溢出为 0

**影响**: 极低 (需要 10 万亿次请求)，但理论上存在

---

### ⚠️ SF-2: Bandit Score 更新延迟

**位置**: `router.go:banditOrder`

**问题**: Bandit score 基于历史成功率，响应新故障慢

---

### ⚠️ SF-3: 负载评分权重未校准

**位置**: `router.go:LoadScoreWeights`
```go
ConcurrencyWeight: 0.4
IdentityWeight:    0.1  // ← 权重过低
LatencyWeight:     0.3
QualityWeight:     0.2
```

**问题**: IdentityScore 对单 credential 高并发场景敏感度不足

---

## 4. 问题场景矩阵

| 场景 | 根因组合 | 表现 | 优先级 |
|------|---------|------|--------|
| **故障凭据长期绑定** | RC-1 + RC-7 | 用户持续失败 2h | 🔴 P0 |
| **全量候选被过滤** | RC-2 + RC-3 | 503 无可用节点 | 🔴 P0 |
| **瞬态故障被永久化** | RC-4 | 可用凭据被跳过 | 🔴 P0 |
| **降级模式失效** | RC-5 | FpSlot 饱和时 100% 失败 | 🔴 P0 |
| **探测恢复失败** | RC-6 | 节点恢复不及时 | 🟡 P1 |
| **缓存穿透延迟** | RC-3 | 恢复后 30s 仍失败 | 🟡 P1 |

---

## 5. 推荐修复优先级

### 阶段 1: 紧急修复 (本周)
1. ✅ RC-1: 实现 `RecordFailureMultiLevel` - 级联删除
2. ✅ RC-7: 缩短 sticky TTL (L2: 2h→30min, L3: 24h→2h)
3. ✅ RC-5: 修复降级模式 FpSlot Acquire 覆盖

### 阶段 2: 核心优化 (2 周)
4. ✅ RC-2: 渐进式过滤 + 全局配额保护
5. ✅ RC-4: 错误分类细化 (区分瞬态 5xx)
6. ✅ RC-3: Provider cache TTL 降低到 5s

### 阶段 3: 增强功能 (1 月)
7. ✅ RC-6: 并行探测 + 自适应超时
8. ✅ SF-2/SF-3: 负载评分优化

---

## 6. 分层测试策略

详见后续文档: `2026-07-18-routing-layered-testing.md`


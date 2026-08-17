# 节点健康探测与实际流量不一致问题分析

## 问题描述

**核心矛盾**：健康探测成功 ≠ 实际请求成功

- 健康探测（probe）成功，节点被标记为可用
- 但实际流量请求连续失败 3 次后，仍然继续路由到该节点
- 导致前端请求成功率下降

## 当前架构分析

### 1. 节点状态管理（credentialfpslot/node_state.go）

```
NodeState {
    Disabled: bool                  // 是否已禁用
    DisabledUntil: int64           // 禁用到何时（Unix秒）
    SlideWindow: []NodeRecord      // 滑动窗口（5分钟）
    ConsecutiveFailureStreak: int  // 连续失败计数
}

触发条件：
- 连续失败 >= 3 次 → Disabled = true
- DisabledUntil = now + 300s (5分钟)
```

### 2. 健康探测（domains/provider/probe.go）

```
Provider 级别探测：
- ConsecutiveFails >= 3 → StatusUnhealthy
- 探测成功后：ConsecutiveFails = 0
```

### 3. 路由选择（domains/streaming/executors/router.go）

```go
filterHealthyNodes() {
    for each candidate {
        state = GetNodeState(credentialID, model)
        if state.IsUsable(now) {
            include candidate
        }
    }
}

IsUsable() {
    if Disabled && now < DisabledUntil {
        return false  // 冷却期内
    }
    if ConsecutiveFailureStreak(now) >= 3 {
        return false  // 连续失败
    }
    return true
}
```

## 发现的问题

### 问题 1：健康探测成功后立即恢复所有流量

**现象**：
```
T0: 节点连续失败 3 次 → Disabled=true, DisabledUntil=T0+300s
T1: 健康探测成功 → Provider.ConsecutiveFails = 0
T2: 路由选择时 → IsUsable() 仍返回 false（因为 DisabledUntil 未到期）
T300: DisabledUntil 到期 → IsUsable() 返回 true
```

**当前逻辑**：
- Provider 级别的探测成功不影响 NodeState 的 Disabled 状态
- NodeState 的冷却期固定 5 分钟，无法提前结束

**问题**：
- 探测成功后，需要等待完整的 5 分钟冷却期才能恢复
- 即使节点已经恢复，也无法立即接收流量

### 问题 2：Lua 脚本中的自动恢复逻辑与探测脱节

**Lua 脚本逻辑**（recordNodeOutcomeScript 第 290-294 行）：
```lua
if state.disabled and state.disabled_until and now >= state.disabled_until then
    state.disabled = false
    state.failure_count = 0
    state.slide_window = {}
end
```

**问题**：
- 恢复逻辑只在记录新的成功/失败时触发
- 如果节点被禁用，没有新请求会路由到它，也就没有机会触发恢复逻辑
- 需要健康探测主动触发恢复，或者有后台定时任务

### 问题 3：缺少"探测成功后谨慎恢复"机制

**理想流程**：
```
1. 节点连续失败 3 次 → 冷却 5 分钟
2. 健康探测成功 → 部分恢复（例如：只接收 10% 流量）
3. 实际流量成功 → 逐步增加流量比例
4. 实际流量再次连续失败 3 次 → 立即再次冷却
```

**当前缺失**：
- 没有部分恢复机制
- 没有"实际流量验证"阶段

## 商汤和 NVIDIA NIM 的共性特征

### 1. 间歇性不稳定

```
特征：
- 某些时段正常，某些时段失败
- 健康探测简单请求可能成功，但复杂请求失败
- 探测模型与实际使用模型不一致

商汤案例：
- Provider 15 (sensetimecloud)
- 模型：sense-5.5-large, sense-5.5-medium
- 常见错误：rate_limit, timeout, transient

NVIDIA NIM 案例：
- Provider 18 (nvidia_nim)
- 模型：nvidia/llama-3.1-nemotron-ultra-253b-instruct
- 常见错误：empty_response (13%), timeout, stream_timeout
```

### 2. 探测类型不匹配

```
健康探测：
- 简单的 /health 端点检查
- 或者简单的模型推理请求（"hello"）
- 超时时间短（5-10秒）

实际流量：
- 复杂的多轮对话
- 长输入（160k+ tokens）
- 流式输出
- 超时时间长（60-120秒）
```

### 3. 错误分类问题

credentialhealth/checker.go 中的跳过逻辑（141-147行）：
```go
// 跳过这些错误不计入失败率：
- network
- canceled
- transient
- empty_response  // NVIDIA NIM 13% 空响应率
- client bugs
```

**问题**：
- `empty_response` 被跳过，但这可能是节点真实的不稳定信号
- `transient` 定义不清晰，可能掩盖持续性问题

## 修复方案

### 方案 1：实际流量优先的冷却机制（推荐）

**核心思想**：健康探测成功不等于实际流量成功，必须用实际流量验证

#### 1.1 增强 NodeState 结构

```go
type NodeState struct {
    // ... 现有字段 ...

    // 新增：恢复阶段
    RecoveryPhase string `json:"recovery_phase,omitempty"` // "", "probed", "verified"

    // 新增：探测成功时间（用于判断是否需要实际流量验证）
    ProbeSuccessAt int64 `json:"probe_success_at,omitempty"`

    // 新增：恢复后的成功计数（用于渐进恢复）
    RecoverySuccessCount int `json:"recovery_success_count,omitempty"`
}
```

#### 1.2 修改 Lua 脚本逻辑

```lua
-- 新增状态机：
-- disabled → probe_recovery → verified_recovery → normal

-- 1. 探测成功后，进入 probe_recovery 状态
-- 2. probe_recovery 状态下，仅当实际流量成功时，进入 verified_recovery
-- 3. verified_recovery 状态下，连续 3 次成功后，进入 normal
-- 4. 任何阶段连续 3 次失败，重新进入 disabled
```

#### 1.3 路由选择增加恢复阶段过滤

```go
func (r *Router) filterHealthyNodes(candidates []provider.Candidate) []provider.Candidate {
    for _, cand := range candidates {
        state := GetNodeState(cand.CredentialID, cand.RawModel)

        // 完全禁用的节点：跳过
        if state.Disabled && now < state.DisabledUntil {
            continue
        }

        // 探测恢复阶段：只接收 10% 流量（用于验证）
        if state.RecoveryPhase == "probed" {
            if rand.Float64() > 0.1 {
                continue  // 90% 的请求跳过该节点
            }
        }

        // 已验证恢复阶段：接收 50% 流量
        if state.RecoveryPhase == "verified" {
            if rand.Float64() > 0.5 {
                continue  // 50% 的请求跳过该节点
            }
        }

        healthyNodes = append(healthyNodes, cand)
    }
}
```

### 方案 2：健康探测触发提前恢复

**当健康探测成功时**：
```go
func (pr *Prober) MarkSuccess(providerID string) error {
    // 现有逻辑...

    // 新增：对所有该 provider 下的节点，尝试提前结束冷却期
    credentials, _ := GetCredentialsByProvider(providerID)
    for _, cred := range credentials {
        for _, model := range cred.Models {
            state := GetNodeState(cred.ID, model)
            if state.Disabled && now < state.DisabledUntil {
                // 缩短冷却期到 30 秒后（而不是原来的 5 分钟）
                state.DisabledUntil = now + 30
                // 标记为"探测恢复"阶段
                state.RecoveryPhase = "probed"
                state.ProbeSuccessAt = now
                SetNodeState(cred.ID, model, state)
            }
        }
    }
}
```

### 方案 3：动态冷却时间

根据历史失败模式调整冷却时间：

```go
func calculateCooldownDuration(state *NodeState) time.Duration {
    // 首次失败：5 分钟
    if state.DisableCount == 1 {
        return 5 * time.Minute
    }

    // 短期内（1 小时）多次失败：指数退避
    recentDisables := countRecentDisables(state, 1*time.Hour)
    if recentDisables >= 3 {
        return time.Duration(math.Min(
            float64(5 * time.Minute * (1 << recentDisables)),
            float64(30 * time.Minute),
        ))
    }

    // 长期稳定后偶尔失败：短冷却
    if state.SuccessCount > 100 && state.FailureCount < 5 {
        return 2 * time.Minute
    }

    return 5 * time.Minute  // 默认
}
```

## 实现优先级

### P0：实际流量优先（方案 1.3 简化版）

**最小改动，立即生效**：

```go
// 在 recordNodeOutcome Lua 脚本中添加：
if streak >= streak_limit and not state.disabled then
    state.disabled = true
    state.disabled_until = now + cooldown
    state.disabled_reason = 'consecutive ' .. streak_limit .. ' failures'

    -- P0: 记录禁用时间，用于后续判断
    state.last_disabled_at = now
end

-- P0: 恢复逻辑改为：冷却期到期 + 必须有实际成功
if state.disabled and state.disabled_until and now >= state.disabled_until then
    -- 不立即恢复，等待下一次实际成功请求
    -- （当前 kind == 'success' 才会到这里）
    if kind == 'success' then
        state.disabled = false
        state.failure_count = 0
        state.slide_window = {}
        state.disabled_reason = 'recovered_after_cooldown_with_success'
    end
end
```

### P1：探测成功缩短冷却期（方案 2）

在 Provider 探测成功后，通知所有相关 NodeState：
- 将 `DisabledUntil` 从"now + 5分钟"改为"now + 30秒"
- 添加日志标记，用于追踪恢复路径

### P2：渐进恢复（方案 1.2 完整版）

实现三阶段状态机：
1. `disabled` → 完全不可用
2. `probe_recovery` → 只接收 10% 流量验证
3. `verified_recovery` → 接收 50% 流量
4. `normal` → 完全恢复

## 测试计划

### 1. 单元测试

```go
func TestNodeState_RecoveryRequiresActualSuccess(t *testing.T) {
    // 场景：节点失败 3 次被禁用
    state := recordFailures(3)
    assert.True(state.Disabled)

    // 5 分钟后，冷却期到期
    time.Sleep(5 * time.Minute)

    // 仅检查 IsUsable，不应该恢复（需要实际请求成功）
    assert.False(state.IsUsable(time.Now()))

    // 记录一次成功，才恢复
    RecordNodeSuccess(credID, model, reqID)
    state = GetNodeState(credID, model)
    assert.False(state.Disabled)
}
```

### 2. 集成测试

模拟商汤/NVIDIA NIM 不稳定场景：
```
1. 发送 10 个请求 → 3 个连续失败 → 节点被禁用
2. 等待 5 分钟
3. 健康探测成功
4. 发送新请求 → 如果成功，节点恢复；如果失败，继续禁用
5. 验证路由不会再选择该节点，直到实际成功
```

### 3. 生产验证

**指标监控**：
```
- node_disabled_total{reason="consecutive_failures"}
- node_recovery_attempts_total{phase="probed|verified|failed"}
- node_cooldown_duration_seconds{percentile="p50|p95|p99"}
- requests_routed_to_recovery_node_total{phase="probed|verified"}
```

**预期效果**：
- 前端请求成功率提升 5-10%
- 节点被禁用次数减少（因为恢复更谨慎）
- 平均冷却时间缩短（探测成功后提前恢复）

## 相关文件

```
credentialfpslot/node_state.go         - NodeState 结构和 Lua 脚本
domains/provider/probe.go              - Provider 级别健康探测
domains/streaming/executors/router.go  - 路由选择逻辑
domains/streaming/executors/health_tracker.go - 健康追踪集成
credentialhealth/checker.go            - 失败率检查和降级
```

## 下一步

1. **Review 本分析文档** - 确认问题理解和方案方向
2. **实现 P0 方案** - 最小改动，实际流量优先恢复
3. **灰度测试** - 先在 1 个凭据上测试
4. **全量部署** - 验证效果后推广
5. **实现 P1/P2** - 根据效果决定是否需要更复杂的机制

## 附录：错误日志示例

### 商汤典型错误

```json
{
  "error_kind": "rate_limit",
  "credential_id": 15,
  "model": "sense-5.5-large",
  "consecutive_failures": 3,
  "disabled_until": 1737445800
}
```

### NVIDIA NIM 典型错误

```json
{
  "error_kind": "empty_response",
  "credential_id": 18,
  "model": "nvidia/llama-3.1-nemotron-ultra-253b-instruct",
  "consecutive_failures": 5,
  "disabled_until": 1737445900,
  "note": "Provider 18 has ~13% empty response rate"
}
```

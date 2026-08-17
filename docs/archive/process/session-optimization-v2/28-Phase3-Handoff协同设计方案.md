# Phase 3 设计方案：Goal 模式与 Handoff 协同

> **版本**: v1.0
> **状态**: 设计阶段
> **创建日期**: 2026-07-19
> **实施状态**: 待实施（Handoff 需先修复）

---

> ⚠️ **当前实施状态勘误（2026-07-23）**
> 
> 本文档描述的 Goal ↔ Handoff 四组件协同（ContextMonitor、GoalStateSerializer、HandoffTrigger、HandoffMessage）为**设计提案，尚未实现**。
> 
> **当前实际状态**：
> - ✅ Handoff 请求侧 Hook 已实现（`domains/hooks/handoff/trigger_hook.go`）
> - ✅ Handoff SQL 已修复（Migration 354）
> - ❌ Goal 状态序列化和四组件协同未实现
> 
> **原因**：Goal/Handoff 状态传递接口未冻结，需先定义稳定契约。
> 
> **相关决策**：见 [31-当前实现基线与修正决策.md](./31-当前实现基线与修正决策.md) § ADR-HANDOFF-001

---

## 1. 背景与现状

### 1.1 Goal 模式现状

**已完成**：Phase 0-2
- ✅ Phase 0: 成本模式预设（minimal/balanced/aggressive）
- ✅ Phase 1: 错误重试
- ✅ Phase 2: 自动继续 + 循环检测 + 代码审计

**特点**：
- 会话可能很长（多次自动继续）
- Token 消耗较大（aggressive 模式 500K/session）
- 需要上下文管理机制

### 1.2 Handoff 功能现状

**状态**: 🔴 已停用（2026-07-06）

**停用原因**：
- 引用了不存在的 `sessions` 表
- 实际 session 追踪在 `session_summaries` (PG) + Redis

**备份位置**：
```
_to-be-deprecated/hooks-handoff-20260706/trigger_hook.go
```

**已完成的数据库迁移**：
- Migration 354: 添加 handoff 追踪列到 `session_summaries`
  - `handoff_count INT`
  - `last_handoff_at TIMESTAMP`
- 创建 `handoff_logs` 表（记录每次 handoff）

**需要修复的内容**：
1. SQL 引用：`sessions` → `session_summaries`
2. 字段映射：`total_tokens_used` → `total_tokens`
3. 接入 Phase 4 response interceptor 管道

---

## 2. 问题定义

### 2.1 核心问题

Goal 模式会话可能遇到上下文上限：

```
用户请求 → Goal 自动继续（7次）→ 代码审计 → 自动修正
  ↓
Token 累积：10K → 50K → 100K → 150K → 180K (接近 200K 上限)
  ↓
上下文即将耗尽，但任务未完成
  ↓
需要 Handoff 机制平滑交接到新会话
```

### 2.2 协同需求

Goal 模式需要 Handoff 提供：

1. **自动触发**：检测到上下文接近上限时自动触发
2. **状态保留**：携带 Goal 配置和执行状态到新会话
3. **无缝继续**：新会话继承 Goal 模式，继续执行

---

## 3. 设计方案

### 3.1 架构概览

```
┌─────────────────────────────────────────────────────────────┐
│                     Goal Mode Session                       │
│                                                             │
│  [用户请求] → [自动继续] → [审计] → [修正]                  │
│                    ↓                                         │
│              Token 累积监控                                  │
│                    ↓                                         │
│         达到阈值（180K / 90%）                               │
│                    ↓                                         │
│         触发 Context Handoff                                 │
└─────────────────┬───────────────────────────────────────────┘
                  │
                  │ Handoff Payload
                  │ - Goal State
                  │ - cost_mode
                  │ - 执行历史
                  │ - 任务上下文
                  │
                  ↓
┌─────────────────────────────────────────────────────────────┐
│                   New Goal Mode Session                     │
│                                                             │
│  [恢复 Goal 状态] → [继续执行] → [完成任务]                 │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 核心组件

#### 组件 1：ContextMonitor（上下文监控器）

**职责**：监控当前会话的 token 使用情况

**实现位置**：`domains/hooks/goal/context_monitor.go`

**接口**：
```go
type ContextMonitor interface {
    // ShouldHandoff 检查是否应触发 handoff
    ShouldHandoff(tokensUsed, contextWindow int) (bool, string)

    // GetHandoffThreshold 获取 handoff 阈值（按 cost_mode）
    GetHandoffThreshold(costMode string) HandoffThreshold
}

type HandoffThreshold struct {
    AbsoluteTokens int     // 绝对阈值（如 180K）
    Percentage     float64 // 百分比阈值（如 0.90）
}
```

**触发条件**：

| Cost Mode | 绝对阈值 | 百分比阈值 | 说明 |
|-----------|---------|-----------|------|
| minimal | 25K | 0.85 | 会话预算 30K，留 5K 余量 |
| balanced | 85K | 0.85 | 会话预算 100K，留 15K 余量 |
| aggressive | 425K | 0.85 | 会话预算 500K，留 75K 余量 |

**计算逻辑**：
```go
func (m *ContextMonitor) ShouldHandoff(tokensUsed, contextWindow int) (bool, string) {
    threshold := m.GetHandoffThreshold(m.costMode)

    // 检查绝对阈值
    if tokensUsed >= threshold.AbsoluteTokens {
        return true, fmt.Sprintf("absolute_threshold:%d", threshold.AbsoluteTokens)
    }

    // 检查百分比阈值
    if float64(tokensUsed) / float64(contextWindow) >= threshold.Percentage {
        return true, fmt.Sprintf("percentage_threshold:%.0f%%", threshold.Percentage*100)
    }

    return false, ""
}
```

---

#### 组件 2：GoalStateSerializer（状态序列化器）

**职责**：序列化 Goal 模式的执行状态

**实现位置**：`domains/hooks/goal/state_serializer.go`

**数据结构**：
```go
type GoalState struct {
    // 配置
    CostMode string `json:"cost_mode"`

    // 执行统计
    RetryCount      int `json:"retry_count"`
    ContinueCount   int `json:"continue_count"`
    LoopDetections  int `json:"loop_detections"`
    ModelSwitches   int `json:"model_switches"`

    // 审计历史
    AuditCount      int `json:"audit_count"`
    FixCount        int `json:"fix_count"`

    // 上下文信息
    TokensUsed      int    `json:"tokens_used"`
    MessageCount    int    `json:"message_count"`
    LastModel       string `json:"last_model"`

    // 任务上下文
    TaskDescription string   `json:"task_description"`
    CompletedSteps  []string `json:"completed_steps"`
    RemainingWork   string   `json:"remaining_work"`
}
```

**接口**：
```go
type GoalStateSerializer interface {
    // Serialize 序列化当前 Goal 状态
    Serialize(ctx context.Context, sessionID string) (*GoalState, error)

    // Deserialize 反序列化并恢复 Goal 状态
    Deserialize(ctx context.Context, state *GoalState) error
}
```

---

#### 组件 3：HandoffTrigger（Handoff 触发器）

**职责**：在合适时机触发 Handoff

**实现位置**：`domains/hooks/goal/handoff_trigger.go`

**触发时机**：

1. **每次 LLM 响应后**（InterceptResponse）
   - 检查 token 使用量
   - 达到阈值 → 触发 handoff

2. **自动继续前**（BeforeContinue）
   - 预估继续后的 token 消耗
   - 如果会超限 → 先 handoff 再继续

3. **审计完成后**（AfterAudit）
   - 如果审计发现大量问题需要修正
   - 预估修正成本，可能触发 handoff

**实现逻辑**：
```go
func (t *HandoffTrigger) CheckAndTrigger(ctx context.Context, req *InterceptRequest) error {
    // 1. 检查是否达到阈值
    shouldHandoff, reason := t.monitor.ShouldHandoff(req.TokensUsed, req.ContextWindow)
    if !shouldHandoff {
        return nil
    }

    // 2. 序列化 Goal 状态
    goalState, err := t.serializer.Serialize(ctx, req.SessionID)
    if err != nil {
        return err
    }

    // 3. 构建 Handoff 消息
    handoffMsg := t.buildHandoffMessage(goalState)

    // 4. 记录 handoff 日志
    err = t.recordHandoff(ctx, req.SessionID, goalState, reason)

    // 5. 返回 Handoff 指令
    return &HandoffInstruction{
        TriggerReason: reason,
        GoalState:     goalState,
        HandoffPrompt: handoffMsg,
    }
}
```

---

#### 组件 4：HandoffMessage Builder（消息构建器）

**职责**：构建传递给新会话的 Handoff 消息

**消息格式**：
```markdown
/handoff

## Context Handoff - Goal Mode

**Reason**: Token limit approaching (180K/200K, 90%)

**Goal Configuration**:
- Cost Mode: aggressive
- Session Budget: 500K tokens
- Current Usage: 180K tokens

**Execution History**:
- Retries: 2 (max: 5)
- Auto-continues: 5 (max: 7)
- Loop detections: 1
- Model switches: 2
- Audits completed: 1 (3 issues found, 2 fixed)

**Task Context**:
Task: 实现用户注册功能

Completed Steps:
1. ✅ Created User model
2. ✅ Created registration API endpoint
3. ✅ Added input validation
4. ✅ Implemented password hashing

Remaining Work:
- Add email verification
- Write integration tests
- Update API documentation

**Instructions for New Session**:
1. Continue in Goal mode with cost_mode=aggressive
2. Resume from "Add email verification"
3. Maintain the same coding style and patterns
4. Complete remaining tasks with auto-continue enabled

Current tokens: 180K
Context window: 200K
```

---

### 3.3 集成点

#### 集成点 1：ModeHook（Goal 模式主钩子）

**位置**：`domains/hooks/goal/mode_hook.go`

**修改**：
```go
type ModeHook struct {
    // 现有组件
    completionDetector *CompletionDetector
    auditHook          *AuditHook
    historyStore       *HistoryStore

    // 新增组件（Phase 3）
    contextMonitor     *ContextMonitor     // 上下文监控
    handoffTrigger     *HandoffTrigger     // Handoff 触发器
    stateSerializer    *GoalStateSerializer // 状态序列化
}

func (h *ModeHook) InterceptResponse(ctx context.Context, req *InterceptRequest) error {
    // 1. 现有逻辑：完成检测 + 审计
    // ...

    // 2. 新增：上下文监控（Phase 3）
    if h.handoffTrigger != nil {
        err := h.handoffTrigger.CheckAndTrigger(ctx, req)
        if err != nil {
            // Handoff 已触发，返回特殊指令
            return err
        }
    }

    return nil
}
```

---

#### 集成点 2：Handoff 包修复

**位置**：`domains/hooks/handoff/trigger_hook.go`

**需要修复的 SQL 查询**：

**❌ 旧代码（已停用）**：
```go
// 引用不存在的 sessions 表
query := `
    SELECT total_tokens_used, handoff_count
    FROM sessions
    WHERE id = $1
`
```

**✅ 新代码（需修复）**：
```go
// 使用实际存在的 session_summaries 表
query := `
    SELECT total_tokens, handoff_count
    FROM session_summaries
    WHERE session_key = $1
`
```

**修复映射表**：

| 旧字段（sessions） | 新字段（session_summaries） | 说明 |
|-------------------|---------------------------|------|
| `id` | `session_key` | 会话主键 |
| `total_tokens_used` | `total_tokens` | Token 使用量 |
| `handoff_count` | `handoff_count` | Handoff 次数（已迁移） |
| `last_handoff_at` | `last_handoff_at` | 最后 Handoff 时间（已迁移） |

---

### 3.4 配置项

新增 Settings 配置（`settings/goal_specs.go`）：

```go
// Handoff 相关配置
{
    Key:             "goal.handoff.enabled",
    Type:            settings.TypeBool,
    DefaultValue:    false, // 默认禁用（需先修复 Handoff）
    Description:     "启用 Goal 模式自动 Handoff",
},
{
    Key:             "goal.handoff.token_threshold_percentage",
    Type:            settings.TypeFloat,
    DefaultValue:    0.85,
    Description:     "触发 Handoff 的 token 百分比阈值（0.0-1.0）",
},
{
    Key:             "goal.handoff.preserve_state",
    Type:            settings.TypeBool,
    DefaultValue:    true,
    Description:     "Handoff 时保留 Goal 状态",
},
```

**集成到 ModePreset**：

```go
type ModePreset struct {
    // ... 现有字段

    // Phase 3: Handoff
    HandoffEnabled       bool
    HandoffTokenPercent  float64
    HandoffPreserveState bool
}

// 更新三种模式
var ModePresets = map[CostMode]ModePreset{
    CostModeMinimal: {
        // ... 现有配置
        HandoffEnabled:      false, // minimal 不启用
    },

    CostModeBalanced: {
        // ... 现有配置
        HandoffEnabled:      true,  // balanced 启用
        HandoffTokenPercent: 0.85,
    },

    CostModeAggressive: {
        // ... 现有配置
        HandoffEnabled:      true,  // aggressive 启用
        HandoffTokenPercent: 0.90,  // 更高阈值（更大预算）
    },
}
```

---

## 4. 数据流设计

### 4.1 正常流程（无 Handoff）

```
用户请求
  ↓
Goal Mode Hook
  ↓
自动继续（5次）
  ↓
任务完成
  ↓
代码审计
  ↓
返回结果

Token 使用：60K / 100K (60%)
```

### 4.2 Handoff 触发流程

```
用户请求
  ↓
Goal Mode Hook
  ↓
自动继续（7次）
  ↓
ContextMonitor 检测：85K / 100K (85%) → 触发 Handoff
  ↓
GoalStateSerializer 序列化状态
  ↓
HandoffTrigger 构建 Handoff 消息
  ↓
记录 handoff_logs
  ↓
更新 session_summaries.handoff_count
  ↓
返回 Handoff 指令 + 新会话提示

━━━━━━━━━━━━━━━ 新会话 ━━━━━━━━━━━━━━━

用户粘贴 Handoff 提示（或自动触发）
  ↓
Goal Mode Hook 检测到 Handoff 恢复
  ↓
GoalStateSerializer 反序列化状态
  ↓
恢复 cost_mode、执行历史、任务上下文
  ↓
继续执行剩余任务
  ↓
任务完成
  ↓
代码审计
  ↓
返回结果

会话 1 Token: 85K
会话 2 Token: 50K
总计：135K（避免了超限）
```

---

## 5. 实施步骤

### Phase 3.1：修复 Handoff 基础功能

**优先级**：P0（前置条件）

**任务**：
1. 修复 `trigger_hook.go` 中的 SQL 引用
2. 更新字段映射（sessions → session_summaries）
3. 恢复 Handoff 到 `domains/hooks/handoff/`
4. 接入 Phase 4 response interceptor 管道
5. 编写单元测试

**验证**：
- Handoff 可以正常触发
- handoff_logs 正确记录
- session_summaries.handoff_count 正确更新

---

### Phase 3.2：实现 Goal 状态序列化

**优先级**：P0

**任务**：
1. 创建 `GoalState` 数据结构
2. 实现 `GoalStateSerializer`
3. 在 ModeHook 中收集状态
4. 实现序列化/反序列化逻辑
5. 编写单元测试

**验证**：
- 状态可以正确序列化为 JSON
- 反序列化后状态一致
- 新会话可以恢复状态

---

### Phase 3.3：实现上下文监控与触发

**优先级**：P0

**任务**：
1. 创建 `ContextMonitor`
2. 定义各 cost_mode 的阈值
3. 实现 `HandoffTrigger`
4. 集成到 ModeHook
5. 实现 Handoff 消息构建器
6. 编写单元测试

**验证**：
- 达到阈值时正确触发
- Handoff 消息格式正确
- 新会话提示清晰易懂

---

### Phase 3.4：集成测试与文档

**优先级**：P1

**任务**：
1. 端到端测试（触发 Handoff）
2. 端到端测试（新会话恢复）
3. 压测（验证阈值设置）
4. 编写用户文档
5. 更新配置指南

**验证**：
- 完整流程可用
- 用户可以理解如何使用
- 文档完整

---

## 6. 风险与挑战

### 6.1 技术风险

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| Handoff 修复失败 | Phase 3 无法实施 | 先在测试环境验证 SQL 修复 |
| 状态序列化不完整 | 新会话无法正确恢复 | 完整的单元测试覆盖 |
| 阈值设置不当 | 过早或过晚触发 | 通过压测调优 |
| 新会话恢复失败 | 用户需要手动重新开始 | 提供回退机制 |

### 6.2 用户体验风险

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| Handoff 消息过于复杂 | 用户不理解如何继续 | 简化消息格式，提供示例 |
| 新会话启动成本高 | 用户体验差 | 自动化新会话创建 |
| 状态丢失 | 任务需要重新开始 | 在 handoff_logs 持久化状态 |

### 6.3 成本风险

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| Handoff 增加 token 消耗 | 成本上升 | 限制 Handoff 次数（最多 2-3 次） |
| 状态序列化消息过长 | 占用新会话空间 | 精简状态，只保留关键信息 |

---

## 7. 成功标准

### 7.1 功能标准

- ✅ Handoff 可以在达到阈值时自动触发
- ✅ Goal 状态可以完整序列化和恢复
- ✅ 新会话可以无缝继续任务
- ✅ handoff_logs 正确记录所有 Handoff

### 7.2 性能标准

- ✅ 上下文监控开销 < 1ms per request
- ✅ 状态序列化时间 < 10ms
- ✅ Handoff 消息大小 < 2KB
- ✅ 新会话启动时间 < 5s

### 7.3 用户体验标准

- ✅ Handoff 消息清晰易懂
- ✅ 新会话恢复成功率 > 95%
- ✅ 用户可以选择是否启用 Handoff
- ✅ 文档完整，有使用示例

---

## 8. 未来扩展

### 8.1 多次 Handoff 支持

当前设计支持单次 Handoff。未来可以支持：
- 会话链：Session A → Session B → Session C
- Handoff 历史追踪
- 跨会话的任务进度追踪

### 8.2 智能阈值调整

当前阈值是静态配置。未来可以：
- 根据任务复杂度动态调整
- 根据历史数据学习最佳阈值
- 预测任务所需 token，提前触发

### 8.3 协作式 Handoff

当前是单向交接。未来可以：
- 多个会话协作完成任务
- 并行处理不同子任务
- 合并多个会话的结果

---

## 9. 参考资料

### 9.1 相关代码

- 备份 Handoff 实现：`_to-be-deprecated/hooks-handoff-20260706/trigger_hook.go`
- Goal Mode Hook：`domains/hooks/goal/mode_hook.go`
- 数据库迁移：`db/migrations/354_handoff_schema_fix.sql`

### 9.2 相关文档

- Phase 0-2 完整总结：`docs/会话优化v2/27-Phase0-2完整实现总结.md`
- Goal 模式设计：`docs/会话优化v2/16-Goal模式会话持续机制设计方案.md`

### 9.3 数据库表

**session_summaries** (已有 handoff 列)：
- `session_key VARCHAR(64)` - 会话 ID
- `total_tokens INT` - Token 使用量
- `handoff_count INT` - Handoff 次数
- `last_handoff_at TIMESTAMP` - 最后 Handoff 时间

**handoff_logs** (新表)：
- `id SERIAL PRIMARY KEY`
- `session_id VARCHAR(64)` - 原会话 ID
- `tenant_id VARCHAR(64)` - 租户 ID
- `trigger_reason VARCHAR(64)` - 触发原因
- `tokens_at_handoff INT` - Handoff 时的 Token 数
- `context_window INT` - 上下文窗口大小
- `handoff_prompt TEXT` - Handoff 消息
- `new_session_id VARCHAR(64)` - 新会话 ID（可选）
- `created_at TIMESTAMP` - 创建时间

---

## 10. 实施优先级建议

### 立即（P0）

1. **修复 Handoff 基础功能**
   - SQL 引用修复
   - 恢复到主代码库
   - 基础测试验证

### 短期（P1，1-2 周）

2. **实现 Goal 状态序列化**
3. **实现上下文监控与触发**
4. **集成测试**

### 中期（P2，1-2 月）

5. **优化阈值设置**
6. **完善用户文档**
7. **生产环境验证**

### 长期（P3，3+ 月）

8. **多次 Handoff 支持**
9. **智能阈值调整**
10. **协作式 Handoff**

---

**最后更新**: 2026-07-19
**状态**: 设计完成，待实施
**下一步**: 修复 Handoff 基础功能（Phase 3.1）

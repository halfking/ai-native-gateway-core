# 2026-11-19 auto模式context_budget_exceeded不换模问题分析

## 一、问题现象

211生产环境发现3笔auto模式请求失败：
- 时间：2026-11-19 03:38-03:39
- 失败原因：`context_budget_exceeded`
- 选择的模型：`glm-5.2`
- 现象：**多次出错后不更换模型**，直接返回失败

## 二、根因分析

### 2.1 错误分类链路

1. **错误产生**：`context_budget_exceeded`在`domains/streaming/executors/context_budget.go`被抛出
2. **错误分类**：在`errorsx/classify.go:138`被分类为`KindContextLength`
3. **动作决策**：在`errorsx/action_policy.go:243`，`KindContextLength`被列入`terminalActionKind`
4. **终止执行**：`DecideNextAction`第319-322行返回`ActionFailTerminal`
5. **不触发换模**：`PlanAfterFailure`第274行返回`NextActionFailed`

### 2.2 关键代码位置

**errorsx/action_policy.go:240-259**
```go
func terminalActionKind(kind ErrorKind) bool {
    switch kind {
    case KindAuthRevoked, KindQuotaPermanent, KindModelNotFound,
        KindModelDeprecated, KindContextLength, KindUnsupportedFeature,  // ← 这里！
        KindContentFilter, KindConversion, KindUpstreamContextLoss,
        KindToolCallIdMismatch, KindClientBug,
        KindCircuitOpen, KindFpSlotSaturated:
        return true
    default:
        return false
    }
}

func DecideNextAction(ctx DecisionContext) ActionDecision {
    // ...
    if terminalActionKind(ctx.Kind) {  // ← KindContextLength走这里
        base.Action = ActionFailTerminal
        base.ReasonCode = string(ctx.Kind)
        return base  // ← 直接返回失败，不触发换模
    }
    // ...
}
```

**domains/dispatch/planner.go:186-283**
```go
func PlanAfterFailure(qr *QueuedRequest, out ForwardOutcome, cfg Config) Decision {
    // ...
    action := errorsx.DecideNextAction(...)
    // ...
    switch action.Action {
    case errorsx.ActionFailTerminal, errorsx.ActionResumeBlocked:
        return Decision{Action: NextActionFailed, Reason: action.ReasonCode}  // ← 返回失败
    default:
        return Decision{Reason: action.ReasonCode}  // ← 允许继续（换模）
    }
}
```

**domains/dispatch/failover.go:202-205**
```go
if next == nil {
    // No sibling credential available under this model → model change.
    p.tryModelChangeOutcome(qr, out)  // ← 但PlanAfterFailure已经返回NextActionFailed
    return
}
```

### 2.3 为什么`KindContextLength`被设计为终止性错误

注释见`errorsx/classify.go:126-138`：
```go
// KindContextLength: upstream rejected the request because the prompt
// (or accumulated conversation history) exceeded the model's context
// window. Distinct from KindTransient so the executor can:
//   1. Surface a structured retry-after-trim path
//      (routing/executor_chat.go attempts one trim+retry before bubbling
//      the 4xx up to the client).
//   2. Avoid re-submitting the same oversized body to a different credential.
```

**历史设计意图**：
- `context_length_exceeded`（上游返回的400/422）应该在executor层做trim+retry
- 避免把同样超长的body发给下一个节点
- 因此设计为"终止性"，不走换模梯子

### 2.4 `context_budget_exceeded`与`context_length_exceeded`的区别

| 错误类型 | 产生位置 | 含义 | 当前处理 | 应有处理 |
|---------|---------|------|---------|---------|
| `context_length_exceeded` | 上游返回400/422 | **请求body本身超过模型窗口** | trim+retry或终止 | ✓ 正确 |
| `context_budget_exceeded` | 网关executor预算检查 | **请求超过bucket预算限制** | 终止 | ✗ **应换模** |

**关键差异**：
- `context_length_exceeded`：body确实太大，换模型也可能失败（除非换到更大窗口的模型）
- `context_budget_exceeded`：**不是模型能力问题，是bucket配置问题** —— auto决策器**选错了bucket**（选了小窗口模型glm-5.2处理大上下文任务）

### 2.5 auto决策为什么会选glm-5.2

需要进一步调查：
1. 请求的实际token数
2. glm-5.2的context window配置
3. auto决策时的候选池
4. 为什么没有选择更大窗口的模型（如glm-5.3、gpt-5.4等）

## 三、影响范围

### 3.1 触发条件

1. 使用`model=auto`
2. 请求的token数超过auto**首选模型**的context window
3. 候选池中存在更大窗口的可用模型

### 3.2 预期行为 vs 实际行为

**预期**：
```
auto选glm-5.2 → context_budget_exceeded → 换模到glm-5.3/gpt-5.4 → 成功
```

**实际**：
```
auto选glm-5.2 → context_budget_exceeded → 直接返回422 → 失败
```

### 3.3 数据验证

从211的验证脚本输出：
```
2026-11-19 03:39:14.330057+08 | glm-5.2 | auto | f | context_budget_exceeded
2026-11-19 03:39:03.389482+08 | glm-5.2 | auto | f | context_budget_exceeded  
2026-11-19 03:38:55.161595+08 | glm-5.2 | auto | f | context_budget_exceeded
```

- 3笔失败
- 全部是`glm-5.2`
- 全部是`context_budget_exceeded`
- **没有看到任何换模尝试**（否则应该有`model_change`事件或其他模型的尝试记录）

## 四、修复方案

### 方案A：区分两种context错误（推荐）

**核心思路**：`context_budget_exceeded`不应该复用`KindContextLength`

1. **新增ErrorKind**：
```go
// errorsx/classify.go
KindContextBudgetExceeded ErrorKind = "context_budget_exceeded"
```

2. **修改分类器**：
```go
// domains/streaming/executors/context_budget.go
// 抛出错误时使用新kind
return &ContextBudgetError{
    Model: model,
    Cause: fmt.Errorf("request tokens exceed bucket budget"),
}

// errorsx/classify.go
func ClassifyError(err error, resp *http.Response) ErrorKind {
    // ...
    if errors.As(err, &ContextBudgetError{}) {
        return KindContextBudgetExceeded
    }
    // ...
}
```

3. **注册为可换模错误**：
```go
// errorsx/action_policy.go
// 不要把KindContextBudgetExceeded加入terminalActionKind

// errorsx/penalty.go
func PenaltyOf(kind ErrorKind) Penalty {
    switch kind {
    case KindContextBudgetExceeded:
        return Penalty{Target: PenaltyNone}  // 不罚节点
    // ...
    }
}
```

4. **在planner中登记**：
```go
// domains/dispatch/planner.go:isCentralPolicyKind
case KindContextBudgetExceeded:
    return true
```

**优点**：
- 语义清晰，两种错误完全分离
- 不影响现有的`context_length_exceeded`处理逻辑
- 符合"bucket预算问题应该换模"的语义

**缺点**：
- 需要修改多个文件
- 需要新增迁移（如果要记录历史）

### 方案B：条件化terminalActionKind（次选）

**核心思路**：根据错误来源决定是否terminal

```go
// errorsx/action_policy.go
func terminalActionKind(kind ErrorKind) bool {
    switch kind {
    case KindAuthRevoked, KindQuotaPermanent, KindModelNotFound,
        KindModelDeprecated, KindUnsupportedFeature,
        KindContentFilter, KindConversion, KindUpstreamContextLoss,
        KindToolCallIdMismatch, KindClientBug,
        KindCircuitOpen, KindFpSlotSaturated:
        return true
    // KindContextLength移除，改为在DecideNextAction中条件判断
    default:
        return false
    }
}

func DecideNextAction(ctx DecisionContext) ActionDecision {
    // ...
    
    // 特殊处理KindContextLength
    if ctx.Kind == KindContextLength {
        // 如果是bucket预算问题（由错误message判断），允许换模
        if strings.Contains(ctx.ErrorMessage, "bucket budget") {
            // 走正常的换模流程
        } else {
            // 上游真正的context length错误，terminal
            base.Action = ActionFailTerminal
            base.ReasonCode = string(ctx.Kind)
            return base
        }
    }
    
    if terminalActionKind(ctx.Kind) {
        // ...
    }
    // ...
}
```

**优点**：
- 改动较小
- 不需要新增ErrorKind

**缺点**：
- 依赖错误消息字符串判断，不够健壮
- 语义混淆（一个kind两种处理方式）

### 方案C：让auto决策器避免选小窗口模型（治标不治本）

**核心思路**：在auto决策阶段就考虑token数，避免选择窗口不足的模型

```go
// autoroute/decision.go
func (d *Decider) SelectModel(req Request) (string, []string) {
    // 计算请求token数
    tokenCount := estimateTokens(req)
    
    // 过滤掉窗口不足的候选
    candidates := d.filterByContextWindow(tokenCount)
    
    // ...
}
```

**优点**：
- 从源头避免问题
- 不需要修改错误处理链路

**缺点**：
- **治标不治本**：即使auto选对了，其他情况下仍然会遇到context_budget_exceeded
- 无法处理动态变化的token数（如streaming中途超budget）
- 仍然需要方案A或B作为兜底

## 五、推荐修复路径

**短期（本轮）**：方案A
**中期**：方案A + 方案C（双重保险）
**长期**：考虑bucket预算系统的重构（超预算时动态升级bucket而非失败）

## 六、验证计划

1. **单元测试**：
   - `TestContextBudgetExceededTriggersModelChange`
   - 验证KindContextBudgetExceeded走换模路径
   - 验证KindContextLength仍然terminal

2. **集成测试**：
   - 构造超预算请求
   - 验证auto→glm-5.2→context_budget_exceeded→换模到glm-5.3→成功

3. **211生产验证**：
   - 部署修复
   - 重放失败的3笔请求
   - 观察是否触发换模
   - 观察最终是否成功

## 七、遗留问题（待调查）

1. **L1**：那3笔请求的完整request_id和请求内容（需要查询数据库）
2. **L2**：auto决策器为什么首选glm-5.2而不是更大窗口模型
3. **L3**：glm-5.2的context window配置是多少
4. **L4**：候选池中有哪些更大窗口的可用模型
5. **L5**：是否需要在auto决策阶段就考虑token数（方案C）

## 八、关联问题

- 前序任务：2026-11-17~19 auto模式六修复（F1-F6）已完成
- 本问题：F7 - context_budget_exceeded不触发换模
- 可能的F8：auto决策器窗口感知优化

## 九、时间线

- 2026-11-19 03:38-03:39：问题发生（3笔失败）
- 2026-11-19 上午：问题分析开始
- 2026-11-19 当前：根因确认，**修复已完成**

## 十、修复实施记录

### 修复内容

按方案A实施，新增`KindContextBudgetExceeded`并区分两种context错误：

1. **errorsx/classify.go**
   - 新增`KindContextBudgetExceeded` ErrorKind常量
   - 新增`isContextBudgetError`辅助函数
   - 在`ClassifyError`开头添加ContextBudgetError检测

2. **errorsx/penalty.go**
   - 将`KindContextBudgetExceeded`加入PenaltyNone（不罚节点）

3. **errorsx/action_policy.go**
   - 为`KindContextBudgetExceeded`添加专门的case
   - 返回`ActionSwitchNode`（允许换模）
   - 不写任何节点健康状态

4. **domains/dispatch/planner.go**
   - 将`KindContextBudgetExceeded`登记到`isCentralPolicyKind`
   - 防止被重写为`KindTransient`

### 测试覆盖

**单元测试（errorsx/context_budget_test.go）**：
- TestContextBudgetExceededClassification：错误分类测试
- TestContextBudgetExceededPenalty：惩罚策略测试
- TestContextBudgetExceededNotTerminal：非终止性测试
- TestContextLengthStillTerminal：确保KindContextLength仍为terminal
- TestContextBudgetErrorDetection：错误检测辅助函数测试

**集成测试（domains/dispatch/context_budget_modelchange_test.go）**：
- TestContextBudgetExceededTriggersModelChange：完整换模流程测试
- TestContextLengthStillTerminalInPlanner：确保上游context_length保持terminal
- TestIsCentralPolicyKindIncludesContextBudgetExceeded：登记验证

**测试结果**：全部通过 ✓

### 验证状态

- [x] 编译通过
- [x] 单元测试全绿（errorsx包）
- [x] 集成测试全绿（dispatch包）
- [x] 全包测试通过
- [ ] 211生产验证（待部署）

### 下一步

1. 提交代码并推送
2. 部署到211
3. 重放失败的3笔请求或构造类似场景
4. 观察日志确认触发model_change
5. 验证最终成功

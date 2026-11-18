# 2026-11-19 auto模式context_budget_exceeded不换模问题修复总结

## 一、问题概述

**现象**：211生产环境发现3笔auto模式请求失败，选择glm-5.2后因`context_budget_exceeded`直接返回422，未尝试换模到更大窗口的模型。

**影响**：auto模式在处理大上下文请求时，如果首选小窗口模型，会直接失败而不是自动切换到合适的模型。

## 二、根因分析

### 2.1 错误处理链路

```
context_budget_exceeded (executor)
  ↓ ClassifyError
KindContextLength
  ↓ terminalActionKind = true
ActionFailTerminal
  ↓ PlanAfterFailure
NextActionFailed
  ↓
直接终止，不触发换模
```

### 2.2 核心问题

`context_budget_exceeded`（网关侧bucket预算检查）被错误地复用了`KindContextLength`（上游400/422），导致：
- **应该换模的场景**（auto选错bucket）被当作**终止性错误**处理
- 节点健康状态正常，但请求无法恢复

### 2.3 两种错误的本质区别

| 错误类型 | 产生位置 | 含义 | 正确处理 |
|---------|---------|------|---------|
| `context_length_exceeded` | 上游返回400/422 | 模型真的放不下 | terminal（或trim+retry） |
| `context_budget_exceeded` | 网关executor预算检查 | auto选错bucket | **换模到更大窗口模型** |

## 三、修复方案

### 3.1 方案设计

新增`KindContextBudgetExceeded`，与`KindContextLength`完全分离：

```go
// 上游错误（保持terminal）
KindContextLength ErrorKind = "context_length_exceeded"

// 网关侧路由错误（新增，允许换模）
KindContextBudgetExceeded ErrorKind = "context_budget_exceeded"
```

### 3.2 修改清单

#### 1. errorsx/classify.go
- 新增`KindContextBudgetExceeded`常量定义
- 新增`isContextBudgetError`辅助函数（通过错误消息前缀判断）
- 在`ClassifyError`开头添加检测，优先于其他分类

#### 2. errorsx/penalty.go
- 将`KindContextBudgetExceeded`加入`PenaltyNone`分支
- 不罚节点、不拉黑绑定、不累计连败

#### 3. errorsx/action_policy.go
- 为`KindContextBudgetExceeded`添加专门case
- 返回`ActionSwitchNode`（允许继续到换模梯子）
- 不写任何节点健康状态

#### 4. domains/dispatch/planner.go
- 将`KindContextBudgetExceeded`登记到`isCentralPolicyKind`
- 防止被重写为`KindTransient`（事故放大器）

### 3.3 代码变更统计

```
7 files changed, 682 insertions(+)
- 4个核心文件修改
- 2个新测试文件
- 1个分析文档
```

## 四、测试验证

### 4.1 单元测试（errorsx/context_budget_test.go）

```go
TestContextBudgetExceededClassification     // 错误分类 ✓
TestContextBudgetExceededPenalty            // 惩罚策略 ✓
TestContextBudgetExceededNotTerminal        // 非终止性 ✓
TestContextLengthStillTerminal              // KindContextLength仍terminal ✓
TestContextBudgetErrorDetection             // 辅助函数 ✓
```

**结果**：5/5 PASS

### 4.2 集成测试（domains/dispatch/context_budget_modelchange_test.go）

```go
TestContextBudgetExceededTriggersModelChange    // 完整换模流程 ✓
TestContextLengthStillTerminalInPlanner         // 上游错误仍terminal ✓
TestIsCentralPolicyKindIncludes...              // 登记验证 ✓
```

**结果**：3/3 PASS

### 4.3 回归测试

- errorsx包：全绿
- domains/dispatch包：全绿（30.454s）
- 编译通过：`go build ./errorsx/ ./domains/dispatch/`

## 五、修复效果

### 5.1 预期行为（修复后）

```
auto选glm-5.2 
  ↓
context_budget_exceeded
  ↓ ClassifyError
KindContextBudgetExceeded
  ↓ DecideNextAction
ActionSwitchNode
  ↓ PlanAfterFailure
允许继续
  ↓ failover.move
无兄弟凭据
  ↓ tryModelChangeOutcome
PlanModelChange
  ↓
换模到glm-5.3（更大窗口）
  ↓
成功 ✓
```

### 5.2 关键改进

1. **不惩罚节点**：PenaltyNone，保护节点健康状态
2. **允许换模**：ActionSwitchNode → 继续failover梯子
3. **不是terminal**：不在terminalActionKind中
4. **防止重写**：在isCentralPolicyKind登记

### 5.3 保持不变

- `KindContextLength`（上游400/422）**仍然是terminal**
- 现有的context_length处理逻辑**完全不变**
- 其他错误类型的行为**零影响**

## 六、待验证项

### 6.1 生产验证计划

1. **部署到211**
   - 拉取最新代码（已包含修复）
   - 运行部署脚本
   - 确认版本号

2. **构造测试场景**
   - 发送超过glm-5.2窗口但在glm-5.3窗口内的请求
   - 使用`model=auto`
   - 观察日志

3. **验证指标**
   - 日志中出现`model_change`事件
   - 从glm-5.2切换到glm-5.3（或其他更大窗口模型）
   - 最终请求成功（200）
   - `error_kind=context_budget_exceeded`但`auto_reselect`计数增加

4. **回滚准备**
   - 确认回滚路径
   - 准备回滚脚本

### 6.2 监控观察

- `error_kind=context_budget_exceeded`的请求是否减少
- `model_change`事件是否增加（针对bucket超限场景）
- auto模式的成功率是否提升
- glm-5.2的失败率是否下降（因为会切走）

## 七、遗留问题

### L1（本轮已修复）
✅ context_budget_exceeded不触发换模 → **已修复**

### L2（待后续优化）
auto决策器在选择模型时**未考虑请求的token数**，可能首选小窗口模型 → 建议在决策阶段就过滤掉窗口不足的候选（方案C）

### L3（待调查）
那3笔失败请求的完整日志（request_id、请求内容、token数） → 需要查询数据库

### L4（待调查）
auto决策器为什么首选glm-5.2而不是glm-5.3 → 需要分析决策逻辑

## 八、通用教训

1. **错误分类要精确**：gateway侧错误与upstream错误不能混用同一个kind
2. **terminal要慎重**：被标记为terminal的错误会跳过所有恢复机制
3. **测试要分层**：单元测试（分类/惩罚）+ 集成测试（换模流程）+ 生产验证
4. **文档要详尽**：每个case都要注释清楚「三个不」与「一个要」

## 九、提交信息

- **Commit**: `ed5654503`
- **分支**: main
- **时间**: 2026-11-19
- **标题**: F7: 修复auto模式context_budget_exceeded不触发换模问题
- **文件**: 7 files changed, 682 insertions(+)

## 十、后续行动

- [ ] 部署到211生产环境
- [ ] 构造测试场景验证
- [ ] 监控24小时观察效果
- [ ] 考虑实施方案C（auto决策阶段过滤小窗口模型）
- [ ] 更新运维文档

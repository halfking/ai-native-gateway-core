# Goal模式会话持续机制设计方案

> **文档版本**: v1.0
> **创建日期**: 2026-07-19
> **状态**: 设计阶段
> **关联模块**: domains/hooks/goal, domains/hooks/handoff, domains/streaming

---

## 1. 需求分析

### 1.1 核心问题

当前系统存在以下会话中断问题：

1. **LLM短时中断导致任务失败**
   - LLM供应商返回错误（rate_limit, timeout, 5xx等）
   - 网络抖动导致连接中断
   - 临时无可用节点

2. **LLM无输出导致会话停止**
   - LLM返回的response中没有tool_calls
   - finish_reason = "stop"但任务实际未完成
   - 前端误判为任务完成，停止轮询

3. **缺乏自动审计修正机制**
   - 编程类任务完成后没有自动审计
   - 发现的问题需要人工介入才能修正

### 1.2 用户需求

需要实现以下机制：

1. **错误自动重试**：LLM报错或无可用节点时，延时后自动发起重试请求
2. **任务完成检测**：智能判断任务是否真正完成，未完成则注入继续提示
3. **自动审计修正**：编程任务完成后自动审计并修正问题
4. **会话自动切换**：上下文接近上限时自动handoff到新会话

### 1.3 配置需求

- goal模式可在系统级或请求级配置
- 重试次数、延时时间可调
- 继续次数上限可调
- 支持per-tenant配置覆盖

---

## 2. 现有架构分析

### 2.1 已有模块梳理

#### 2.1.1 Goal模块 (`domains/hooks/goal`)

**核心能力**：
- ✅ **任务完成检测**：`CompletionDetector`通过多种策略判断任务是否完成
  - keyword策略（检测"完成"等关键词）
  - heuristic策略（finish_reason="stop" + 无tool_calls）
  - llm策略（调用LLM判断）
  - hybrid策略（组合以上策略）

- ✅ **自动继续**：`ModeHook.InterceptNonStream/InterceptStreamEnd`
  - 任务未完成时注入"请继续"follow-up
  - 原子计数保证并发安全（`AtomicAutoContinue`）
  - 支持最大继续次数限制（`max_auto_continue_count`）

- ✅ **循环检测与模型切换**：`loop_detector.go`
  - 检测重复响应（`RecordResponse`计算hash）
  - 达到阈值时切换到fallback模型
  - 支持最大切换次数限制（`max_model_switch_count`）

- ✅ **自动审计**：`AuditHook`
  - 任务完成后触发审计follow-up
  - 调用LLM审查执行结果
  - 生成问题报告和修复建议

**缺失能力**：
- ❌ **错误重试**：LLM返回错误时没有自动重试机制
- ❌ **延时注入**：无法在错误后延时N秒再发起请求
- ❌ **模拟工具调用**：错误场景下无法模拟tool注入保持会话活跃

#### 2.1.2 Handoff模块 (`domains/hooks/handoff`)

**核心能力**：
- ✅ **上下文阈值检测**：
  - 绝对阈值（tokens）
  - 百分比阈值（占model上限的%）
  - 消息数阈值
  - 空闲时间阈值

- ✅ **会话摘要生成**：`SummaryEngine`
  - llm策略（LLM生成摘要）
  - rule策略（正则提取关键事实）
  - hybrid策略（LLM失败降级到rule）

- ✅ **Handoff注入**：
  - 生成`/handoff` skill调用
  - 包含会话摘要和关键事实
  - 支持冷却时间和次数上限

**缺失能力**：
- ❌ **与Goal模式协同**：handoff和goal是独立的拦截器链节点
- ❌ **任务状态传递**：handoff时未传递goal session状态

#### 2.1.3 Follow-up引擎 (`domains/streaming/response_interceptor_helpers.go`)

**核心能力**：
- ✅ **异步注入**：`injectFollowUpRequest`异步发送follow-up
- ✅ **递归深度限制**：`MaxFollowUpDepth = 15`
- ✅ **会话级限制**：`MaxFollowUpsPerSession = 50`
- ✅ **panic恢复**：保护主流程不被follow-up崩溃
- ✅ **认证降级**：parent key失败时自动尝试fallback key

**缺失能力**：
- ❌ **延时注入**：当前是100ms固定延时，无法配置
- ❌ **错误重试**：LLM错误时follow-up直接失败
- ❌ **模拟响应**：无法在LLM错误时返回模拟tool保持会话活跃

### 2.2 LLM调用流程分析

#### 2.2.1 正常流程（无错误）

```
Client Request
  ↓
ChatHandler.ServeHTTP
  ↓
[Pre-Request Hooks]
  ├─ Handoff.BeforeRequest  // 检查是否需要handoff
  │   └─ 如触发：注入/handoff skill + 会话摘要
  ↓
Routing & Dispatch
  ↓
Provider LLM Call
  ↓
Response Stream/Non-Stream
  ↓
[Response Interceptors]
  ├─ Goal.InterceptNonStream/InterceptStreamEnd
  │   ├─ 任务完成检测（CompletionDetector）
  │   │   ├─ 完成 → 注入audit follow-up
  │   │   └─ 未完成 → 注入continue follow-up
  │   └─ 循环检测（hash响应）
  │       └─ 达到阈值 → 切换模型 + 注入continue
  ├─ Audit.InterceptNonStream
  │   └─ 审计任务执行（仅当goal已标记完成）
  └─ OutputCompliance.InterceptNonStream
      └─ 脱敏处理
  ↓
Response to Client
  ↓
[Async Follow-up Processing]
  └─ injectFollowUpRequest
      ├─ 深度检查（<15）
      ├─ 会话计数（<50）
      ├─ 100ms延时
      └─ 递归调用ChatHandler
```

#### 2.2.2 错误流程（当前缺失）

```
Provider LLM Call → Error (5xx, timeout, no_candidates)
  ↓
Response Error to Client
  ↓
❌ 会话终止（没有重试机制）
```

**问题**：
1. 错误直接返回给客户端，会话中断
2. Goal session状态变为failed，但没有自动恢复
3. 前端收到错误后停止轮询

---

## 3. 方案设计

### 3.1 架构设计原则

1. **分层设计**：错误重试在routing层，任务判断在interceptor层
2. **可配置**：所有阈值、次数、延时通过settings动态配置
3. **向后兼容**：默认关闭，不影响现有行为
4. **防循环**：多层限制（重试次数、继续次数、follow-up深度）
5. **可观测**：每层记录metrics和日志

### 3.2 方案A：网关层错误重试机制（推荐）

#### 3.2.1 设计思路

**在routing层捕获LLM错误，延时后自动重试，对上层透明。**

核心思想：
- LLM错误时**不立即返回**给客户端
- 延时N秒后**内部重试**（调用routing再次选路）
- 重试成功则正常返回响应，拦截器链继续执行
- 重试失败则返回错误，goal session进入failed状态

#### 3.2.2 实现位置

在 `domains/streaming/handler.go` 的 `relayAndRespond` 方法中增加重试逻辑：

```go
// 伪代码示例
func (h *ChatHandler) relayAndRespond(...) error {
    maxRetries := h.getGoalRetryCount(tenantID)  // 从settings读取
    retryDelay := h.getGoalRetryDelay(tenantID)  // 默认20s

    for attempt := 0; attempt <= maxRetries; attempt++ {
        // 调用provider
        resp, err := h.doProviderCall(...)

        if err == nil && !isRetriableError(resp) {
            // 成功或不可重试错误，返回
            return h.processResponse(resp)
        }

        // 可重试错误（5xx, timeout, no_candidates, rate_limit等）
        if attempt < maxRetries {
            slog.Info("goal_retry_scheduled",
                "attempt", attempt+1,
                "max", maxRetries,
                "delay_sec", retryDelay,
                "error", err)

            // 延时
            time.Sleep(time.Duration(retryDelay) * time.Second)

            // 重新routing（可能选到不同provider）
            continue
        }

        // 所有重试耗尽，返回错误
        return err
    }
}
```

**优点**：
- ✅ 对拦截器链透明（goal/audit不感知重试）
- ✅ 重试时会重新routing，可能选到不同provider/credential
- ✅ 实现简单，改动集中
- ✅ 支持所有错误类型（5xx, timeout, no_candidates等）

**缺点**：
- ⚠️ 同步阻塞（延时期间占用goroutine）
- ⚠️ 不能跨请求重试（client需保持连接）

#### 3.2.3 配置设计

新增settings配置项：

```go
// settings/goal_specs.go
{
    Key:   "goal.retry_on_error",
    Scope: settings.ScopeTenant,
    Type:  "bool",
    DefaultValue: json.RawMessage(`false`),  // 默认关闭
    Description: "LLM错误时自动重试",
},
{
    Key:   "goal.max_retry_count",
    Scope: settings.ScopeTenant,
    Type:  "int",
    DefaultValue: json.RawMessage(`3`),
    Description: "最大重试次数",
},
{
    Key:   "goal.retry_delay_seconds",
    Scope: settings.ScopeTenant,
    Type:  "int",
    DefaultValue: json.RawMessage(`20`),
    Description: "重试延时（秒）",
},
{
    Key:   "goal.retriable_errors",
    Scope: settings.ScopeTenant,
    Type:  "string",
    DefaultValue: json.RawMessage(`"5xx,timeout,no_candidates,rate_limit"`),
    Description: "可重试的错误类型（逗号分隔）",
},
```

### 3.3 方案B：拦截器层模拟工具注入（备选）

#### 3.3.1 设计思路

**在response interceptor中检测错误，注入模拟tool保持会话活跃。**

核心思想：
- Goal模式激活后，拦截器检测到error response
- 不直接返回错误，而是**模拟LLM返回**了一个tool_call
- Tool名为 `retry_llm_call`，参数包含错误信息
- 前端看到tool_call，保持连接，等待下一轮
- 后端延时后自动注入follow-up执行重试

#### 3.3.2 实现伪代码

```go
// domains/hooks/goal/mode_hook.go
func (h *ModeHook) InterceptError(ctx context.Context, req *response.InterceptRequest, err error) (*response.InterceptResult, error) {
    enabled := h.loadBool(req.TenantID, "goal.enabled", h.config.Enabled)
    retryEnabled := h.loadBool(req.TenantID, "goal.retry_on_error", false)
    if !enabled || !retryEnabled {
        return nil, nil
    }

    goalSession, _ := h.db.GetSession(ctx, req.SessionID)
    if goalSession == nil {
        return nil, nil  // goal未激活
    }

    maxRetries := h.loadInt(req.TenantID, "goal.max_retry_count", 3)
    if goalSession.RetryCount >= maxRetries {
        return nil, nil  // 重试次数耗尽
    }

    // 原子递增重试计数
    h.db.IncrementRetryCount(ctx, req.SessionID)

    // 构造模拟response（包含tool_call）
    mockResponse := map[string]interface{}{
        "choices": []map[string]interface{}{
            {
                "message": map[string]interface{}{
                    "role": "assistant",
                    "content": nil,
                    "tool_calls": []map[string]interface{}{
                        {
                            "id": "retry_" + req.RequestID,
                            "type": "function",
                            "function": map[string]interface{}{
                                "name": "retry_llm_call",
                                "arguments": json.Marshal(map[string]interface{}{
                                    "error": err.Error(),
                                    "retry_count": goalSession.RetryCount + 1,
                                    "delay_seconds": 20,
                                }),
                            },
                        },
                    },
                },
                "finish_reason": "tool_calls",
            },
        },
    }

    mockBody, _ := json.Marshal(mockResponse)

    // 延时注入重试follow-up
    retryDelay := h.loadInt(req.TenantID, "goal.retry_delay_seconds", 20)
    go func() {
        time.Sleep(time.Duration(retryDelay) * time.Second)
        h.injectRetryFollowUp(ctx, req)
    }()

    return &response.InterceptResult{
        RewriteResponse: mockBody,  // 替换error response
        Action: "goal_retry_inject",
    }, nil
}
```

**优点**：
- ✅ 前端无感知（看到tool_call，自然等待）
- ✅ 异步非阻塞（延时在goroutine中）
- ✅ 可跨请求重试（不依赖client长连接）

**缺点**：
- ❌ 实现复杂（需要新增InterceptError接口）
- ❌ 模拟响应可能被前端识破
- ❌ 需要修改ResponseInterceptor接口定义

### 3.4 方案对比

| 维度 | 方案A（网关层重试） | 方案B（模拟工具注入） |
|------|-------------------|---------------------|
| **实现复杂度** | ⭐⭐ 简单 | ⭐⭐⭐⭐ 复杂 |
| **改动范围** | 仅handler.go | goal hook + interceptor接口 |
| **阻塞方式** | 同步阻塞 | 异步非阻塞 |
| **前端感知** | 透明 | 需要识别retry tool |
| **跨请求重试** | ❌ 不支持 | ✅ 支持 |
| **向后兼容** | ✅ 完全兼容 | ⚠️ 需前端配合 |
| **可观测性** | ⭐⭐⭐ 好 | ⭐⭐ 一般 |

**推荐**：**方案A（网关层重试）**
- 实现简单，风险可控
- 对现有架构零侵入
- 能覆盖90%的短时错误场景

### 3.5 任务完成检测增强（复用现有机制）

**现状**：Goal模块已实现完善的任务完成检测机制。

**增强点**：
1. ✅ 提高hybrid策略的权重（已支持，通过`goal.completion_confidence`调整）
2. ✅ finish_reason="stop"时强制LLM判断（已支持，见`shouldAutoContinue`逻辑）
3. ✅ 支持tool_calls场景的完成检测（已支持，CompletionDetector检查structured字段）

**无需新增代码**，仅需配置优化：

```yaml
# 推荐配置
goal.enabled: true
goal.detection_mode: hybrid
goal.completion_confidence: 0.75  # 提高判断信心阈值
goal.auto_continue_on_pause: true
goal.max_auto_continue_count: 5   # 允许更多次继续
```

### 3.6 自动审计修正（复用现有机制）

**现状**：Goal模块已实现audit hook。

**增强点**：
1. ✅ 编程任务检测：通过metadata hint（已支持`task_type_hint`）
2. ✅ 审计结果解析：audit hook调用LLM生成issues列表
3. ❌ **自动修正**：当前audit仅生成报告，不自动应用fix

**新增**：`goal.auto_fix_enabled`配置

```go
// domains/hooks/goal/audit_hook.go
func (h *AuditHook) InterceptNonStream(...) {
    // ... 现有审计逻辑 ...

    // 新增：自动修正
    autoFix := h.loadBool(req.TenantID, "goal.auto_fix_enabled", false)
    if autoFix && len(auditResult.Issues) > 0 {
        fixPrompt := h.buildFixPrompt(auditResult.Issues)
        return &response.InterceptResult{
            InjectFollowUp: fixPrompt,
            Action: "goal_auto_fix",
        }, nil
    }
}

func (h *AuditHook) buildFixPrompt(issues []Issue) []byte {
    prompt := "Based on the audit findings, please fix the following issues:\n\n"
    for i, issue := range issues {
        if issue.Severity == "high" || issue.Severity == "medium" {
            prompt += fmt.Sprintf("%d. %s\n   Fix: %s\n\n", i+1, issue.Description, issue.Fix)
        }
    }
    // ... 构造完整请求体 ...
}
```

---

## 4. 实施计划

### 4.1 Phase 1：网关层错误重试（核心）

**目标**：实现LLM错误时的自动重试机制

**改动**：
1. `domains/streaming/handler.go`：增加重试循环
2. `settings/goal_specs.go`：新增4个配置项
3. `domains/hooks/goal/store.go`：新增`IncrementRetryCount`方法
4. 数据库：goal_sessions表增加`retry_count INT DEFAULT 0`

**时间**：2天

### 4.2 Phase 2：审计自动修正

**目标**：审计发现问题后自动应用fix

**改动**：
1. `domains/hooks/goal/audit_hook.go`：增加auto_fix逻辑
2. `settings/goal_specs.go`：新增`goal.auto_fix_enabled`配置

**时间**：1天

### 4.3 Phase 3：与Handoff模式协同

**目标**：Goal session状态在handoff时传递

**改动**：
1. `domains/hooks/handoff/trigger_hook.go`：handoff摘要包含goal状态
2. `domains/hooks/goal/mode_hook.go`：支持从handoff恢复goal session

**时间**：1天

### 4.4 Phase 4：优化与测试

**目标**：压测、日志、metrics完善

**改动**：
1. 增加Prometheus metrics（retry_count, fix_count等）
2. 压测验证重试不会导致雪崩
3. 文档更新

**时间**：1天

**总计**：5天

---

## 5. 风险与缓解

### 5.1 风险1：重试雪崩

**风险**：多个请求同时重试，放大provider压力

**缓解**：
- 重试延时增加随机抖动（±20%）
- 单tenant的并发重试数限制（通过Redis计数）
- 重试失败立即降级，不再继续

### 5.2 风险2：循环死锁

**风险**：重试→失败→继续→重试→...无限循环

**缓解**：
- 三层限制：retry_count（3） + auto_continue_count（5） + follow_up_depth（15）
- 总计最多45次调用，远小于MaxFollowUpsPerSession（50）
- 任一限制触发后goal session进入failed状态，不再注入

### 5.3 风险3：成本爆炸

**风险**：自动重试+自动继续+自动审计+自动修正，token消耗倍增

**缓解**：
- 所有机制默认关闭，per-tenant opt-in
- 计费时标记retry/continue/audit类型，可单独限额
- 监控dashboard显示自动调用占比

---

## 6. 配置示例

### 6.1 保守配置（生产推荐）

```yaml
# 仅错误重试，其他保持现有行为
goal.enabled: true
goal.retry_on_error: true
goal.max_retry_count: 2
goal.retry_delay_seconds: 15
goal.auto_continue_on_pause: false  # 手动控制继续
goal.auto_fix_enabled: false        # 手动审计
```

### 6.2 激进配置（开发/内部）

```yaml
# 全自动模式
goal.enabled: true
goal.retry_on_error: true
goal.max_retry_count: 3
goal.retry_delay_seconds: 20
goal.auto_continue_on_pause: true
goal.max_auto_continue_count: 5
goal.auto_fix_enabled: true
goal.use_autoroute_for_audit: true
```

---

## 7. 接下来的步骤

1. **确认方案**：老板review本文档，确认采用方案A
2. **建表SQL**：准备`goal_sessions.retry_count`字段迁移
3. **编码实现**：按Phase 1-4顺序实施
4. **本地测试**：模拟LLM错误场景验证重试
5. **部署验证**：kaixuan-1部署，真实场景测试
6. **生产发布**：184生产，默认关闭，per-tenant开启

---

**待确认问题**：

1. 方案A的同步阻塞是否可接受？（延时20s期间占用goroutine）
2. 是否需要支持跨请求重试？（需要方案B或引入任务队列）
3. Phase 3（Handoff协同）的优先级？
4. 自动修正的安全性考虑？（是否需要人工审批）

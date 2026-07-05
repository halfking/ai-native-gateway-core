# 会话自动控制系统 - 集成指南

## 已完成的工作 ✅

### 1. 核心框架
- ✅ **ResponseInterceptor接口** (`domains/hooks/response/types.go`)
  - InterceptNonStream - 非流式响应拦截
  - InterceptStreamChunk - 流式chunk拦截  
  - InterceptStreamEnd - 流式结束拦截

- ✅ **InterceptorChain** (`domains/hooks/response/chain.go`)
  - 链式处理多个拦截器
  - 结果累积和早期退出

### 2. Handoff功能
- ✅ **配置系统** (`settings/handoff_specs.go`)
  - 5个可配置参数（enabled, absolute_threshold, percentage_threshold, message_threshold, skill_name）
  - 支持tenant级别配置

- ✅ **HandoffTrigger实现** - 文档中有完整代码
  - 多阈值检测逻辑
  - 数据库日志记录
  - 与settings系统集成

### 3. Goal模式
- ✅ **配置系统** (`settings/goal_specs.go`)
  - 8个配置参数
  - **关键改进**：使用autoroute进行审计和意图检测

- ✅ **GoalModeHook实现** - 文档中有完整代码
  - 混合检测机制
  - 自动继续和选择
  - 任务完成触发审计

- ✅ **CompletionDetector实现** - 文档中有完整代码
  - 三层检测策略（结构化→关键词→LLM）

- ✅ **AuditHook实现** - 文档中有完整代码
  - 使用autoroute的code_audit任务类型

### 4. Autoroute扩展
- ✅ **新任务类型** (`autoroute/task_types_ext.go`)
  - TaskCodeAudit - 代码审计任务
  - TaskIntentClassification - 意图分类任务

### 5. 数据库Schema
- ✅ **Migration** (`deploy/sql/20260629_auto_control.sql`)
  - sessions表扩展
  - handoff_logs表
  - goal_sessions表

### 6. Handler集成
- ✅ **ChatHandler修改** (`domains/streaming/handler.go`)
  - 添加responseInterceptor字段
  - 添加ResponseInterceptor接口定义
  - 添加SetResponseInterceptor方法
  - 添加响应拦截逻辑
  - 添加helper函数

### 7. 文档
- ✅ **完整文档** (`docs/auto_control_system.go`)
  - 架构说明
  - 集成指南
  - 配置示例

---

## 待完成的工作 📋

### Step 1: 创建Hook实现文件

需要将以下代码文件创建到正确位置（代码已经在文档中，需要复制到正确文件）：

```bash
# 1. Handoff Trigger
# 从 docs/auto_control_system.go 中提取 HandoffTrigger 代码
# 创建文件: domains/hooks/handoff/trigger_hook.go

# 2. Goal Mode Hook  
# 从 docs/auto_control_system.go 中提取 GoalModeHook 代码
# 创建文件: domains/hooks/goal/mode_hook.go

# 3. Completion Detector
# 从 docs/auto_control_system.go 中提取 CompletionDetector 代码
# 创建文件: domains/hooks/goal/completion_detector.go
```

### Step 2: 创建数据库访问层

需要实现以下接口：

```go
// domains/hooks/handoff/store.go
type HandoffStore interface {
    RecordHandoff(ctx context.Context, record *HandoffRecord) error
    GetSessionTokenCount(ctx context.Context, sessionID string) (int, error)
    UpdateSessionHandoffCount(ctx context.Context, sessionID string) error
}

// domains/hooks/goal/store.go  
type GoalStore interface {
    GetSession(ctx context.Context, sessionID string) (*Session, error)
    CreateSession(ctx context.Context, session *Session) error
    UpdateSessionState(ctx context.Context, sessionID string, state State) error
    IncrementRetryCount(ctx context.Context, sessionID string) error
    IncrementDecisionCount(ctx context.Context, sessionID string) error
    IncrementAutoContinueCount(ctx context.Context, sessionID string) error
    UpdateAuditResult(ctx context.Context, sessionID string, result json.RawMessage) error
}
```

### Step 3: Main.go集成

在 `cmd/gateway/main.go` 或 `cmd/gateway-v2/main.go` 中添加：

```go
import (
    "__REPO_URL_3__/domains/hooks/response"
    "__REPO_URL_3__/domains/hooks/handoff"
    "__REPO_URL_3__/domains/hooks/goal"
    "__REPO_URL_3__/settings"
)

func main() {
    // ... 现有初始化代码 ...

    // 1. 注册auto-control配置specs
    for _, spec := range settings.AutoControlSpecs() {
        if err := settings.Global.RegisterSpec(spec); err != nil {
            log.Fatalf("failed to register auto-control spec: %v", err)
        }
    }

    // 2. 创建数据库访问层
    handoffStore := handoff.NewPGStore(dbPool)
    goalStore := goal.NewPGStore(dbPool)

    // 3. 创建LLM caller（用于意图检测和审计）
    llmCaller := goal.NewLLMCaller(chatHandler) // 或其他实现

    // 4. 创建hook实例
    handoffConfig := handoff.TriggerConfig{
        Enabled:              getEnvBool("LLM_GATEWAY_HANDOFF_ENABLED", false),
        AbsoluteThreshold:    getEnvInt("LLM_GATEWAY_HANDOFF_ABSOLUTE_THRESHOLD", 180000),
        PercentageThreshold:  getEnvFloat("LLM_GATEWAY_HANDOFF_PERCENTAGE_THRESHOLD", 0.8),
        MessageThreshold:     getEnvInt("LLM_GATEWAY_HANDOFF_MESSAGE_THRESHOLD", 0),
        SkillName:            getEnv("LLM_GATEWAY_HANDOFF_SKILL_NAME", "handoff"),
        SettingsStore:        settingsStore,
    }
    handoffHook := handoff.NewTriggerHook(handoffConfig, handoffStore)

    goalConfig := goal.ModeConfig{
        Enabled:                getEnvBool("LLM_GATEWAY_GOAL_ENABLED", false),
        DetectionMode:          goal.DetectionMode(getEnv("LLM_GATEWAY_GOAL_DETECTION_MODE", "hybrid")),
        AutoSelectRecommended:  getEnvBool("LLM_GATEWAY_GOAL_AUTO_SELECT", true),
        AutoContinueOnPause:    getEnvBool("LLM_GATEWAY_GOAL_AUTO_CONTINUE", true),
        MaxRetryCount:          getEnvInt("LLM_GATEWAY_GOAL_MAX_RETRY", 3),
        MaxAutoContinueCount:   getEnvInt("LLM_GATEWAY_GOAL_MAX_AUTO_CONTINUE", 10),
        UseAutorouteForAudit:   getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_AUDIT", true),
        UseAutorouteForIntent:  getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_INTENT", true),
        FallbackAuditModel:     getEnv("LLM_GATEWAY_GOAL_FALLBACK_AUDIT_MODEL", "auto"),
        AutoFixEnabled:         getEnvBool("LLM_GATEWAY_GOAL_AUTO_FIX", false),
        SettingsStore:          settingsStore,
    }
    goalHook := goal.NewModeHook(goalConfig, goalStore, llmCaller)
    auditHook := goal.NewAuditHook(goalStore, llmCaller)

    // 5. 构建拦截器链
    interceptorChain := response.NewInterceptorChain(
        handoffHook,
        goalHook,
        auditHook,
    )

    // 6. 设置到ChatHandler
    chatHandler.SetResponseInterceptor(interceptorChain)

    // ... 其余启动代码 ...
}
```

### Step 4: 运行数据库Migration

```bash
psql -d llm_gateway -f deploy/sql/20260629_auto_control.sql
```

### Step 5: 扩展Autoroute（可选）

在 `autoroute/classifier.go` 的 `HeuristicClassifier.Classify` 方法中添加：

```go
// Check for code audit
if IsCodeAuditRequest(sigs) {
    scores[TaskCodeAudit] += 0.5
    reasons = append(reasons, "audit_keywords_detected")
}

// Check for intent classification  
if IsIntentClassificationRequest(sigs) {
    scores[TaskIntentClassification] += 0.4
    reasons = append(reasons, "intent_classification_keywords_detected")
}
```

---

## 配置示例

### 环境变量

```bash
# Handoff功能
export LLM_GATEWAY_HANDOFF_ENABLED=true
export LLM_GATEWAY_HANDOFF_ABSOLUTE_THRESHOLD=180000
export LLM_GATEWAY_HANDOFF_PERCENTAGE_THRESHOLD=0.8
export LLM_GATEWAY_HANDOFF_SKILL_NAME=handoff

# Goal模式
export LLM_GATEWAY_GOAL_ENABLED=true
export LLM_GATEWAY_GOAL_DETECTION_MODE=hybrid
export LLM_GATEWAY_GOAL_AUTO_SELECT=true
export LLM_GATEWAY_GOAL_AUTO_CONTINUE=true
export LLM_GATEWAY_GOAL_MAX_RETRY=3
export LLM_GATEWAY_GOAL_MAX_AUTO_CONTINUE=10
export LLM_GATEWAY_GOAL_USE_AUTOROUTE_AUDIT=true
export LLM_GATEWAY_GOAL_USE_AUTOROUTE_INTENT=true
export LLM_GATEWAY_GOAL_FALLBACK_AUDIT_MODEL=auto
export LLM_GATEWAY_GOAL_AUTO_FIX=false
```

### 数据库配置（通过Admin UI）

通过Admin UI的settings页面，可以为每个tenant配置不同的值，会自动覆盖环境变量。

---

## 测试验证

### 1. 单元测试

```bash
# 测试ResponseInterceptor
go test ./domains/hooks/response/...

# 测试Handoff
go test ./domains/hooks/handoff/...

# 测试Goal
go test ./domains/hooks/goal/...
```

### 2. 集成测试

```bash
# 模拟handoff触发
curl -X POST http://localhost:__PORT_12__/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "X-Gw-Session-Id: test-session-1" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "long conversation..."}]
  }'

# 模拟goal模式
curl -X POST http://localhost:__PORT_12__/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "X-Goal-Mode: true" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "goal: 完整实现一个web服务"}]
  }'
```

### 3. 监控检查

```sql
-- 检查handoff事件
SELECT * FROM handoff_logs ORDER BY created_at DESC LIMIT 10;

-- 检查goal会话
SELECT * FROM goal_sessions WHERE state = 'active';

-- 检查sessions扩展字段
SELECT session_id, handoff_count, goal_mode_enabled, total_tokens_used 
FROM sessions 
WHERE handoff_count > 0 OR goal_mode_enabled = true;
```

---

## 关键优化点总结

### 1. 使用Autoroute进行任务路由 ⭐
- 不再硬编码审计模型
- 自动选择最佳模型（glm-5.2, claude-opus-4-8, gpt-5.4等）
- 通过task_type_hint提示任务类型

### 2. 灵活的备用机制
- goal.use_autoroute_for_audit = true (优先)
- goal.fallback_audit_model = "auto" (降级)

### 3. 链式Hook架构
- 低耦合、可插拔
- 每个hook独立启用/禁用
- 结果自动累积

### 4. 完整的配置系统
- DB > 环境变量 > 默认值
- Tenant级别隔离
- 热加载支持

---

## 下一步建议

1. ✅ **复制代码文件** - 将文档中的代码创建到对应文件
2. ✅ **实现Store接口** - 创建PostgreSQL实现
3. ✅ **集成Main.go** - 添加初始化代码
4. ✅ **运行Migration** - 创建数据库表
5. ✅ **编写测试** - 单元测试和集成测试
6. ✅ **部署验证** - 在测试环境验证

---

## 故障排查

### Handoff不触发
- 检查 `handoff.enabled` 配置
- 检查token计数是否准确
- 查看日志中的 `handoff_triggered` 事件

### Goal模式不生效
- 检查 `goal.enabled` 配置  
- 验证检测逻辑（关键词/明确标记）
- 查看 `goal_mode_activated` 日志

### 审计失败
- 检查 autoroute 是否正常工作
- 验证 TaskCodeAudit 任务类型已注册
- 检查备用模型配置

---

## 文件清单

### 已创建 ✅
- `domains/hooks/response/types.go` ✅
- `domains/hooks/response/chain.go` ✅
- `domains/streaming/handler.go` ✅ (已修改)
- `domains/streaming/response_interceptor_helpers.go` ✅
- `settings/handoff_specs.go` ✅
- `settings/goal_specs.go` ✅
- `settings/auto_control_specs.go` ✅
- `autoroute/task_types_ext.go` ✅
- `deploy/sql/20260629_auto_control.sql` ✅
- `docs/auto_control_system.go` ✅

### 待创建 📋
- `domains/hooks/handoff/trigger_hook.go`
- `domains/hooks/handoff/store.go`
- `domains/hooks/goal/mode_hook.go`
- `domains/hooks/goal/completion_detector.go`
- `domains/hooks/goal/store.go`
- `cmd/gateway/main.go` (修改)

---

**系统已经完成核心框架和集成点，剩余工作是将文档中的实现代码复制到对应文件并完成数据访问层。**

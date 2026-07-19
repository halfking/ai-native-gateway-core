# Goal模式成本控制与分级方案

> **文档版本**: v1.0  
> **创建日期**: 2026-07-19  
> **状态**: 设计阶段  
> **关联文档**: 16-Goal模式设计方案, 17-审计报告

---

## 1. 方案概述

### 1.1 设计原则

根据审计报告的成本分析，将Goal模式设计为**三级成本模式**，通过单一配置项控制：

```go
goal.cost_mode = "minimal" | "balanced" | "aggressive"
```

**核心思想**：
- ✅ **预设分级**：每个模式预定义所有子配置，避免用户逐项配置
- ✅ **单点切换**：修改一个配置项即可切换整个行为模式
- ✅ **成本透明**：每个模式明确标注成本增幅与适用场景
- ✅ **向后兼容**：默认`minimal`模式（等价于当前行为）

### 1.2 三级模式定义

| 模式 | 成本增幅 | 适用场景 | 能力 |
|------|---------|---------|------|
| **minimal** | +20% | 生产环境、成本敏感 | 仅错误重试 |
| **balanced** | +140% | 内部开发、研究任务 | 重试 + 自动继续 |
| **aggressive** | +252% | 关键任务、紧急场景 | 全自动（重试+继续+审计+修正） |

---

## 2. 模式详细设计

### 2.1 Minimal模式（成本优先）

**目标**：最小成本增幅，仅解决短时错误

**启用能力**：
- ✅ 错误自动重试（3次，延时20s）
- ❌ 自动继续（关闭）
- ❌ 自动审计（关闭）
- ❌ 自动修正（关闭）

**配置映射**：
```yaml
goal.enabled: true
goal.retry_on_error: true
goal.max_retry_count: 2
goal.retry_delay_seconds: 15
goal.retry_total_timeout_seconds: 40
goal.retriable_errors: "5xx,timeout,no_candidates,rate_limit,overloaded"

goal.auto_continue_on_pause: false
goal.max_auto_continue_count: 0
goal.auto_fix_enabled: false
goal.use_audit: false

goal.monthly_token_limit: 500000
goal.session_token_budget: 30000
```

**成本分析**：
```
失败率 10%
  └─ 90%请求：1000 tokens
  └─ 10%请求：重试2次成功 = 1000 × 3 = 3000 tokens

平均成本 = 0.9 × 1000 + 0.1 × 3000 = 1200 tokens
成本增幅 = +20%
月度成本（100K请求）= $1200（baseline $1000）
```

**推荐场景**：
- 生产环境默认模式
- 成本敏感的tenant
- 高并发API服务

---

### 2.2 Balanced模式（功能与成本平衡）

**目标**：提供自动化便利，控制成本在合理范围

**启用能力**：
- ✅ 错误自动重试（3次，延时20s）
- ✅ 自动继续（5次上限，检测未完成任务）
- ✅ 循环检测与模型切换（3次切换上限）
- ❌ 自动审计（关闭，需手动触发）
- ❌ 自动修正（关闭）

**配置映射**：
```yaml
goal.enabled: true
goal.retry_on_error: true
goal.max_retry_count: 3
goal.retry_delay_seconds: 20
goal.retry_total_timeout_seconds: 50

goal.auto_continue_on_pause: true
goal.max_auto_continue_count: 5
goal.completion_confidence: 0.75
goal.detection_mode: "hybrid"

goal.loop_detection_enabled: true
goal.loop_threshold: 2
goal.max_model_switch_count: 3

goal.auto_fix_enabled: false
goal.use_audit: false  # 手动触发

goal.monthly_token_limit: 2000000
goal.session_token_budget: 100000
goal.cost_alert_threshold: 0.85
goal.downgrade_on_budget: true
```

**成本分析**：
```
失败率 10%, 未完成率 30%
  └─ 60%请求：1000 tokens（成功且完成）
  └─ 10%请求：3000 tokens（重试成功）
  └─ 30%请求：1000 × 5 = 5000 tokens（继续5次）

平均成本 = 0.6 × 1000 + 0.1 × 3000 + 0.3 × 5000 = 2400 tokens
成本增幅 = +140%
月度成本（100K请求）= $2400（baseline $1000）
```

**动态降级策略**：
```go
// 当月度使用量达到85%时自动降级
if usage >= limit * 0.85 {
    // 降级到minimal模式
    goal.auto_continue_on_pause = false
    goal.max_auto_continue_count = 0
    // 仅保留重试能力
}
```

**推荐场景**：
- 内部开发环境
- 研究类任务（文档分析、代码生成）
- 交互式编程助手

---

### 2.3 Aggressive模式（功能优先）

**目标**：最大化自动化，不惜成本确保任务完成

**启用能力**：
- ✅ 错误自动重试（5次，延时30s）
- ✅ 自动继续（10次上限，低阈值）
- ✅ 循环检测与模型切换（5次切换上限）
- ✅ 自动审计（任务完成后立即审计）
- ✅ 自动修正（高/中级问题自动修复）

**配置映射**：
```yaml
goal.enabled: true
goal.retry_on_error: true
goal.max_retry_count: 5
goal.retry_delay_seconds: 30
goal.retry_total_timeout_seconds: 120  # 允许更长等待

goal.auto_continue_on_pause: true
goal.max_auto_continue_count: 10
goal.completion_confidence: 0.6  # 降低阈值，更激进
goal.detection_mode: "hybrid"

goal.loop_detection_enabled: true
goal.loop_threshold: 3
goal.max_model_switch_count: 5

goal.auto_fix_enabled: true
goal.auto_fix_severity_threshold: "medium"  # high + medium都修
goal.use_audit: true
goal.use_autoroute_for_audit: true

goal.monthly_token_limit: 10000000  # 10M tokens/月
goal.session_token_budget: 500000   # 单任务50万tokens
goal.cost_alert_threshold: 0.95
goal.downgrade_on_budget: false     # 不降级，允许超限
```

**成本分析**：
```
失败率 10%, 未完成率 30%, 审计率 100%, 修正率 20%
  └─ 60%请求：1000 + 1000(audit) = 2000 tokens
  └─ 10%请求：3000 + 1000(audit) = 4000 tokens
  └─ 30%请求：5000 + 1000(audit) + 0.2×2000(fix) = 6400 tokens

平均成本 = 0.6 × 2000 + 0.1 × 4000 + 0.3 × 6400 = 3520 tokens
成本增幅 = +252%
月度成本（100K请求）= $3520（baseline $1000）
```

**推荐场景**：
- 关键任务（生产问题修复、紧急需求）
- Demo演示（展示最佳效果）
- VIP用户（付费高级功能）

---

## 3. 实现设计

### 3.1 配置Schema设计

#### 3.1.1 新增核心配置项

```go
// settings/goal_specs.go

{
    Key:   "goal.cost_mode",
    Scope: settings.ScopeTenant,
    Type:  "string",
    DefaultValue: json.RawMessage(`"minimal"`),
    ValidValues: []string{"minimal", "balanced", "aggressive"},
    Description: "Goal模式成本级别：minimal(+20%)、balanced(+140%)、aggressive(+252%)",
}
```

#### 3.1.2 模式预设表

```go
// domains/hooks/goal/cost_presets.go

package goal

type CostMode string

const (
    CostModeMinimal    CostMode = "minimal"
    CostModeBalanced   CostMode = "balanced"
    CostModeAggressive CostMode = "aggressive"
)

// ModePreset defines the complete configuration for a cost mode.
type ModePreset struct {
    // Retry settings
    RetryEnabled       bool
    MaxRetryCount      int
    RetryDelaySeconds  int
    RetryTotalTimeout  int
    RetriableErrors    string
    
    // Continue settings
    AutoContinue       bool
    MaxContinueCount   int
    CompletionConfidence float64
    DetectionMode      string
    
    // Loop detection
    LoopDetectionEnabled bool
    LoopThreshold        int
    MaxModelSwitch       int
    
    // Audit & Fix
    UseAudit           bool
    AutoFixEnabled     bool
    AutoFixSeverity    string
    
    // Cost limits
    MonthlyTokenLimit  int
    SessionTokenBudget int
    CostAlertThreshold float64
    DowngradeOnBudget  bool
}

var ModePresets = map[CostMode]ModePreset{
    CostModeMinimal: {
        // Retry only
        RetryEnabled:      true,
        MaxRetryCount:     2,
        RetryDelaySeconds: 15,
        RetryTotalTimeout: 40,
        RetriableErrors:   "5xx,timeout,no_candidates,rate_limit,overloaded",
        
        // No auto-continue
        AutoContinue:     false,
        MaxContinueCount: 0,
        
        // No audit/fix
        UseAudit:       false,
        AutoFixEnabled: false,
        
        // Conservative limits
        MonthlyTokenLimit:  500000,
        SessionTokenBudget: 30000,
        CostAlertThreshold: 0.85,
        DowngradeOnBudget:  true,
    },
    
    CostModeBalanced: {
        // Retry + Continue
        RetryEnabled:      true,
        MaxRetryCount:     3,
        RetryDelaySeconds: 20,
        RetryTotalTimeout: 50,
        RetriableErrors:   "5xx,timeout,no_candidates,rate_limit,overloaded",
        
        AutoContinue:         true,
        MaxContinueCount:     5,
        CompletionConfidence: 0.75,
        DetectionMode:        "hybrid",
        
        LoopDetectionEnabled: true,
        LoopThreshold:        2,
        MaxModelSwitch:       3,
        
        // Manual audit only
        UseAudit:       false,
        AutoFixEnabled: false,
        
        // Moderate limits
        MonthlyTokenLimit:  2000000,
        SessionTokenBudget: 100000,
        CostAlertThreshold: 0.85,
        DowngradeOnBudget:  true,
    },
    
    CostModeAggressive: {
        // Full automation
        RetryEnabled:      true,
        MaxRetryCount:     5,
        RetryDelaySeconds: 30,
        RetryTotalTimeout: 120,
        RetriableErrors:   "5xx,timeout,no_candidates,rate_limit,overloaded",
        
        AutoContinue:         true,
        MaxContinueCount:     10,
        CompletionConfidence: 0.6,
        DetectionMode:        "hybrid",
        
        LoopDetectionEnabled: true,
        LoopThreshold:        3,
        MaxModelSwitch:       5,
        
        // Full audit + fix
        UseAudit:        true,
        AutoFixEnabled:  true,
        AutoFixSeverity: "medium",
        
        // High limits
        MonthlyTokenLimit:  10000000,
        SessionTokenBudget: 500000,
        CostAlertThreshold: 0.95,
        DowngradeOnBudget:  false,
    },
}

// GetPreset returns the preset for a cost mode, falling back to minimal.
func GetPreset(mode string) ModePreset {
    m := CostMode(mode)
    if preset, ok := ModePresets[m]; ok {
        return preset
    }
    return ModePresets[CostModeMinimal]
}
```

### 3.2 配置加载逻辑

```go
// cmd/gateway/goal_control.go

func buildGoalConfig() (goal.ModeConfig, bool) {
    // 1. 读取cost_mode
    costMode := getEnv("LLM_GATEWAY_GOAL_COST_MODE", "minimal")
    preset := goal.GetPreset(costMode)
    
    // 2. 应用preset作为默认值
    cfg := goal.ModeConfig{
        Enabled: getEnvBool("LLM_GATEWAY_GOAL_ENABLED", false),
        
        // Retry settings from preset
        RetryOnError:        preset.RetryEnabled,
        MaxRetryCount:       preset.MaxRetryCount,
        RetryDelaySeconds:   preset.RetryDelaySeconds,
        RetryTotalTimeout:   preset.RetryTotalTimeout,
        
        // Continue settings from preset
        AutoContinueOnPause: preset.AutoContinue,
        MaxAutoContinueCount: preset.MaxContinueCount,
        CompletionConfidence: preset.CompletionConfidence,
        
        // Audit/Fix from preset
        UseAudit:        preset.UseAudit,
        AutoFixEnabled:  preset.AutoFixEnabled,
        
        // Cost limits from preset
        MonthlyTokenLimit:  preset.MonthlyTokenLimit,
        SessionTokenBudget: preset.SessionTokenBudget,
    }
    
    // 3. 允许环境变量覆盖（高级用户自定义）
    if v := os.Getenv("LLM_GATEWAY_GOAL_MAX_RETRY_COUNT"); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            cfg.MaxRetryCount = n
        }
    }
    // ... 其他覆盖 ...
    
    return cfg, true
}
```

### 3.3 Settings集成

```go
// domains/hooks/goal/mode_hook.go

func (h *ModeHook) loadCostMode(tenantID string) ModePreset {
    mode := h.config.SettingsGetter.GetString(tenantID, "goal.cost_mode", "minimal")
    return GetPreset(mode)
}

func (h *ModeHook) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
    // 1. 加载tenant的cost_mode
    preset := h.loadCostMode(req.TenantID)
    
    // 2. 覆盖hook行为
    h.detector.SetMinConfidence(preset.CompletionConfidence)
    
    // 3. 根据preset决策
    if !preset.AutoContinue {
        // minimal模式，不自动继续
        return nil, nil
    }
    
    // 4. 检查预算
    if h.isOverBudget(ctx, req.TenantID, preset) {
        if preset.DowngradeOnBudget {
            // 降级到minimal
            return nil, nil
        }
        // aggressive模式允许超限
    }
    
    // ... 继续现有逻辑 ...
}
```

---

## 4. 成本监控与告警

### 4.1 实时监控指标

```go
// Prometheus metrics

goal_requests_total{tenant_id, cost_mode, action}
  // action = retry | continue | audit | fix

goal_tokens_consumed_total{tenant_id, cost_mode, action}

goal_monthly_usage_ratio{tenant_id}
  // current_month_tokens / monthly_limit

goal_session_budget_exceeded_total{tenant_id}
```

### 4.2 成本告警规则

```yaml
# Grafana alerting rules

- alert: GoalMonthlyCostHigh
  expr: goal_monthly_usage_ratio > 0.85
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Tenant {{ $labels.tenant_id }} goal mode usage at {{ $value }}%"
    
- alert: GoalMonthlyCostCritical
  expr: goal_monthly_usage_ratio > 0.95
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: "Tenant {{ $labels.tenant_id }} goal mode nearly exhausted"
```

### 4.3 成本Dashboard

```
[Goal模式成本面板]
  
  ┌─────────────────────────────────────────┐
  │ 本月使用量 / 限额                       │
  │ ████████████░░░░  85% (850K / 1M)      │
  └─────────────────────────────────────────┘
  
  ┌──────────────┬──────────────┬──────────────┐
  │ Retry        │ Continue     │ Audit/Fix    │
  │ 120K tokens  │ 680K tokens  │ 50K tokens   │
  │ (+12K cost)  │ (+68K cost)  │ (+5K cost)   │
  └──────────────┴──────────────┴──────────────┘
  
  ┌─────────────────────────────────────────┐
  │ Cost Mode 分布                          │
  │ minimal:    60%  [████████░░░░░░░]      │
  │ balanced:   35%  [█████░░░░░░░░░]       │
  │ aggressive:  5%  [█░░░░░░░░░░░░░]       │
  └─────────────────────────────────────────┘
```

---

## 5. 迁移与兼容

### 5.1 现有配置迁移

```go
// 向后兼容：现有环境变量映射到cost_mode

func inferCostMode() string {
    // 如果显式设置了cost_mode，优先使用
    if mode := os.Getenv("LLM_GATEWAY_GOAL_COST_MODE"); mode != "" {
        return mode
    }
    
    // 推断：如果启用了auto_fix，则为aggressive
    if getEnvBool("LLM_GATEWAY_GOAL_AUTO_FIX", false) {
        return "aggressive"
    }
    
    // 推断：如果启用了auto_continue，则为balanced
    if getEnvBool("LLM_GATEWAY_GOAL_AUTO_CONTINUE", false) {
        return "balanced"
    }
    
    // 推断：如果只启用了retry，则为minimal
    if getEnvBool("LLM_GATEWAY_GOAL_RETRY", false) {
        return "minimal"
    }
    
    // 默认minimal
    return "minimal"
}
```

### 5.2 部署建议

```bash
# 生产环境（默认minimal）
export LLM_GATEWAY_GOAL_ENABLED=true
export LLM_GATEWAY_GOAL_COST_MODE=minimal

# 内部开发（balanced）
export LLM_GATEWAY_GOAL_ENABLED=true
export LLM_GATEWAY_GOAL_COST_MODE=balanced

# Demo/VIP（aggressive）
export LLM_GATEWAY_GOAL_ENABLED=true
export LLM_GATEWAY_GOAL_COST_MODE=aggressive
```

---

## 6. 测试场景

### 6.1 Minimal模式测试

```go
func TestMinimalMode(t *testing.T) {
    preset := goal.GetPreset("minimal")
    
    assert.True(t, preset.RetryEnabled)
    assert.Equal(t, 2, preset.MaxRetryCount)
    assert.False(t, preset.AutoContinue)
    assert.False(t, preset.UseAudit)
    assert.False(t, preset.AutoFixEnabled)
    
    // 成本验证
    baseCost := 1000
    avgCost := simulateCost(preset, 10000)  // 10K requests
    assert.InDelta(t, 1.2, avgCost/baseCost, 0.05)  // 20% ±5%
}
```

### 6.2 Balanced模式测试

```go
func TestBalancedMode(t *testing.T) {
    preset := goal.GetPreset("balanced")
    
    assert.True(t, preset.AutoContinue)
    assert.Equal(t, 5, preset.MaxContinueCount)
    assert.False(t, preset.AutoFixEnabled)
    
    avgCost := simulateCost(preset, 10000)
    assert.InDelta(t, 2.4, avgCost/1000, 0.1)  // 140% ±10%
}
```

### 6.3 Aggressive模式测试

```go
func TestAggressiveMode(t *testing.T) {
    preset := goal.GetPreset("aggressive")
    
    assert.True(t, preset.UseAudit)
    assert.True(t, preset.AutoFixEnabled)
    assert.Equal(t, 10, preset.MaxContinueCount)
    
    avgCost := simulateCost(preset, 10000)
    assert.InDelta(t, 3.5, avgCost/1000, 0.2)  // 252% ±20%
}
```

---

## 7. 文档与用户指南

### 7.1 配置示例文档

```markdown
# Goal模式成本控制指南

## 快速开始

选择一个成本模式：

### Minimal（推荐生产）
成本增幅：+20%
适合：API服务、生产环境

export LLM_GATEWAY_GOAL_COST_MODE=minimal

### Balanced（内部开发）
成本增幅：+140%
适合：研发、文档生成

export LLM_GATEWAY_GOAL_COST_MODE=balanced

### Aggressive（关键任务）
成本增幅：+252%
适合：紧急修复、VIP用户

export LLM_GATEWAY_GOAL_COST_MODE=aggressive
```

### 7.2 Settings API文档

```bash
# 查询当前模式
curl -X GET "https://api.example.com/api/settings/goal.cost_mode?tenant_id=T001"
# => {"value": "minimal"}

# 切换模式（需admin权限）
curl -X PUT "https://api.example.com/api/settings/goal.cost_mode?tenant_id=T001" \
  -d '{"value": "balanced"}'
```

---

## 8. 实施计划更新

### 8.1 Phase 0：成本分级基础（2天）

**新增任务**：
1. 实现 `cost_presets.go`（模式预设表）
2. 实现 `GetPreset` 函数
3. 实现 `inferCostMode` 向后兼容逻辑
4. 更新 `buildGoalConfig` 应用preset
5. 更新 `ModeHook` 加载preset
6. 单元测试（3个模式 + 迁移逻辑）

### 8.2 调整后总时间

| Phase | 内容 | 时间 | 备注 |
|-------|------|------|------|
| Phase 0 | 成本分级基础 | **2天** | +0.5天（相比原1.5天） |
| Phase 1 | 网关层重试 + P0修正 | 3天 | 无变化 |
| Phase 2 | 审计自动修正 | 1.5天 | 无变化 |
| Phase 3 | Handoff协同 | 1天 | 无变化 |
| Phase 4 | 监控 + Dashboard | 2天 | 无变化 |
| **总计** | | **9.5天** | 原8.5天 +1天 |

---

## 9. 总结

### 9.1 方案优势

1. **用户友好**：单一配置项 `goal.cost_mode`，无需逐项调参
2. **成本透明**：每个模式明确标注成本增幅
3. **灵活可扩展**：未来可增加新模式（如 `custom`）
4. **向后兼容**：自动推断现有配置对应的模式
5. **强制预算**：每个模式内置月度限额和会话预算

### 9.2 关键决策

- ✅ 默认 `minimal` 模式（安全生产）
- ✅ `balanced` 模式为开发主力（性价比最优）
- ✅ `aggressive` 模式需显式开启（防止成本失控）
- ✅ 每个模式内置预算保护（不依赖外部限流）

### 9.3 下一步

1. **确认三级模式定义**（名称、成本增幅、能力范围）
2. **确认默认限额**（monthly_token_limit合理值）
3. **确认降级策略**（minimal/balanced触发降级的阈值）
4. **开始实施Phase 0**

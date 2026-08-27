# 压缩策略选择器设计文档

**版本**: v1.0  
**创建日期**: 2026-08-28  
**作者**: ZCode Agent  
**状态**: RFC (Request for Comments)  

---

## 1. 概述

### 1.1 背景

当前 llm-gateway-go 的会话压缩系统（v4 intelligent compression）采用单一固定流程：

```
Phase 1 (Strip) → Phase 2 (Thinking) → Phase 3 (LLM Summary) → Phase 4 (Rebuild)
```

这种设计的局限性：
- ❌ **无法适应不同场景**: 工具密集型会话 vs 纯文本对话需要不同策略
- ❌ **性能固定**: 所有会话都使用 LLM 摘要（2-4秒延迟）
- ❌ **无法平衡**: 质量 vs 速度 vs 成本无法选择
- ❌ **不可扩展**: 添加新策略需要修改核心流程

### 1.2 设计目标

本文档设计一个**策略选择器系统**，实现：

1. **手动选择**: 租户/管理员可配置偏好算法
2. **自动选择**: 根据会话特征动态选择最优策略
3. **可扩展**: 新策略只需实现接口即可集成
4. **向后兼容**: 现有行为作为默认策略保留

### 1.3 参考来源

本设计借鉴 OmniRoute 的多引擎架构（详见 `docs/research/omniroute-compression-comparison.md`）。

---

## 2. 核心概念

### 2.1 压缩策略 (Compression Strategy)

**定义**: 一个完整的压缩算法实现，包含触发条件、压缩逻辑、质量保证。

**当前策略**:
- `intelligent`: v4 intelligent compression（LLM 摘要）
- `disabled`: 不压缩

**新增策略**（按优先级）:
- `tool-focused`: 工具结果压缩优先（学习 OmniRoute aggressive）
- `rule-based`: 基于规则的文本压缩（学习 OmniRoute caveman）
- `hybrid`: intelligent + tool-focused 组合

### 2.2 选择模式 (Selection Mode)

| 模式 | 触发时机 | 决策者 | 优先级 |
|------|---------|--------|--------|
| **Manual** | 配置时 | 租户/管理员 | 最高 |
| **Session Hint** | 请求时 | 调用方 | 高 |
| **Auto** | 运行时 | 选择器算法 | 中 |
| **Default** | 回退 | 全局配置 | 最低 |

### 2.3 会话特征 (Session Features)

用于自动选择的特征维度：

```go
type SessionFeatures struct {
    // 结构特征
    MessageCount       int
    EstimatedTokens    int
    HasToolCalls       bool
    ToolCallCount      int
    ToolCallRatio      float64  // 工具消息占比
    
    // 内容特征
    AvgMessageLength   int
    HasCodeBlocks      bool
    CodeBlockRatio     float64  // 代码块占比
    
    // 时间特征
    IdleMinutes        int
    SessionAge         time.Duration
    
    // 性能约束
    MaxLatencyMs       int      // 从上下文获取
    MaxCostCents       float64  // 从上下文获取
}
```

---

## 3. 架构设计

### 3.1 系统架构图

```
┌─────────────────────────────────────────────────────────────┐
│                    Compression Entrypoint                    │
│                  (session_compressor.go)                     │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                     Strategy Selector                        │
│                    (selector/selector.go)                    │
│  ┌────────────────┐  ┌────────────────┐  ┌──────────────┐  │
│  │ Manual Config  │→ │ Session Hint   │→ │ Auto Select  │  │
│  └────────────────┘  └────────────────┘  └──────────────┘  │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                     Strategy Registry                        │
│                   (strategy/registry.go)                     │
│  ┌────────────┐  ┌──────────────┐  ┌────────────┐          │
│  │ intelligent│  │ tool-focused │  │ rule-based │  ...     │
│  └────────────┘  └──────────────┘  └────────────┘          │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                  Strategy Implementation                     │
│              (strategy/intelligent.go, ...)                  │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 接口定义

```go
package strategy

import (
    "context"
    "time"
)

// Strategy 定义压缩策略的核心接口
type Strategy interface {
    // Name 返回策略的唯一标识符
    Name() string
    
    // Description 返回策略的人类可读描述
    Description() string
    
    // Apply 执行压缩
    Apply(ctx context.Context, req *ApplyRequest) (*ApplyResult, error)
    
    // EstimateLatency 预估延迟（用于自动选择）
    EstimateLatency(features *SessionFeatures) time.Duration
    
    // EstimateCost 预估成本（用于自动选择）
    EstimateCost(features *SessionFeatures) float64
}

// ApplyRequest 压缩请求
type ApplyRequest struct {
    Messages         []Message
    Config           *StrategyConfig
    Features         *SessionFeatures
    ModelContextSize int  // 模型上下文窗口大小
}

// ApplyResult 压缩结果
type ApplyResult struct {
    Messages           []Message
    Compressed         bool
    Metrics            *Metrics
    ValidationWarnings []string
}

// Metrics 压缩度量
type Metrics struct {
    OriginalTokens      int
    CompressedTokens    int
    SavingsPercent      float64
    DurationMs          int64
    InformationLossRate float64  // 0-1
    FidelityScore       float64  // 0-1
}

// Selector 策略选择器接口
type Selector interface {
    // Select 选择最优策略
    Select(ctx context.Context, req *SelectRequest) (Strategy, error)
}

// SelectRequest 选择请求
type SelectRequest struct {
    // 配置层
    TenantConfig  *TenantStrategyConfig  // 租户级配置
    SessionHint   *string                // 会话级提示
    GlobalConfig  *GlobalStrategyConfig  // 全局配置
    
    // 上下文
    Features      *SessionFeatures
    Constraints   *SelectionConstraints
}

// SelectionConstraints 选择约束
type SelectionConstraints struct {
    MaxLatency    time.Duration  // 最大延迟
    MaxCost       float64        // 最大成本（美分）
    MinQuality    float64        // 最小质量要求 (0-1)
}
```

### 3.3 配置模型

```go
package config

// GlobalStrategyConfig 全局策略配置
type GlobalStrategyConfig struct {
    // 默认策略
    DefaultStrategy string  // "intelligent" | "tool-focused" | "rule-based" | "hybrid" | "disabled"
    
    // 自动选择
    AutoSelect     bool
    AutoSelector   *AutoSelectorConfig
    
    // 策略特定配置
    Strategies     map[string]*StrategyConfig
}

// TenantStrategyConfig 租户级策略配置
type TenantStrategyConfig struct {
    TenantID       string
    Strategy       *string  // nil = 使用全局配置
    StrategyConfig *StrategyConfig
}

// AutoSelectorConfig 自动选择器配置
type AutoSelectorConfig struct {
    Mode           string  // "rule-based" | "ml-based" | "adaptive"
    RuleSet        *RuleBasedSelectorConfig
    ModelPath      *string  // ML 模型路径（未来）
    AdaptiveConfig *AdaptiveConfig
}

// RuleBasedSelectorConfig 基于规则的选择器配置
type RuleBasedSelectorConfig struct {
    Rules []SelectionRule
}

// SelectionRule 选择规则
type SelectionRule struct {
    Name        string
    Condition   string  // CEL 表达式
    Strategy    string
    Priority    int     // 优先级（越高越优先）
}

// 示例规则：
// {
//   "name": "tool-heavy-sessions",
//   "condition": "features.toolCallRatio > 0.5",
//   "strategy": "tool-focused",
//   "priority": 100
// }

// StrategyConfig 策略特定配置
type StrategyConfig struct {
    // intelligent 配置
    Intelligent *IntelligentConfig
    
    // tool-focused 配置
    ToolFocused *ToolFocusedConfig
    
    // rule-based 配置
    RuleBased   *RuleBasedConfig
    
    // 质量门控
    QualityGate *QualityGateConfig
}

// QualityGateConfig 质量门控配置
type QualityGateConfig struct {
    Enabled             bool
    MinSavingsPercent   float64  // 最小节省率（默认 5%）
    MaxInflationPercent float64  // 最大膨胀率（默认 10%）
    MaxLossRate         float64  // 最大信息丢失率（默认 20%）
}
```

### 3.4 配置示例

```yaml
# Global configuration
compression:
  strategy:
    # 默认策略
    default: intelligent
    
    # 自动选择
    auto_select: true
    auto_selector:
      mode: rule-based
      rule_set:
        - name: low-latency-required
          condition: constraints.maxLatency < 500ms
          strategy: rule-based
          priority: 200
        
        - name: tool-heavy-sessions
          condition: features.toolCallRatio > 0.5
          strategy: tool-focused
          priority: 100
        
        - name: cost-sensitive
          condition: constraints.maxCost < 0.5
          strategy: rule-based
          priority: 50
        
        - name: quality-first
          condition: constraints.minQuality > 0.9
          strategy: intelligent
          priority: 10
    
    # 策略特定配置
    strategies:
      intelligent:
        max_message_count: 50
        max_token_count: 128000
        idle_minutes: 30
        quality_gate:
          enabled: true
          min_savings_percent: 5.0
          max_loss_rate: 0.15
      
      tool-focused:
        compress_file_content: true
        compress_grep_search: true
        compress_shell_output: true
        compress_json: true
        compress_error_message: true
        max_shell_output_lines: 150
        quality_gate:
          enabled: true
          min_savings_percent: 10.0
      
      rule-based:
        intensity: full  # lite | full | ultra
        compress_roles: [user, assistant]
        skip_rules: []
        quality_gate:
          enabled: true
          min_savings_percent: 8.0

# Tenant-specific override
tenants:
  - tenant_id: "tenant-123"
    compression:
      strategy:
        default: tool-focused  # 覆盖全局默认
        auto_select: false     # 禁用自动选择
```

---

## 4. 实现计划

### 4.1 Phase 1: 基础架构 (Week 1)

**目标**: 建立策略模式基础设施

**任务**:
1. ✅ 定义 `Strategy` 接口
2. ✅ 实现 `Registry` 注册表
3. ✅ 重构当前 `session_compressor.go` 为 `IntelligentStrategy`
4. ✅ 实现简单的 `ManualSelector`（只读取配置）
5. ✅ 单元测试

**交付物**:
```
domains/hooks/compression/strategy/
├── types.go           # 接口定义
├── registry.go        # 注册表
├── intelligent.go     # 现有实现重构
├── selector.go        # 选择器接口
├── manual_selector.go # 手动选择器
└── *_test.go
```

### 4.2 Phase 2: tool-focused 策略 (Week 1-2)

**目标**: 实现工具结果压缩策略（学习 OmniRoute aggressive）

**任务**:
1. ✅ 实现 `ToolFocusedStrategy`
2. ✅ 工具结果压缩算法：
   - `compressFileContent()`: 删除空行、注释、多余空格
   - `deduplicateGrepSearch()`: 去重搜索结果
   - `truncateShellOutput()`: 保留前100+后50行
   - `compressJSON()`: 移除缩进
   - `simplifyErrorMessage()`: 提取核心错误
3. ✅ 质量门控集成
4. ✅ 单元测试 + 集成测试

**交付物**:
```
domains/hooks/compression/strategy/
├── tool_focused.go
├── tool_compressor.go  # 工具压缩算法
└── tool_focused_test.go
```

### 4.3 Phase 3: 自动选择器 (Week 2)

**目标**: 实现基于规则的自动选择

**任务**:
1. ✅ 提取 `SessionFeatures`
2. ✅ 实现 `RuleBasedSelector`
3. ✅ CEL 表达式引擎集成
4. ✅ 默认规则集
5. ✅ 单元测试

**交付物**:
```
domains/hooks/compression/selector/
├── selector.go
├── rule_based_selector.go
├── features.go         # 特征提取
├── cel_evaluator.go    # CEL 表达式
└── *_test.go
```

### 4.4 Phase 4: 配置与集成 (Week 2-3)

**目标**: 配置系统集成

**任务**:
1. ✅ 扩展 `Config` 结构体
2. ✅ 租户级配置支持
3. ✅ 会话级 hint 支持（通过 HTTP header）
4. ✅ 配置热重载
5. ✅ 文档更新

**交付物**:
```
config/compression_strategy.go
docs/configuration/compression-strategy.md
```

### 4.5 Phase 5: rule-based 策略 (Week 3-4，可选)

**目标**: 实现基于规则的文本压缩（学习 OmniRoute caveman）

**任务**:
1. ✅ 实现 `RuleBasedStrategy`
2. ✅ 核心规则集（20-30 条，从 OmniRoute 移植）
3. ✅ 规则引擎
4. ✅ 性能优化（正则预编译）
5. ✅ 单元测试

**交付物**:
```
domains/hooks/compression/strategy/
├── rule_based.go
├── rules.go            # 规则定义
└── rule_engine.go
```

---

## 5. 使用示例

### 5.1 手动选择（租户配置）

```go
// 租户 A 配置：质量优先
tenantAConfig := &TenantStrategyConfig{
    TenantID: "tenant-a",
    Strategy: ptr("intelligent"),
    StrategyConfig: &StrategyConfig{
        Intelligent: &IntelligentConfig{
            MaxMessageCount: 50,
            MaxTokenCount:   128000,
        },
    },
}

// 租户 B 配置：性能优先
tenantBConfig := &TenantStrategyConfig{
    TenantID: "tenant-b",
    Strategy: ptr("tool-focused"),
    StrategyConfig: &StrategyConfig{
        ToolFocused: &ToolFocusedConfig{
            CompressFileContent: true,
            CompressShellOutput: true,
        },
    },
}
```

### 5.2 会话级 Hint

```bash
# HTTP 请求头
curl -X POST https://api.example.com/v1/chat/completions \
  -H "X-Compression-Strategy: tool-focused" \
  -H "X-Compression-Max-Latency-Ms: 500" \
  -d '...'
```

```go
// 从上下文提取
func extractSessionHint(ctx context.Context) *string {
    if strategy := ctx.Value("compression-strategy"); strategy != nil {
        s := strategy.(string)
        return &s
    }
    return nil
}
```

### 5.3 自动选择

```go
// 场景 1: 工具密集型会话 → tool-focused
features := &SessionFeatures{
    MessageCount:   30,
    EstimatedTokens: 50000,
    ToolCallCount:  15,
    ToolCallRatio:  0.5,  // 50% 是工具消息
}

selector := NewRuleBasedSelector(config)
strategy, err := selector.Select(ctx, &SelectRequest{
    Features: features,
    Constraints: &SelectionConstraints{
        MaxLatency: 1 * time.Second,
    },
})
// → 返回 tool-focused

// 场景 2: 纯文本长对话 + 质量要求高 → intelligent
features := &SessionFeatures{
    MessageCount:   80,
    EstimatedTokens: 150000,
    ToolCallCount:  0,
    ToolCallRatio:  0.0,
}

strategy, err := selector.Select(ctx, &SelectRequest{
    Features: features,
    Constraints: &SelectionConstraints{
        MinQuality: 0.9,  // 高质量要求
    },
})
// → 返回 intelligent

// 场景 3: 延迟敏感 → rule-based
features := &SessionFeatures{
    MessageCount:   20,
    EstimatedTokens: 30000,
}

strategy, err := selector.Select(ctx, &SelectRequest{
    Features: features,
    Constraints: &SelectionConstraints{
        MaxLatency: 200 * time.Millisecond,
    },
})
// → 返回 rule-based
```

### 5.4 完整流程

```go
// 在 session_compressor.go 中
func (c *SessionCompressor) Compress(ctx context.Context, session *Session) error {
    // 1. 提取特征
    features := extractFeatures(session)
    
    // 2. 选择策略
    selector := c.getSelector()
    strategy, err := selector.Select(ctx, &SelectRequest{
        TenantConfig: c.getTenantConfig(ctx),
        SessionHint:  extractSessionHint(ctx),
        GlobalConfig: c.globalConfig,
        Features:     features,
        Constraints:  extractConstraints(ctx),
    })
    if err != nil {
        return fmt.Errorf("strategy selection failed: %w", err)
    }
    
    // 3. 执行压缩
    result, err := strategy.Apply(ctx, &ApplyRequest{
        Messages:         session.Messages,
        Config:           c.getStrategyConfig(strategy.Name()),
        Features:         features,
        ModelContextSize: c.getModelContextSize(ctx),
    })
    if err != nil {
        return fmt.Errorf("compression failed: %w", err)
    }
    
    // 4. 质量门控
    if !c.qualityGate.ShouldAccept(result.Metrics) {
        return fmt.Errorf("compression rejected by quality gate")
    }
    
    // 5. 更新会话
    session.Messages = result.Messages
    session.CompressionMetrics = result.Metrics
    
    return nil
}
```

---

## 6. 测试策略

### 6.1 单元测试

**策略实现测试**:
```go
func TestIntelligentStrategy_Apply(t *testing.T) {
    strategy := NewIntelligentStrategy()
    
    result, err := strategy.Apply(ctx, &ApplyRequest{
        Messages: testMessages,
        Config:   defaultConfig,
    })
    
    require.NoError(t, err)
    assert.True(t, result.Compressed)
    assert.Greater(t, result.Metrics.SavingsPercent, 5.0)
}
```

**选择器测试**:
```go
func TestRuleBasedSelector_Select(t *testing.T) {
    tests := []struct {
        name     string
        features *SessionFeatures
        expected string
    }{
        {
            name: "tool-heavy",
            features: &SessionFeatures{
                ToolCallRatio: 0.6,
            },
            expected: "tool-focused",
        },
        {
            name: "low-latency",
            features: &SessionFeatures{
                MessageCount: 20,
            },
            expected: "rule-based",
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            selector := NewRuleBasedSelector(config)
            strategy, err := selector.Select(ctx, &SelectRequest{
                Features: tt.features,
            })
            require.NoError(t, err)
            assert.Equal(t, tt.expected, strategy.Name())
        })
    }
}
```

### 6.2 集成测试

```go
func TestCompressionFlow_End2End(t *testing.T) {
    // 准备测试会话
    session := createTestSession(t, &SessionParams{
        MessageCount:  50,
        ToolCallCount: 20,
    })
    
    // 配置
    config := &GlobalStrategyConfig{
        DefaultStrategy: "intelligent",
        AutoSelect:      true,
    }
    
    // 执行压缩
    compressor := NewSessionCompressor(config)
    err := compressor.Compress(ctx, session)
    
    require.NoError(t, err)
    assert.NotNil(t, session.CompressionMetrics)
    assert.Greater(t, session.CompressionMetrics.SavingsPercent, 5.0)
}
```

### 6.3 性能基准测试

```go
func BenchmarkStrategies(b *testing.B) {
    strategies := []struct {
        name     string
        strategy Strategy
    }{
        {"intelligent", NewIntelligentStrategy()},
        {"tool-focused", NewToolFocusedStrategy()},
        {"rule-based", NewRuleBasedStrategy()},
    }
    
    for _, s := range strategies {
        b.Run(s.name, func(b *testing.B) {
            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                _, _ = s.strategy.Apply(ctx, testRequest)
            }
        })
    }
}

// 预期结果:
// BenchmarkStrategies/intelligent-8    100   12000000 ns/op  (12ms)
// BenchmarkStrategies/tool-focused-8  1000    1500000 ns/op  (1.5ms)
// BenchmarkStrategies/rule-based-8   10000     150000 ns/op  (0.15ms)
```

---

## 7. 监控与可观测性

### 7.1 指标

```go
// Prometheus 指标
var (
    compressionDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "compression_duration_seconds",
            Help:    "Compression duration in seconds",
            Buckets: []float64{0.1, 0.5, 1, 2, 5, 10},
        },
        []string{"strategy", "tenant_id"},
    )
    
    compressionSavings = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "compression_savings_percent",
            Help:    "Compression savings percentage",
            Buckets: []float64{0, 5, 10, 20, 30, 50, 70, 90},
        },
        []string{"strategy", "tenant_id"},
    )
    
    strategySelection = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "compression_strategy_selections_total",
            Help: "Number of times each strategy was selected",
        },
        []string{"strategy", "selection_mode", "tenant_id"},
    )
    
    qualityGateRejections = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "compression_quality_gate_rejections_total",
            Help: "Number of compressions rejected by quality gate",
        },
        []string{"strategy", "reason", "tenant_id"},
    )
)
```

### 7.2 日志

```go
// 结构化日志
log.Info("compression completed",
    "strategy", strategy.Name(),
    "original_tokens", result.Metrics.OriginalTokens,
    "compressed_tokens", result.Metrics.CompressedTokens,
    "savings_percent", result.Metrics.SavingsPercent,
    "duration_ms", result.Metrics.DurationMs,
    "selection_mode", selectionMode,
    "tenant_id", tenantID,
)
```

### 7.3 分布式追踪

```go
// OpenTelemetry span
ctx, span := tracer.Start(ctx, "compression.strategy.apply",
    trace.WithAttributes(
        attribute.String("strategy", strategy.Name()),
        attribute.Int("original_tokens", req.Features.EstimatedTokens),
    ),
)
defer span.End()

result, err := strategy.Apply(ctx, req)

span.SetAttributes(
    attribute.Int("compressed_tokens", result.Metrics.CompressedTokens),
    attribute.Float64("savings_percent", result.Metrics.SavingsPercent),
)
```

---

## 8. 向后兼容性

### 8.1 现有行为保留

**默认策略 = intelligent**:
```yaml
compression:
  strategy:
    default: intelligent  # 与当前行为一致
```

**配置迁移**:
```go
// 旧配置格式（继续支持）
type OldCompressionConfig struct {
    MaxMessageCount int
    MaxTokenCount   int
    IdleMinutes     int
}

// 自动转换为新格式
func migrateConfig(old *OldCompressionConfig) *GlobalStrategyConfig {
    return &GlobalStrategyConfig{
        DefaultStrategy: "intelligent",
        AutoSelect:      false,
        Strategies: map[string]*StrategyConfig{
            "intelligent": {
                Intelligent: &IntelligentConfig{
                    MaxMessageCount: old.MaxMessageCount,
                    MaxTokenCount:   old.MaxTokenCount,
                    IdleMinutes:     old.IdleMinutes,
                },
            },
        },
    }
}
```

### 8.2 渐进式迁移

**阶段 1**: 添加新接口，旧代码继续工作
```go
// 旧代码路径（保留）
if !config.UseNewStrategySystem {
    return oldCompress(session)
}

// 新代码路径
return newCompress(session)
```

**阶段 2**: 默认启用新系统，提供回退
```go
config.UseNewStrategySystem = true  // 默认
config.AllowFallbackToOld = true    // 新系统失败时回退
```

**阶段 3**: 移除旧代码（3-6 个月后）

---

## 9. 安全性考虑

### 9.1 租户隔离

```go
// 租户配置隔离
func (s *Selector) Select(ctx context.Context, req *SelectRequest) (Strategy, error) {
    tenantID := extractTenantID(ctx)
    
    // 只能访问自己的配置
    tenantConfig := s.configStore.GetTenantConfig(tenantID)
    
    // 不能访问其他租户的配置
    if req.TenantConfig != nil && req.TenantConfig.TenantID != tenantID {
        return nil, fmt.Errorf("tenant config mismatch")
    }
    
    // ...
}
```

### 9.2 配置验证

```go
// 防止恶意配置
func validateStrategyConfig(config *StrategyConfig) error {
    // 防止无限循环
    if config.Intelligent.MaxMessageCount < 0 {
        return fmt.Errorf("invalid max_message_count")
    }
    
    // 防止资源耗尽
    if config.RuleBased.Intensity == "ultra" && !config.RuleBased.TrustedTenant {
        return fmt.Errorf("ultra intensity requires trusted tenant")
    }
    
    // 防止注入攻击
    for _, rule := range config.AutoSelector.RuleSet.Rules {
        if err := validateCELExpression(rule.Condition); err != nil {
            return fmt.Errorf("invalid CEL expression: %w", err)
        }
    }
    
    return nil
}
```

### 9.3 速率限制

```go
// 防止滥用
type RateLimiter struct {
    limiter *rate.Limiter
}

func (r *RateLimiter) Allow(tenantID string) bool {
    // 每个租户每秒最多 10 次压缩
    return r.limiter.Allow()
}
```

---

## 10. 未来扩展

### 10.1 ML-Based 选择器（Phase 6）

```go
type MLBasedSelector struct {
    model *ml.Model
}

func (s *MLBasedSelector) Select(ctx context.Context, req *SelectRequest) (Strategy, error) {
    // 从历史数据训练的模型
    features := s.extractMLFeatures(req.Features)
    prediction := s.model.Predict(features)
    
    return s.registry.Get(prediction.Strategy), nil
}
```

**训练数据**:
```json
{
  "features": {
    "message_count": 50,
    "tool_call_ratio": 0.3,
    "avg_message_length": 200
  },
  "label": "intelligent",
  "metrics": {
    "savings_percent": 35.2,
    "duration_ms": 1200,
    "user_satisfaction": 0.9
  }
}
```

### 10.2 Adaptive 选择器（Phase 7）

```go
type AdaptiveSelector struct {
    // 根据实时反馈调整策略选择
    feedback *FeedbackLoop
}

func (s *AdaptiveSelector) Select(ctx context.Context, req *SelectRequest) (Strategy, error) {
    // 初始选择
    strategy := s.initialSelect(req)
    
    // 监控效果
    s.feedback.Monitor(strategy, req.Features)
    
    // 动态调整
    if s.feedback.ShouldSwitch(strategy) {
        strategy = s.feedback.SuggestAlternative()
    }
    
    return strategy, nil
}
```

### 10.3 Hybrid 策略（Phase 8）

```go
type HybridStrategy struct {
    strategies []Strategy
    combiner   Combiner
}

func (s *HybridStrategy) Apply(ctx context.Context, req *ApplyRequest) (*ApplyResult, error) {
    // 组合多个策略
    // 例如: tool-focused (Phase 1) + intelligent (Phase 2)
    
    result1, err := s.strategies[0].Apply(ctx, req)
    if err != nil {
        return nil, err
    }
    
    req2 := &ApplyRequest{
        Messages: result1.Messages,
        Config:   req.Config,
    }
    result2, err := s.strategies[1].Apply(ctx, req2)
    if err != nil {
        return nil, err
    }
    
    return s.combiner.Merge(result1, result2), nil
}
```

---

## 11. 总结

### 11.1 关键收益

1. **灵活性**: 租户/管理员可选择最适合的策略
2. **性能**: rule-based 策略比 intelligent 快 10-20 倍
3. **成本**: tool-focused 策略无 LLM 调用成本
4. **可扩展**: 新策略只需实现接口
5. **智能**: 自动选择根据会话特征优化

### 11.2 实施时间线

| Phase | 内容 | 时间 | 优先级 |
|-------|------|------|--------|
| 1 | 基础架构 | Week 1 | P0 |
| 2 | tool-focused 策略 | Week 1-2 | P0 |
| 3 | 自动选择器 | Week 2 | P1 |
| 4 | 配置集成 | Week 2-3 | P1 |
| 5 | rule-based 策略 | Week 3-4 | P2 |
| 6 | ML-Based 选择器 | Future | P3 |
| 7 | Adaptive 选择器 | Future | P3 |
| 8 | Hybrid 策略 | Future | P3 |

### 11.3 成功指标

- ✅ 策略选择准确率 > 90%
- ✅ tool-focused 策略延迟 < 500ms
- ✅ 压缩节省率 > 15%
- ✅ 质量门控拒绝率 < 5%
- ✅ 零向后兼容性问题

---

**文档状态**: RFC - 等待评审  
**下一步**: 开始 Phase 1 实施


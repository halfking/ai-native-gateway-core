# [P0] 安全检测引擎配置项未接入实际代码

**优先级**: P0 - 阻断性问题  
**标签**: bug, security, configuration, r1.14  
**分配给**: @backend-team  
**创建时间**: 2026-07-09  
**预计修复**: R1.14

---

## 问题描述

在 R1.13 中新增了安全检测引擎模块（security），包含 20 个配置项（`security.mode`、`security.llm.intent_model`、`security.threat.severity_threshold` 等），但这些配置项未被实际代码读取，仍使用硬编码值。

**影响**：
- ❌ 用户在前端（`/admin/modules`）修改配置后完全无效
- ❌ 配置项成为"空气配置"，违反"配置即文档"原则
- ❌ 无法根据业务场景调整安全策略

---

## 复现步骤

1. 访问 `/admin/modules`，选择"安全检测引擎"
2. 切换到 Config 标签，修改 `security.intent.confidence_threshold` 从 0.7 改为 0.9
3. 保存配置
4. 发送一个测试请求
5. **实际结果**：仍使用硬编码值 0.5（见 `intent_analyzer.go:11`）
6. **预期结果**：使用配置值 0.9

---

## 根本原因

### 代码分析

**domains/hooks/security/intent_analyzer.go:11**
```go
func NewIntentAnalyzer(minScore float64) *IntentAnalyzer {
    if minScore <= 0 {
        minScore = 0.5  // ❌ 硬编码，未读取 security.intent.confidence_threshold
    }
    return &IntentAnalyzer{minScore: minScore}
}
```

**domains/hooks/security/threat_detector.go:11**
```go
func NewThreatDetector(severityThreshold int) *ThreatDetector {
    if severityThreshold <= 0 {
        severityThreshold = 7  // ❌ 硬编码，未读取 security.threat.severity_threshold
    }
    return &ThreatDetector{threshold: severityThreshold}
}
```

**domains/hooks/security/hook.go:19-22**
```go
func NewSecurityHook(intent *IntentAnalyzer, threat *ThreatDetector) *SecurityHook {
    return &SecurityHook{intent: intent, threat: threat}
}
// ❌ 构造函数未注入 settings.Registry，无法读取配置
```

---

## 解决方案

### 方案 A：重构构造函数，注入 settings.Registry（推荐）

#### 1. 修改 SecurityHook 构造函数

**domains/hooks/security/hook.go**
```go
import (
    "github.com/kaixuan/llm-gateway-go/settings"
)

type SecurityHook struct {
    intent   *IntentAnalyzer
    threat   *ThreatDetector
    config   *SecurityConfig
    registry *settings.Registry
}

type SecurityConfig struct {
    Mode                   string
    IntentModel            string
    ThreatModel            string
    IntentEnabled          bool
    IntentConfidenceThresh float64
    IntentDriftThresh      float64
    ThreatEnabled          bool
    SeverityThreshold      int
    LowThreshold           int
    MediumThreshold        int
    // ... 其他配置项
}

// ✅ 新构造函数：从 settings 读取配置
func NewSecurityHook(registry *settings.Registry) *SecurityHook {
    config := loadSecurityConfig(registry)
    
    intent := NewIntentAnalyzer(config.IntentConfidenceThresh)
    threat := NewThreatDetector(config.SeverityThreshold)
    
    return &SecurityHook{
        intent:   intent,
        threat:   threat,
        config:   config,
        registry: registry,
    }
}

func loadSecurityConfig(reg *settings.Registry) *SecurityConfig {
    return &SecurityConfig{
        Mode:                   reg.String("security.mode", "observe"),
        IntentModel:            reg.String("security.llm.intent_model", "gpt-4o-mini"),
        ThreatModel:            reg.String("security.llm.threat_model", "gpt-4o-mini"),
        IntentEnabled:          reg.Bool("security.intent.enabled", true),
        IntentConfidenceThresh: reg.Float64("security.intent.confidence_threshold", 0.7),
        IntentDriftThresh:      reg.Float64("security.intent.drift_threshold", 0.5),
        ThreatEnabled:          reg.Bool("security.threat.enabled", true),
        SeverityThreshold:      reg.Int("security.threat.severity_threshold", 7),
        LowThreshold:           reg.Int("security.threat.risk_level.low_threshold", 3),
        MediumThreshold:        reg.Int("security.threat.risk_level.medium_threshold", 5),
        // ... 其他配置项
    }
}
```

#### 2. 支持热加载

```go
// ✅ 支持热加载（每次 Execute 时重新读取配置）
func (h *SecurityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
    // 热加载配置
    if h.registry != nil {
        h.config = loadSecurityConfig(h.registry)
        h.intent.minScore = h.config.IntentConfidenceThresh
        h.threat.threshold = h.config.SeverityThreshold
    }
    
    // ... 执行检测逻辑
}
```

#### 3. 修改调用方

**domains/pipeline/bootstrap.go（或其他初始化位置）**
```go
import (
    "github.com/kaixuan/llm-gateway-go/settings"
    "github.com/kaixuan/llm-gateway-go/domains/hooks/security"
)

func initSecurityHook(registry *settings.Registry) pipeline.Hook {
    return security.NewSecurityHook(registry)
}
```

---

### 方案 B：使用全局 settings.Global（不推荐）

**缺点**：
- 增加全局状态依赖
- 测试困难（需要 mock 全局变量）
- 不符合依赖注入原则

**示例**：
```go
func NewSecurityHook() *SecurityHook {
    threshold := settings.Global.Float64("security.intent.confidence_threshold", 0.7)
    intent := NewIntentAnalyzer(threshold)
    // ...
}
```

---

## 实施清单

### ✅ 必须修改的文件

- [ ] `domains/hooks/security/hook.go` - 重构构造函数
- [ ] `domains/hooks/security/intent_analyzer.go` - 移除硬编码
- [ ] `domains/hooks/security/threat_detector.go` - 移除硬编码
- [ ] `domains/pipeline/bootstrap.go` - 修改 Hook 初始化逻辑
- [ ] `domains/hooks/security/hook_test.go` - 更新测试用例

### ✅ 配置项清单（20 个）

**基础配置** (2)
- [ ] `security.mode`
- [ ] （`security.enabled` 由模块系统管理，无需代码读取）

**LLM 模型** (2)
- [ ] `security.llm.intent_model`
- [ ] `security.llm.threat_model`

**意图分析** (3)
- [ ] `security.intent.enabled`
- [ ] `security.intent.confidence_threshold`
- [ ] `security.intent.drift_threshold`

**威胁检测** (8)
- [ ] `security.threat.enabled`
- [ ] `security.threat.severity_threshold`
- [ ] `security.threat.risk_level.low_threshold`
- [ ] `security.threat.risk_level.medium_threshold`
- [ ] `security.threat.checks.prompt_inject`
- [ ] `security.threat.checks.jailbreak`
- [ ] `security.threat.checks.data_leak`
- [ ] `security.threat.checks.pii`
- [ ] `security.threat.checks.persona_override`

**响应策略** (3)
- [ ] `security.response.low_risk`
- [ ] `security.response.medium_risk`
- [ ] `security.response.high_risk`

**审计联动** (3)
- [ ] `security.audit.enabled`
- [ ] `security.audit.log_all`
- [ ] `security.audit.sampling_rate`

---

## 测试计划

### 单元测试

```go
// domains/hooks/security/hook_test.go
func TestSecurityHook_LoadsConfigFromRegistry(t *testing.T) {
    // 创建 mock registry
    reg := settings.NewMockRegistry()
    reg.Set("security.intent.confidence_threshold", 0.9)
    reg.Set("security.threat.severity_threshold", 8)
    
    // 创建 hook
    hook := NewSecurityHook(reg)
    
    // 验证配置已加载
    assert.Equal(t, 0.9, hook.intent.minScore)
    assert.Equal(t, 8, hook.threat.threshold)
}

func TestSecurityHook_HotReload(t *testing.T) {
    reg := settings.NewMockRegistry()
    reg.Set("security.intent.confidence_threshold", 0.7)
    hook := NewSecurityHook(reg)
    
    // 修改配置
    reg.Set("security.intent.confidence_threshold", 0.8)
    
    // 执行 hook（触发热加载）
    env := domain.NewRequestEnvelope(ctx, nil)
    _ = hook.Execute(ctx, env)
    
    // 验证配置已更新
    assert.Equal(t, 0.8, hook.intent.minScore)
}
```

### 集成测试

```bash
# 1. 启动本地环境
docker-compose up -d

# 2. 修改配置
curl -X PUT http://localhost:8080/api/admin/settings/security.intent.confidence_threshold \
  -d '{"value": 0.9}'

# 3. 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer xxx" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'

# 4. 检查审计日志
psql -h localhost -U postgres -d llm_gateway -c \
  "SELECT intent_confidence FROM security_audit_log ORDER BY id DESC LIMIT 1;"
# 预期：返回的 intent_confidence 应该是 0.9
```

---

## 回归风险评估

### 影响范围

- **HIGH**: SecurityHook 构造函数签名变更，所有调用方需要修改
- **MEDIUM**: 测试用例需要更新
- **LOW**: 其他模块无影响

### 风险缓解

1. ✅ 保留旧构造函数（标记为 deprecated），逐步迁移
   ```go
   // Deprecated: 使用 NewSecurityHook(registry) 替代
   func NewSecurityHookLegacy(intent *IntentAnalyzer, threat *ThreatDetector) *SecurityHook {
       return &SecurityHook{intent: intent, threat: threat}
   }
   ```

2. ✅ 添加兼容性检查
   ```go
   func (h *SecurityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
       if h.config == nil {
           // 兼容旧版本：使用默认配置
           h.config = &SecurityConfig{
               IntentConfidenceThresh: 0.7,
               SeverityThreshold: 7,
               // ...
           }
       }
       // ...
   }
   ```

---

## 验收标准

- [ ] 所有 20 个配置项能在前端修改并立即生效
- [ ] 热加载测试通过（修改配置后无需重启）
- [ ] 单元测试覆盖率 ≥ 80%
- [ ] 集成测试全部通过
- [ ] 无回归问题（现有功能不受影响）
- [ ] 文档已更新（移除"配置项未生效"的警告）

---

## 相关文档

- [安全检测引擎架构文档](/docs/modules/security-engine.md)
- [审计报告](/docs/audit/security-module-config-audit-2026-07-09.md)
- [迁移指南](/docs/migrations/r1.13-security-config.md)

---

## 备注

**为什么是 P0？**
- 配置项是用户与系统交互的核心界面
- 配置不生效严重损害用户信任
- 阻断了用户根据业务场景调整安全策略的能力

**预计工作量**：
- 开发：2-3 天
- 测试：1 天
- Code Review + 修复：1 天
- **总计**：4-5 天

**责任人**：
- 开发：@backend-team
- Review：@security-team
- QA：@qa-team

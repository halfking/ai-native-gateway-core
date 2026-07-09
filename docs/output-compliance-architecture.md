# 输出合规检测模块 - 架构与功能总结

## 概述

输出合规检测模块提供企业级 LLM 响应内容治理能力，包括 PII/毒性/偏见/幻觉/密钥/内网IP 等多维度检测，支持身份感知例外规则、会话级标签聚合、人工复核队列与反馈闭环。

**关键特性**：
- **多引擎检测**：regex / keyword / LLM-as-judge 三种模式
- **身份感知例外**：数据所有者/角色/应用级放行规则
- **会话级治理**：以 `gw_session_id` 为维度累积标签与策略
- **四层响应**：log / warn / redact / block
- **人工复核**：自动队列 + 审批/驳回工作流
- **反馈闭环**：误报/漏报上报 + 策略优化

## 架构图

```
┌─────────────────────────────────────────────────────────────────┐
│                  LLM Gateway - 输出合规检测                      │
└─────────────────────────────────────────────────────────────────┘

                          ┌─────────────┐
                          │  /v1/chat/  │
                          │ completions │
                          └──────┬──────┘
                                 │
                                 ▼
                    ┌────────────────────────┐
                    │   V2 Pipeline Entry    │
                    │ (domains/pipeline)     │
                    └──────────┬─────────────┘
                               │
          ┌────────────────────┼────────────────────┐
          │                    │                    │
          ▼                    ▼                    ▼
   ┌──────────┐        ┌─────────────┐      ┌──────────────┐
   │ Compress │        │ Prompt      │      │  Session     │
   │  Hook    │        │ Injection   │      │  Analysis    │
   └──────────┘        │  Detector   │      │    Hook      │
                       └─────────────┘      └──────────────┘
                                                     │
                       ┌─────────────────────────────┘
                       │
                       ▼
              ┌─────────────────────┐
              │ Output Compliance   │
              │      Checker        │◄───────────┐
              │ (after_completion)  │            │
              └──────────┬──────────┘            │
                         │                       │
        ┌────────────────┼────────────────┐      │
        │                │                │      │
        ▼                ▼                ▼      │
   ┌─────────┐    ┌──────────┐    ┌──────────┐ │
   │   PII   │    │ Toxicity │    │  Secrets │ │
   │ Detector│    │ Detector │    │ Detector │ │
   └─────────┘    └──────────┘    └──────────┘ │
        │                │                │      │
        │                │                │      │
        └────────────────┼────────────────┘      │
                         │                       │
                         ▼                       │
              ┌──────────────────┐               │
              │  Policy Engine   │               │
              │  - Check types   │               │
              │  - Thresholds    │               │
              │  - Actions       │               │
              └────────┬─────────┘               │
                       │                         │
                       ▼                         │
              ┌──────────────────┐               │
              │ Exception Rules  │               │
              │  - owner_user    │───────────────┘
              │  - role          │  (skip if matched)
              │  - application   │
              └────────┬─────────┘
                       │
        ┌──────────────┼──────────────┐
        │              │              │
        ▼              ▼              ▼
   ┌────────┐    ┌─────────┐    ┌────────┐
   │  Log   │    │ Redact  │    │ Block  │
   │        │    │ (mask)  │    │ (403)  │
   └───┬────┘    └────┬────┘    └───┬────┘
       │              │              │
       └──────────────┼──────────────┘
                      │
                      ▼
          ┌──────────────────────┐
          │ Audit Log + Metrics  │
          └──────────┬───────────┘
                     │
        ┌────────────┼────────────┐
        │            │            │
        ▼            ▼            ▼
   ┌────────┐  ┌─────────┐  ┌─────────┐
   │ Review │  │ Alert   │  │ Skill   │
   │ Queue  │  │ Channel │  │ Gen     │
   └────────┘  └─────────┘  └─────────┘
```

## 核心组件

### 1. Checker (`domains/outputcompliance/checker.go`)

**职责**：执行检测逻辑，应用策略，输出合规结果。

**检测引擎**：
- **PII 检测**：email / phone / ID card / credit card / bank card / JWT / password
- **Toxicity 检测**：关键词匹配 + 自定义敏感词库
- **Secrets 检测**：API key (sk-*) / JWT / 私钥模式
- **Internal IP 检测**：RFC1918 (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)
- **Bias 检测**（可选）：LLM-as-judge
- **Hallucination 检测**（可选）：LLM-as-judge

**Policy 字段**（87 个配置项）：
```go
type Policy struct {
    // 检测开关
    CheckPII, CheckToxicity, CheckBias, CheckHallucination bool
    CheckSecrets, CheckInternalIP bool
    CheckJailbreakResponse, CheckInstructionInjectionResponse bool
    
    // 引擎选择
    PIIEngine string // regex / model / hybrid
    ToxicityEngine string // keyword / model / hybrid
    LLMEngineID *int
    
    // 阈值
    PIIThreshold, ToxicityThreshold, SecretsThreshold float64
    
    // 响应动作
    ActionOnPII string // log / warn / redact / block
    ActionOnSecrets string
    ActionOnInternalIP string
    
    // 脱敏配置
    AutoRedact bool
    RedactEmail, RedactPhone, RedactIDCard, RedactCreditCard bool
    RedactBankCard, RedactJWT, RedactPassword bool
    
    // 例外规则
    ExceptionRules []ExceptionRule
    WhitelistKeywords []string
    
    // 告警与学习
    RealtimeAlertEnabled bool
    AutoReviewQueueEnabled bool
    FeedbackLoopEnabled bool
    SkillGenerationEnabled bool
}
```

**例外规则结构**：
```go
type ExceptionRule struct {
    Scope      string   // owner_user / role / application_code
    Values     []string // ["user@example.com"] / ["security_auditor"]
    CheckTypes []string // ["pii", "secret"]
    Actions    []string // ["skip_redact", "skip_block"]
    Reason     string   // "数据所有者"
}
```

### 2. Hook (`domains/hooks/outputcompliance/hook.go`)

**触发时机**：`after_completion` - LLM 响应完成后，返回客户端前

**执行流程**：
1. 从 `env.Metadata` 读取策略
2. 调用 `Checker.Check(output, policy, env.Metadata)`
3. 检测结果写回 `env.Metadata["output_compliance_result"]`
4. 如果 action=block 且 enforcement_mode=enforce，返回 403
5. 如果 action=redact，替换 `env.Response` 为脱敏后内容

### 3. Admin API Handler (`admin/output_compliance_handler.go`)

**端点列表**：

| 方法 | 路径 | 功能 |
|------|------|------|
| GET | `/api/admin/output-compliance/policy` | 获取策略（不存在返回默认值） |
| PUT | `/api/admin/output-compliance/policy` | 更新策略 |
| GET | `/api/admin/output-compliance/keywords` | 列出自定义敏感词 |
| POST | `/api/admin/output-compliance/keywords` | 添加敏感词 |
| DELETE | `/api/admin/output-compliance/keywords/:id` | 删除敏感词 |
| PUT | `/api/admin/output-compliance/keywords/:id/toggle` | 启用/禁用敏感词 |
| GET | `/api/admin/output-compliance/review-queue` | 复核队列列表 |
| PUT | `/api/admin/output-compliance/review-queue/:id/approve` | 审批通过 |
| PUT | `/api/admin/output-compliance/review-queue/:id/reject` | 驳回 |
| GET | `/api/admin/output-compliance/feedback` | 反馈列表 |
| POST | `/api/admin/output-compliance/feedback` | 提交反馈 |
| GET | `/api/admin/output-compliance/stats` | 统计（总检测数/阻断数/待复核数） |

### 4. Database Schema (`sql/migrations/startup/365_output_compliance_policy_enhance.sql`)

**表结构**：

**`output_compliance_policies`**（策略表，per-tenant）
- 87 个字段：检测开关、阈值、动作、脱敏配置、例外规则、告警通道、学习开关
- `UNIQUE(tenant_id)`

**`output_compliance_custom_keywords`**（自定义敏感词）
- `id`, `tenant_id`, `keyword`, `category`, `severity`, `action`, `enabled`
- `UNIQUE(tenant_id, keyword)`

**`output_compliance_review_queue`**（复核队列）
- `id`, `tenant_id`, `audit_id`, `issue_type`, `severity`, `status`
- `status IN ('pending', 'approved', 'rejected')`
- 索引：`(tenant_id, status, created_at DESC)`

**`output_compliance_feedback`**（反馈）
- `id`, `tenant_id`, `audit_id`, `feedback_type`, `reporter`, `comment`
- `feedback_type IN ('false_positive', 'false_negative', 'correct')`

**`output_compliance_audit`**（审计日志，原有表扩展）
- 新增：`policy_id`, `exception_matched`, `alert_sent`, `skill_suggestion`, `review_queue_id`

## 典型流程

### 场景 1：敏感数据脱敏

```
1. LLM 输出：
   "您的邮箱是 user@example.com，手机号 13800138000"

2. Checker 检测：
   - detectPII() → 命中 email + phone
   - policy.ActionOnPII = "redact"
   - policy.AutoRedact = true

3. 脱敏后：
   "您的邮箱是 [EMAIL_REDACTED]，手机号 [PHONE_REDACTED]"

4. 审计日志：
   issue_type=pii, severity=7, redacted=true, blocked=false
```

### 场景 2：密钥泄漏阻断

```
1. LLM 输出：
   "API key: sk-proj-abc123xyz"

2. Checker 检测：
   - detectSecrets() → 命中 OpenAI API key
   - policy.ActionOnSecrets = "block"
   - policy.EnforcementMode = "enforce"

3. 响应：
   HTTP 403 Forbidden
   {"error": "响应因合规策略被阻断"}

4. 审计日志：
   issue_type=secret, severity=10, blocked=true
```

### 场景 3：身份感知例外（数据所有者豁免）

```
1. 请求上下文：
   - owner_user = "alice@example.com"
   - caller (sk- key).OwnerUser = "alice@example.com"

2. LLM 输出：
   "Alice 的邮箱是 alice@example.com"

3. 策略：
   exception_rules = [
     {scope: "owner_user", values: ["alice@example.com"], 
      check_types: ["pii"], actions: ["skip_redact"]}
   ]

4. Checker 逻辑：
   - detectPII() → 命中 email
   - matchesExceptionRule(issue, env.Metadata) → true
   - issue.ExceptionMatched = true
   - 跳过脱敏，原文返回

5. 审计日志：
   exception_matched=true, exception_scope="owner_user"
```

### 场景 4：人工复核队列

```
1. 检测到疑似违规（severity ≥ 8）
2. policy.AutoReviewQueueEnabled = true
3. 自动写入 output_compliance_review_queue
   - status = 'pending'
4. 管理员通过 /review-queue API 查看
5. 审批/驳回后：
   - approved → 例外规则白名单
   - rejected → 加强策略
```

### 场景 5：反馈闭环

```
1. 用户提交反馈：
   POST /api/admin/output-compliance/feedback
   {audit_id: 123, feedback_type: "false_positive"}

2. policy.FeedbackLoopEnabled = true
3. 系统记录 → 供 skill_generation 或阈值调优使用
4. policy.SkillGenerationEnabled = true → 自动生成改写建议
```

## 依赖关系

输出合规模块声明依赖（`admin/modules.go`）：

```go
{
    Key: "output_compliance",
    Dependencies: []ModuleDependency{
        {Key: "compression", Name: "会话压缩", Required: true},
        {Key: "cache", Name: "会话缓存", Required: true},
        {Key: "prompt_injection", Name: "提示词注入检测", Required: true},
    },
}
```

**依赖理由**：
- `compression`: 压缩后的响应需要先解压才能检测
- `cache`: 缓存命中时仍需检测输出
- `prompt_injection`: 提示词注入检测在前，输出合规检测在后，共享威胁情报

## 配置示例

### 默认策略（observe 模式）

```json
{
  "tenant_id": "default",
  "enabled": true,
  "enforcement_mode": "observe",
  "pii_engine": "regex",
  "toxicity_engine": "keyword",
  "check_pii": true,
  "check_toxicity": true,
  "check_secrets": true,
  "check_internal_ip": true,
  "pii_threshold": 0.7,
  "action_on_pii": "redact",
  "action_on_toxicity": "warn",
  "action_on_secrets": "redact",
  "action_on_internal_ip": "redact",
  "auto_redact": true,
  "redact_email": true,
  "redact_phone": true,
  "redact_jwt": true,
  "redact_password": true,
  "strict_mode": false,
  "sampling_rate": 1.0,
  "retention_days": 90
}
```

### 生产策略（enforce 模式 + 例外规则）

```json
{
  "enforcement_mode": "enforce",
  "action_on_secrets": "block",
  "action_on_internal_ip": "block",
  "exception_rules": [
    {
      "scope": "owner_user",
      "values": ["alice@example.com", "bob@example.com"],
      "check_types": ["pii"],
      "actions": ["skip_redact"],
      "reason": "数据所有者可见自己 PII"
    },
    {
      "scope": "role",
      "values": ["security_auditor"],
      "check_types": ["pii", "secret"],
      "actions": ["skip_redact"],
      "reason": "安全审计员需要完整日志"
    }
  ],
  "notification_channels": [
    {"type": "webhook", "url": "https://alert.example.com/hook"},
    {"type": "lark", "webhook": "https://open.feishu.cn/..."}
  ],
  "realtime_alert_enabled": true,
  "alert_threshold_severity": 8,
  "auto_review_queue_enabled": true,
  "feedback_loop_enabled": true
}
```

## 性能与可观测性

### 性能指标

- **检测延迟**：P50 < 10ms，P99 < 50ms（regex 模式）
- **脱敏延迟**：按位置替换，O(n) 线性复杂度
- **采样率**：`sampling_rate` 控制检测频率（1.0 = 全量）

### 可观测性

**日志**：
- `slog.Info("output compliance check", "request_id", ..., "issues", len(issues), "action", ...)`
- `slog.Warn("output compliance blocked", "reason", ...)`

**指标**（待实现）：
- `output_compliance_checks_total{tenant, issue_type}`
- `output_compliance_blocks_total{tenant, action}`
- `output_compliance_latency_ms{tenant, p50/p95/p99}`

**追踪**：
- 通过 `env.Metadata["output_compliance_result"]` 传递检测结果
- 可选写入 `output_compliance_audit` 表（`policy.LogAllOutputs=true`）

## 安全考虑

1. **RLS 隔离**：所有表启用 Row-Level Security，租户间数据隔离
2. **敏感数据不落日志**：脱敏后的内容写审计表，原文不记录
3. **例外规则审计**：`exception_matched=true` 记录到审计日志，供合规审查
4. **管理员权限**：策略修改需 `super_admin` 角色（`RequireSuperAdminForWrite`）

## 扩展点

### 1. 自定义检测器

实现 `Detector` 接口：

```go
type CustomDetector struct{}

func (d *CustomDetector) Detect(text string) []ComplianceIssue {
    // 自定义检测逻辑
    return issues
}

// 注册到 Checker
checker.RegisterDetector("custom", &CustomDetector{})
```

### 2. LLM-as-judge 集成

```go
policy.BiasThreshold = 0.8
policy.LLMEngineID = 42 // 指向 prompt_injection_llm_engines.id

// Checker 内部调用 LLM 评估
verdict := callLLMJudge(output, policy.LLMEngineID)
if verdict.Score > policy.BiasThreshold {
    issues = append(issues, ComplianceIssue{Type: "bias", Severity: 8})
}
```

### 3. Skill 生成（SkillGenerationEnabled）

检测到合规问题时，自动生成改写建议：

```go
if policy.SkillGenerationEnabled && len(issues) > 0 {
    suggestion := generateSafeRewrite(output, issues)
    auditRecord.SkillSuggestion = suggestion
}
```

## 测试覆盖

### 单元测试

**`domains/outputcompliance/checker_test.go`**（97 行）：
- `TestActionForIssue`: 动作映射
- `TestShouldBlockWithActionBlock`: enforce + block 阻断
- `TestShouldBlockExceptionMatched`: 例外豁免
- `TestDetectSecrets`: 密钥检测
- `TestDetectInternalIP`: 内网 IP 检测
- `TestRedactOutputSkipsException`: 脱敏跳过例外
- `TestRedactOutputSecret`: 按位置精确脱敏

### 集成测试

**`admin/output_compliance_handler_test.go`**（206 行）：
- `TestOutputComplianceHandler_GetPolicy_Default`: 默认策略
- `TestOutputComplianceHandler_UpdatePolicy_InvalidMode`: 参数校验
- `TestOutputComplianceHandler_Keywords_CRUD`: 敏感词生命周期

**覆盖率**：核心逻辑 > 80%，管理 API > 60%

## 参考文档

- [Migration 365](../sql/migrations/startup/365_output_compliance_policy_enhance.sql)
- [Checker 实现](../domains/outputcompliance/checker.go)
- [Admin Handler](../admin/output_compliance_handler.go)
- [Hook 集成](../domains/hooks/outputcompliance/hook.go)
- [模块依赖](../admin/modules.go)

---

**最后更新**：2026-07-09  
**版本**：R1.13 - Output Compliance Enhancement

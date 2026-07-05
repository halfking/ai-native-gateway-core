# llm-gateway-go 架构重构 - 审批流程集成设计

## 概述

在原有架构重构的基础上，增加**审批流程**功能，支持在会话中插入人工审批环节。

---

## 审批流程设计

### 核心场景

1. **敏感内容检测触发审批** - 检测到敏感词、高风险操作时暂停
2. **主动审批模式** - 特定租户/用户配置为"所有请求需审批"
3. **工具调用审批** - 执行敏感工具调用前需审批
4. **金额限制审批** - 单次调用成本超过阈值需审批

### 状态机扩展

**原有 7 状态** → **扩展为 11 状态**：

```
INITIAL
  ↓
RECEIVING_FROM_CLIENT
  ↓
PENDING_TO_LLM
  ↓
[新增] PENDING_APPROVAL ← 触发审批
  ↓
[新增] APPROVAL_REQUESTED ← 等待审批中
  ↓
[新增] APPROVAL_APPROVED / APPROVAL_REJECTED ← 审批结果
  ↓
SENDING_TO_LLM (如果 APPROVED)
  ↓
RECEIVING_FROM_LLM
  ↓
PENDING_TO_CLIENT
  ↓
SENDING_TO_CLIENT
  ↓
COMPLETED / REJECTED
```

### 审批数据模型

```go
// 审批请求
type ApprovalRequest struct {
    RequestID       string                 // 请求 ID
    SessionID       string                 // 会话 ID
    TenantID        string                 // 租户 ID
    
    // 触发原因
    TriggerType     ApprovalTriggerType    // 触发类型
    TriggerReason   string                 // 触发原因描述
    RiskLevel       RiskLevel              // 风险等级: LOW/MEDIUM/HIGH/CRITICAL
    
    // 会话摘要
    SessionSummary  SessionSummary         // 会话摘要
    
    // 敏感信息
    SensitiveInfo   []SensitiveItem        // 检测到的敏感信息
    
    // 请求内容
    UserMessage     string                 // 用户消息（脱敏后）
    FullContext     []Message              // 完整上下文（可选，管理员可见）
    
    // 预估信息
    EstimatedCost   float64                // 预估成本
    EstimatedTokens int                    // 预估 token 数
    
    // 审批配置
    ApprovalConfig  ApprovalConfig         // 审批配置
    
    // 状态
    Status          ApprovalStatus         // 审批状态
    CreatedAt       time.Time              // 创建时间
    ExpiresAt       time.Time              // 过期时间
    
    // 结果
    ApprovedBy      string                 // 审批人
    ApprovedAt      time.Time              // 审批时间
    ApprovalNote    string                 // 审批备注
    Rejected        bool                   // 是否拒绝
    RejectionReason string                 // 拒绝原因
}

// 触发类型
type ApprovalTriggerType string
const (
    TriggerSensitiveContent ApprovalTriggerType = "sensitive_content"  // 敏感内容
    TriggerHighCost         ApprovalTriggerType = "high_cost"          // 高成本
    TriggerToolCall         ApprovalTriggerType = "tool_call"          // 工具调用
    TriggerPolicyMatch      ApprovalTriggerType = "policy_match"       // 策略匹配
    TriggerManualMode       ApprovalTriggerType = "manual_mode"        // 手动模式
)

// 风险等级
type RiskLevel string
const (
    RiskLow      RiskLevel = "LOW"
    RiskMedium   RiskLevel = "MEDIUM"
    RiskHigh     RiskLevel = "HIGH"
    RiskCritical RiskLevel = "CRITICAL"
)

// 敏感项
type SensitiveItem struct {
    Type        string   // 类型: PII/SECRET/FINANCIAL/MEDICAL
    Content     string   // 内容（脱敏）
    Location    string   // 位置: message[0].content
    Confidence  float64  // 置信度
}

// 会话摘要
type SessionSummary struct {
    MessageCount    int       // 消息数量
    TotalTokens     int       // 总 token 数
    Duration        string    // 会话时长
    Topics          []string  // 话题标签
    UserIntent      string    // 用户意图摘要
    LastMessages    []string  // 最近 3 条消息（脱敏）
}

// 审批配置
type ApprovalConfig struct {
    TenantID        string                  // 租户 ID
    Enabled         bool                    // 是否启用审批
    Mode            ApprovalMode            // 审批模式
    Approvers       []Approver              // 审批人列表
    Channels        []NotificationChannel   // 通知渠道
    TimeoutSeconds  int                     // 超时时间（秒）
    AutoRejectOnTimeout bool                // 超时是否自动拒绝
    
    // 触发规则
    Rules           []ApprovalRule          // 审批规则
}

// 审批模式
type ApprovalMode string
const (
    ModeDisabled    ApprovalMode = "disabled"     // 禁用
    ModeAutomatic   ApprovalMode = "automatic"    // 自动（规则触发）
    ModeManual      ApprovalMode = "manual"       // 手动（所有请求）
)

// 审批人
type Approver struct {
    UserID      string   // 用户 ID
    Name        string   // 姓名
    Email       string   // 邮箱
    Phone       string   // 手机号
    Role        string   // 角色: admin/auditor/manager
    Priority    int      // 优先级（多审批人时）
}

// 通知渠道
type NotificationChannel struct {
    Type        ChannelType              // 类型
    Config      map[string]string        // 配置
    Enabled     bool                     // 是否启用
}

// 渠道类型
type ChannelType string
const (
    ChannelFeishu   ChannelType = "feishu"   // 飞书
    ChannelWeChat   ChannelType = "wechat"   // 企业微信
    ChannelDingTalk ChannelType = "dingtalk" // 钉钉
    ChannelEmail    ChannelType = "email"    // 邮件
    ChannelWebhook  ChannelType = "webhook"  // Webhook
)

// 审批规则
type ApprovalRule struct {
    Name        string              // 规则名称
    Enabled     bool                // 是否启用
    Priority    int                 // 优先级
    Conditions  []RuleCondition     // 条件
    Action      RuleAction          // 动作
}

// 规则条件
type RuleCondition struct {
    Field       string   // 字段: message_content/token_count/cost/tool_name
    Operator    string   // 操作符: contains/gt/lt/eq/regex
    Value       string   // 值
}

// 规则动作
type RuleAction struct {
    Type        string   // 类型: require_approval/auto_approve/auto_reject
    RiskLevel   RiskLevel // 风险等级
    Reason      string   // 原因
}
```

---

## 架构组件

### 1. 审批检测器 (Approval Detector)

**职责**: 检测是否需要触发审批

```go
// domains/approval/detector.go
type ApprovalDetector struct {
    configManager  *ConfigManager
    sensitiveDetector *SensitiveDetector
    costEstimator  *CostEstimator
}

func (d *ApprovalDetector) ShouldApprove(ctx context.Context, sc *session.SessionContext) (*ApprovalDecision, error)
```

### 2. 审批管理器 (Approval Manager)

**职责**: 管理审批请求的生命周期

```go
// domains/approval/manager.go
type ApprovalManager struct {
    store      ApprovalStore      // 审批请求存储
    notifier   ApprovalNotifier   // 通知发送
    summarizer SessionSummarizer  // 会话摘要生成
}

func (m *ApprovalManager) CreateApprovalRequest(ctx context.Context, sc *session.SessionContext, decision *ApprovalDecision) (*ApprovalRequest, error)
func (m *ApprovalManager) WaitForApproval(ctx context.Context, requestID string, timeout time.Duration) (*ApprovalResult, error)
func (m *ApprovalManager) Approve(ctx context.Context, requestID string, approver string, note string) error
func (m *ApprovalManager) Reject(ctx context.Context, requestID string, approver string, reason string) error
```

### 3. 通知发送器 (Approval Notifier)

**职责**: 向审批人发送通知

```go
// domains/approval/notifier.go
type ApprovalNotifier struct {
    channels map[ChannelType]NotificationChannel
}

func (n *ApprovalNotifier) Notify(ctx context.Context, req *ApprovalRequest, config *ApprovalConfig) error
```

支持的通知渠道：
- **飞书**: 发送审批卡片消息，包含"批准"/"拒绝"按钮
- **企业微信**: 发送应用消息，包含审批链接
- **钉钉**: 发送工作通知，包含审批链接
- **邮件**: 发送审批邮件，包含批准/拒绝链接
- **Webhook**: POST 到自定义 URL

### 4. 会话摘要生成器 (Session Summarizer)

**职责**: 生成会话摘要供审批人查看

```go
// domains/approval/summarizer.go
type SessionSummarizer struct {
    llmClient LLMClient // 用于生成摘要
}

func (s *SessionSummarizer) Summarize(ctx context.Context, messages []Message) (*SessionSummary, error)
```

### 5. 敏感信息检测器 (Sensitive Detector)

**职责**: 检测敏感信息并脱敏

```go
// domains/approval/sensitive_detector.go
type SensitiveDetector struct {
    patterns map[string]*regexp.Regexp
}

func (d *SensitiveDetector) Detect(ctx context.Context, content string) ([]SensitiveItem, error)
func (d *SensitiveDetector) Redact(content string, items []SensitiveItem) string
```

检测类型：
- **PII**: 身份证、手机号、邮箱、地址
- **SECRET**: API Key、密码、Token
- **FINANCIAL**: 银行卡号、支付账号
- **MEDICAL**: 病历号、诊断信息

### 6. 审批配置管理器 (Config Manager)

**职责**: 管理租户的审批配置

```go
// domains/approval/config_manager.go
type ConfigManager struct {
    db DB
    cache Cache
}

func (m *ConfigManager) GetConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error)
func (m *ConfigManager) UpdateConfig(ctx context.Context, tenantID string, config *ApprovalConfig) error
```

### 7. 审批存储 (Approval Store)

**职责**: 持久化审批请求

```go
// domains/approval/store.go
type ApprovalStore interface {
    Create(ctx context.Context, req *ApprovalRequest) error
    Get(ctx context.Context, requestID string) (*ApprovalRequest, error)
    Update(ctx context.Context, req *ApprovalRequest) error
    List(ctx context.Context, filter ApprovalFilter) ([]*ApprovalRequest, error)
}
```

存储方案：
- **PostgreSQL**: 审批请求记录
- **Redis**: 审批状态缓存（快速查询）

---

## 集成到状态机

### 扩展状态机

```go
// domains/session/state_machine.go (扩展)

const (
    // ... 原有状态
    StatePendingApproval    SessionState = "PENDING_APPROVAL"
    StateApprovalRequested  SessionState = "APPROVAL_REQUESTED"
    StateApprovalApproved   SessionState = "APPROVAL_APPROVED"
    StateApprovalRejected   SessionState = "APPROVAL_REJECTED"
)
```

### 审批检测回调

```go
// 在 StatePendingToLLM 状态注册审批检测回调
stateMachine.RegisterCallback(session.StatePendingToLLM, func(ctx context.Context, sc *session.SessionContext) error {
    // 检测是否需要审批
    decision, err := approvalDetector.ShouldApprove(ctx, sc)
    if err != nil {
        return err
    }
    
    if !decision.RequiresApproval {
        return nil // 不需要审批，继续流程
    }
    
    // 需要审批，转换状态
    if err := stateMachine.Transition(ctx, sc, session.StatePendingApproval, decision.Reason); err != nil {
        return err
    }
    
    // 创建审批请求
    approvalReq, err := approvalManager.CreateApprovalRequest(ctx, sc, decision)
    if err != nil {
        return err
    }
    sc.SetMetadata("approval_request_id", approvalReq.RequestID)
    
    // 转换到等待审批状态
    if err := stateMachine.Transition(ctx, sc, session.StateApprovalRequested, "approval_created"); err != nil {
        return err
    }
    
    // 异步等待审批结果
    go waitForApprovalResult(ctx, sc, approvalReq.RequestID)
    
    // 返回特殊错误，告知 handler 暂停处理
    return ErrApprovalPending
})
```

### Handler 处理审批流程

```go
// ChatHandler.ServeHTTP (修改)
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... 前面的流程
    
    // 状态转换: RECEIVING_FROM_CLIENT → PENDING_TO_LLM
    if err := h.stateMachine.Transition(ctx, sc, session.StatePendingToLLM, "parsed"); err != nil {
        if errors.Is(err, approval.ErrApprovalPending) {
            // 审批中，返回 202 Accepted
            h.respondApprovalPending(w, sc)
            return
        }
        h.respondError(w, err, http.StatusInternalServerError)
        return
    }
    
    // ... 继续后续流程
}

func (h *ChatHandler) respondApprovalPending(w http.ResponseWriter, sc *session.SessionContext) {
    approvalReqID, _ := sc.GetMetadata("approval_request_id")
    
    response := map[string]any{
        "status": "approval_pending",
        "message": "Your request is pending approval",
        "approval_request_id": approvalReqID,
        "poll_url": fmt.Sprintf("/v1/approvals/%s", approvalReqID),
    }
    
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusAccepted) // 202
    json.NewEncoder(w).Encode(response)
}
```

### 审批结果处理

```go
// 审批批准后继续流程
func (m *ApprovalManager) handleApprovalApproved(ctx context.Context, sc *session.SessionContext) error {
    // 转换状态
    if err := stateMachine.Transition(ctx, sc, session.StateApprovalApproved, "approved"); err != nil {
        return err
    }
    
    // 继续原有流程: SENDING_TO_LLM
    if err := stateMachine.Transition(ctx, sc, session.StateSendingToLLM, "resume"); err != nil {
        return err
    }
    
    // 调用 LLM
    upstreamResp, err := executor.Execute(ctx, sc)
    if err != nil {
        return err
    }
    
    // ... 后续流程
}

// 审批拒绝后返回错误
func (m *ApprovalManager) handleApprovalRejected(ctx context.Context, sc *session.SessionContext, reason string) error {
    // 转换状态
    if err := stateMachine.Transition(ctx, sc, session.StateApprovalRejected, reason); err != nil {
        return err
    }
    
    // 返回拒绝响应给客户端
    return sendRejectionResponse(ctx, sc, reason)
}
```

---

## API 设计

### 1. 审批配置 API

```
# 获取审批配置
GET /api/admin/tenants/{tenant_id}/approval-config

# 更新审批配置
PUT /api/admin/tenants/{tenant_id}/approval-config
{
  "enabled": true,
  "mode": "automatic",
  "approvers": [
    {
      "user_id": "user_123",
      "name": "张三",
      "email": "zhangsan@example.com",
      "role": "admin"
    }
  ],
  "channels": [
    {
      "type": "feishu",
      "enabled": true,
      "config": {
        "app_id": "cli_xxx",
        "app_secret": "xxx",
        "webhook_url": "https://open.feishu.cn/xxx"
      }
    }
  ],
  "timeout_seconds": 3600,
  "auto_reject_on_timeout": true,
  "rules": [
    {
      "name": "高成本审批",
      "enabled": true,
      "priority": 1,
      "conditions": [
        {
          "field": "estimated_cost",
          "operator": "gt",
          "value": "10.0"
        }
      ],
      "action": {
        "type": "require_approval",
        "risk_level": "HIGH",
        "reason": "预估成本超过 $10"
      }
    }
  ]
}

# 获取审批人列表
GET /api/admin/tenants/{tenant_id}/approvers

# 添加审批人
POST /api/admin/tenants/{tenant_id}/approvers
{
  "user_id": "user_123",
  "name": "李四",
  "email": "lisi@example.com",
  "role": "auditor"
}
```

### 2. 审批请求 API

```
# 查询审批请求
GET /api/v1/approvals/{request_id}

# 批准审批请求
POST /api/v1/approvals/{request_id}/approve
{
  "approver": "user_123",
  "note": "已审核，可以继续"
}

# 拒绝审批请求
POST /api/v1/approvals/{request_id}/reject
{
  "approver": "user_123",
  "reason": "包含敏感信息，不予通过"
}

# 列出审批请求（管理员）
GET /api/admin/approvals?status=pending&tenant_id=xxx

# 审批统计
GET /api/admin/approvals/stats?tenant_id=xxx
```

### 3. 通知回调 API

```
# 飞书审批回调
POST /api/webhooks/feishu/approval-callback
{
  "request_id": "req_xxx",
  "action": "approve",
  "user_id": "ou_xxx",
  "timestamp": 1234567890
}

# 企业微信审批回调
POST /api/webhooks/wechat/approval-callback

# 钉钉审批回调
POST /api/webhooks/dingtalk/approval-callback
```

---

## 数据库设计

### approval_configs 表

```sql
CREATE TABLE approval_configs (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL UNIQUE,
    enabled BOOLEAN DEFAULT false,
    mode VARCHAR(32) NOT NULL, -- disabled/automatic/manual
    timeout_seconds INT DEFAULT 3600,
    auto_reject_on_timeout BOOLEAN DEFAULT true,
    config JSONB NOT NULL, -- 完整配置 JSON
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_approval_configs_tenant ON approval_configs(tenant_id);
```

### approval_requests 表

```sql
CREATE TABLE approval_requests (
    id SERIAL PRIMARY KEY,
    request_id VARCHAR(64) NOT NULL UNIQUE,
    session_id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    
    trigger_type VARCHAR(32) NOT NULL,
    trigger_reason TEXT,
    risk_level VARCHAR(16) NOT NULL,
    
    session_summary JSONB,
    sensitive_info JSONB,
    user_message TEXT,
    
    estimated_cost DECIMAL(10, 4),
    estimated_tokens INT,
    
    status VARCHAR(32) NOT NULL, -- pending/approved/rejected/timeout
    created_at TIMESTAMP DEFAULT NOW(),
    expires_at TIMESTAMP NOT NULL,
    
    approved_by VARCHAR(64),
    approved_at TIMESTAMP,
    approval_note TEXT,
    
    rejected BOOLEAN DEFAULT false,
    rejection_reason TEXT,
    
    metadata JSONB
);

CREATE INDEX idx_approval_requests_request_id ON approval_requests(request_id);
CREATE INDEX idx_approval_requests_session_id ON approval_requests(session_id);
CREATE INDEX idx_approval_requests_tenant_id ON approval_requests(tenant_id);
CREATE INDEX idx_approval_requests_status ON approval_requests(status);
CREATE INDEX idx_approval_requests_created_at ON approval_requests(created_at DESC);
```

### approval_approvers 表

```sql
CREATE TABLE approval_approvers (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    email VARCHAR(128),
    phone VARCHAR(32),
    role VARCHAR(32) NOT NULL,
    priority INT DEFAULT 0,
    enabled BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    
    UNIQUE(tenant_id, user_id)
);

CREATE INDEX idx_approval_approvers_tenant ON approval_approvers(tenant_id);
```

### approval_rules 表

```sql
CREATE TABLE approval_rules (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    enabled BOOLEAN DEFAULT true,
    priority INT DEFAULT 0,
    conditions JSONB NOT NULL,
    action JSONB NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_approval_rules_tenant ON approval_rules(tenant_id);
CREATE INDEX idx_approval_rules_priority ON approval_rules(priority DESC);
```

---

## 通知渠道实现

### 飞书集成

```go
// domains/approval/channels/feishu.go
type FeishuChannel struct {
    appID     string
    appSecret string
    client    *feishu.Client
}

func (c *FeishuChannel) SendApprovalNotification(ctx context.Context, req *ApprovalRequest, approvers []Approver) error {
    card := c.buildApprovalCard(req)
    
    for _, approver := range approvers {
        // 发送给审批人
        if err := c.client.SendMessage(ctx, approver.UserID, card); err != nil {
            return err
        }
    }
    
    return nil
}

func (c *FeishuChannel) buildApprovalCard(req *ApprovalRequest) *feishu.InteractiveCard {
    return &feishu.InteractiveCard{
        Header: &feishu.CardHeader{
            Title: &feishu.CardTitle{
                Content: fmt.Sprintf("审批请求 - %s", req.RiskLevel),
                Tag:     "plain_text",
            },
            Template: riskLevelColor(req.RiskLevel),
        },
        Elements: []feishu.CardElement{
            {
                Tag: "div",
                Fields: []feishu.CardField{
                    {Tag: "lark_md", Content: fmt.Sprintf("**触发原因**: %s", req.TriggerReason)},
                    {Tag: "lark_md", Content: fmt.Sprintf("**会话 ID**: %s", req.SessionID)},
                    {Tag: "lark_md", Content: fmt.Sprintf("**预估成本**: $%.2f", req.EstimatedCost)},
                },
            },
            {
                Tag: "div",
                Text: &feishu.CardText{
                    Content: fmt.Sprintf("**用户消息**:\n%s", req.UserMessage),
                    Tag:     "lark_md",
                },
            },
            {
                Tag: "hr",
            },
            {
                Tag: "action",
                Actions: []feishu.CardAction{
                    {
                        Tag:  "button",
                        Text: &feishu.CardText{Content: "批准", Tag: "plain_text"},
                        Type: "primary",
                        Value: map[string]string{
                            "action":     "approve",
                            "request_id": req.RequestID,
                        },
                    },
                    {
                        Tag:  "button",
                        Text: &feishu.CardText{Content: "拒绝", Tag: "plain_text"},
                        Type: "danger",
                        Value: map[string]string{
                            "action":     "reject",
                            "request_id": req.RequestID,
                        },
                    },
                },
            },
        },
    }
}
```

### 企业微信集成

```go
// domains/approval/channels/wechat.go
type WeChatChannel struct {
    corpID     string
    corpSecret string
    agentID    int
    client     *wechat.Client
}

func (c *WeChatChannel) SendApprovalNotification(ctx context.Context, req *ApprovalRequest, approvers []Approver) error {
    message := c.buildApprovalMessage(req)
    
    for _, approver := range approvers {
        if err := c.client.SendTextCard(ctx, approver.UserID, c.agentID, message); err != nil {
            return err
        }
    }
    
    return nil
}
```

### 钉钉集成

```go
// domains/approval/channels/dingtalk.go
type DingTalkChannel struct {
    appKey    string
    appSecret string
    client    *dingtalk.Client
}

func (c *DingTalkChannel) SendApprovalNotification(ctx context.Context, req *ApprovalRequest, approvers []Approver) error {
    message := c.buildApprovalMessage(req)
    
    for _, approver := range approvers {
        if err := c.client.SendWorkNotification(ctx, approver.UserID, message); err != nil {
            return err
        }
    }
    
    return nil
}
```

---

## 前端管理界面

### 审批配置页面

```
/admin/approval-config
- 启用/禁用审批
- 选择审批模式（自动/手动）
- 管理审批人列表
- 配置通知渠道（飞书/企业微信/钉钉）
- 设置审批规则
- 设置超时时间
```

### 审批请求列表

```
/admin/approvals
- 待审批请求列表
- 已审批历史
- 审批统计图表
- 快速批准/拒绝
```

### 审批详情页面

```
/admin/approvals/{request_id}
- 请求详情
- 会话摘要
- 敏感信息提示
- 完整上下文（可展开）
- 批准/拒绝操作
- 审批历史
```

---

## 安全考虑

1. **权限控制**
   - 只有配置的审批人可以查看审批请求
   - 管理员可以管理审批配置
   - 租户隔离

2. **数据脱敏**
   - 审批通知中的敏感信息自动脱敏
   - 完整内容需要额外权限查看

3. **审计日志**
   - 所有审批操作记录审计日志
   - 包括审批人、时间、结果、原因

4. **防滥用**
   - 审批请求有过期时间
   - 超时自动拒绝或根据配置处理
   - 限制审批请求频率

---

这是完整的审批流程设计，接下来我会重新划分任务并创建并行执行方案。

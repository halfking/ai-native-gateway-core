# llm-gateway-go Casdoor审批集成指南

**版本**: v1.0  
**日期**: 2026-11-15  
**目标**: 将Casdoor双层审批架构集成到llm-gateway-go主应用

---

## 集成步骤

### Step 1: 更新配置结构

**文件**: `internal/config/config.go`

```go
package config

type Config struct {
    // ... existing fields ...
    
    // Approval configuration
    Approval ApprovalConfig `yaml:"approval"`
}

type ApprovalConfig struct {
    Mode     string              `yaml:"mode"`      // native | casdoor | hybrid
    Native   NativeApprovalConfig `yaml:"native"`
    Casdoor  CasdoorApprovalConfig `yaml:"casdoor"`
    Router   RouterConfig         `yaml:"router"`
}

type NativeApprovalConfig struct {
    Enabled        bool `yaml:"enabled"`
    TimeoutMinutes int  `yaml:"timeout_minutes"`
}

type CasdoorApprovalConfig struct {
    Enabled        bool   `yaml:"enabled"`
    Endpoint       string `yaml:"endpoint"`
    ClientID       string `yaml:"client_id"`
    ClientSecret   string `yaml:"client_secret"`
    CallbackSecret string `yaml:"callback_secret"`
    CallbackKid    string `yaml:"callback_kid"`
    CallbackURL    string `yaml:"callback_url"`
    TaskOwner      string `yaml:"task_owner"`
    DefaultFlow    string `yaml:"default_flow"`
    TTLMinutes     int    `yaml:"ttl_minutes"`
}

type RouterConfig struct {
    HighSensitivityThreshold       float64  `yaml:"high_sensitivity_threshold"`
    RequireCasdoorKeywords         []string `yaml:"require_casdoor_keywords"`
    CrossDepartmentRequiresCasdoor bool     `yaml:"cross_department_requires_casdoor"`
}
```

---

### Step 2: 初始化审批组件

**文件**: `cmd/gateway/main.go`

在main函数中添加：

```go
package main

import (
    "github.com/kaixuan/llm-gateway-go/internal/approval"
    "github.com/kaixuan/llm-gateway-go/internal/casdoor"
    "github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

func main() {
    // ... existing initialization ...
    
    // Load config
    cfg := loadConfig()
    
    // Initialize database pool
    pool := initDBPool(cfg.Database)
    
    // Initialize existing approval manager (native L1)
    approvalManager := sessionaudit.NewApprovalManager(pool)
    
    // Initialize approval components
    approvalRouter := initApprovalSystem(cfg.Approval, pool, approvalManager)
    
    // ... rest of main ...
}

func initApprovalSystem(
    cfg ApprovalConfig,
    pool *pgxpool.Pool,
    nativeApprovalManager *sessionaudit.ApprovalManager,
) *approval.Router {
    // Initialize Casdoor client
    casdoorClient := casdoor.NewClient(casdoor.Config{
        Enabled:        cfg.Casdoor.Enabled,
        Endpoint:       cfg.Casdoor.Endpoint,
        ClientID:       cfg.Casdoor.ClientID,
        ClientSecret:   cfg.Casdoor.ClientSecret,
        CallbackSecret: cfg.Casdoor.CallbackSecret,
        CallbackKid:    cfg.Casdoor.CallbackKid,
        CallbackURL:    cfg.Casdoor.CallbackURL,
        TaskOwner:      cfg.Casdoor.TaskOwner,
        DefaultFlow:    cfg.Casdoor.DefaultFlow,
        TTLMinutes:     cfg.Casdoor.TTLMinutes,
    })
    
    // Initialize approval store
    approvalStore := approval.NewPgxApprovalStore(pool)
    
    // Initialize providers
    nativeProvider := approval.NewNativeProvider(
        nativeApprovalManager,
        cfg.Native.TimeoutMinutes,
    )
    
    casdoorProvider := approval.NewCasdoorProvider(
        casdoorClient,
        approvalStore,
    )
    
    // Initialize router
    routerConfig := approval.RouterConfig{
        HighSensitivityThreshold:       cfg.Router.HighSensitivityThreshold,
        RequireCasdoorKeywords:         cfg.Router.RequireCasdoorKeywords,
        CrossDepartmentRequiresCasdoor: cfg.Router.CrossDepartmentRequiresCasdoor,
    }
    
    router := approval.NewRouter(routerConfig, nativeProvider, casdoorProvider)
    
    slog.Info("approval system initialized",
        "mode", cfg.Mode,
        "native_enabled", cfg.Native.Enabled,
        "casdoor_enabled", casdoorClient.IsEnabled(),
    )
    
    return router
}
```

---

### Step 3: 添加Webhook处理器

**文件**: `api/webhook_handler.go` (新建或添加到现有文件)

```go
package api

import (
    "context"
    "log/slog"
    "net/http"
    
    "github.com/kaixuan/llm-gateway-go/internal/approval"
    "github.com/kaixuan/llm-gateway-go/internal/casdoor"
)

type ApprovalWebhookHandler struct {
    casdoorClient   *casdoor.Client
    casdoorProvider *approval.CasdoorProvider
    sessionManager  SessionManager // interface to resume sessions
}

func NewApprovalWebhookHandler(
    client *casdoor.Client,
    provider *approval.CasdoorProvider,
    sessionMgr SessionManager,
) *ApprovalWebhookHandler {
    return &ApprovalWebhookHandler{
        casdoorClient:   client,
        casdoorProvider: provider,
        sessionManager:  sessionMgr,
    }
}

func (h *ApprovalWebhookHandler) HandleCasdoorApprovalWebhook(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    
    // Parse and verify webhook
    event, err := h.casdoorClient.HandleWebhook(r)
    if err != nil {
        slog.Error("webhook signature verification failed", "error", err)
        http.Error(w, "signature verification failed", http.StatusUnauthorized)
        return
    }
    
    slog.Info("received casdoor webhook",
        "event_id", event.EventID,
        "task_id", event.TaskID,
        "status", event.Status,
        "external_id", event.ExternalID,
    )
    
    // Process webhook
    if err := h.casdoorProvider.HandleWebhook(ctx, event); err != nil {
        slog.Error("webhook processing failed", "error", err, "event_id", event.EventID)
        http.Error(w, "processing failed", http.StatusInternalServerError)
        return
    }
    
    // Resume session if approved
    if event.Status == "approved" {
        if err := h.resumeSession(ctx, event.ExternalID); err != nil {
            slog.Error("session resume failed", "error", err, "external_id", event.ExternalID)
            // Don't fail the webhook - approval is already recorded
        }
    }
    
    // Respond success
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    w.Write([]byte(`{"ok":true}`))
}

func (h *ApprovalWebhookHandler) resumeSession(ctx context.Context, externalID string) error {
    // Parse externalID: "sessionId:requestId"
    parts := splitExternalID(externalID)
    if len(parts) != 2 {
        return fmt.Errorf("invalid externalId format: %s", externalID)
    }
    
    sessionID, requestID := parts[0], parts[1]
    
    slog.Info("resuming session",
        "session_id", sessionID,
        "request_id", requestID,
    )
    
    // Call session manager to resume
    return h.sessionManager.ResumeSession(ctx, sessionID, requestID)
}

func splitExternalID(externalID string) []string {
    // Find last colon to handle session IDs that might contain colons
    for i := len(externalID) - 1; i >= 0; i-- {
        if externalID[i] == ':' {
            return []string{externalID[:i], externalID[i+1:]}
        }
    }
    return nil
}
```

---

### Step 4: 注册路由

**文件**: `cmd/gateway/routes.go` (或主HTTP服务器设置)

```go
package main

import (
    "net/http"
    
    "github.com/kaixuan/llm-gateway-go/api"
)

func setupRoutes(mux *http.ServeMux, handlers *Handlers) {
    // ... existing routes ...
    
    // Casdoor approval webhook
    mux.HandleFunc("/api/webhooks/casdoor/approval",
        handlers.ApprovalWebhook.HandleCasdoorApprovalWebhook,
    )
    
    // ... other routes ...
}

type Handlers struct {
    // ... existing handlers ...
    ApprovalWebhook *api.ApprovalWebhookHandler
}

func initHandlers(
    casdoorClient *casdoor.Client,
    casdoorProvider *approval.CasdoorProvider,
    sessionManager SessionManager,
) *Handlers {
    return &Handlers{
        // ... existing handlers ...
        ApprovalWebhook: api.NewApprovalWebhookHandler(
            casdoorClient,
            casdoorProvider,
            sessionManager,
        ),
    }
}
```

---

### Step 5: 修改审批创建点

**文件**: 现有审批触发逻辑所在文件（需要查找实际位置）

将原有的直接调用 `approvalManager.CreateApproval` 改为通过router路由：

```go
// Old code:
// approval, err := approvalManager.CreateApproval(ctx, ...)

// New code:
approval, err := approvalRouter.CreateApproval(ctx, &approval.CreateApprovalRequest{
    RequestID:     requestID,
    SessionID:     sessionID,
    TenantID:      tenantID,
    UserID:        userID,
    CasdoorUserID: getCasdoorUserID(user), // need to implement
    DetectResult: &approval.DetectResult{
        Triggers:         detectResult.Triggers,
        SensitivityScore: detectResult.Score,
        CrossDepartment:  detectResult.CrossDepartment,
        Summary:          detectResult.Summary,
        Metadata:         detectResult.Metadata,
    },
    ClientInfo: clientInfo,
    CreatedAt:  time.Now(),
})

if err != nil {
    return fmt.Errorf("create approval: %w", err)
}

// Log which provider was used
slog.Info("approval created",
    "request_id", approval.ID,
    "provider", approval.Provider,
    "status", approval.Status,
)
```

---

### Step 6: 实现辅助函数

**获取Casdoor用户ID**:

```go
// internal/auth/user.go or similar
func GetCasdoorUserID(user *User) string {
    // If using Casdoor auth integration (Phase 2-4 completed)
    if user.CasdoorOrg != "" && user.CasdoorUsername != "" {
        return user.CasdoorOrg + "/" + user.CasdoorUsername
    }
    
    // Fallback: use llm-gateway user ID
    return user.ID
}
```

---

### Step 7: 配置文件

**文件**: `config/config.yaml`

```yaml
# ... existing config ...

approval:
  mode: hybrid
  
  native:
    enabled: true
    timeout_minutes: 30
    
  casdoor:
    enabled: ${CASDOOR_APPROVAL_ENABLED}
    endpoint: ${CASDOOR_ENDPOINT}
    client_id: ${CASDOOR_CLIENT_ID}
    client_secret: ${CASDOOR_CLIENT_SECRET}
    callback_secret: ${CASDOOR_CALLBACK_SECRET}
    callback_kid: ${CASDOOR_CALLBACK_KID}
    callback_url: ${CASDOOR_CALLBACK_URL}
    task_owner: admin
    default_flow: "@admin/sensitive-content-approval"
    ttl_minutes: 30
    
  router:
    high_sensitivity_threshold: 0.8
    require_casdoor_keywords:
      - "财务数据"
      - "薪资信息"
      - "商业机密"
      - "客户隐私"
      - "financial_data"
      - "salary"
      - "trade_secret"
      - "customer_privacy"
    cross_department_requires_casdoor: true
```

---

### Step 8: 环境变量

**文件**: `.env` (不要提交到git)

```bash
# Casdoor Approval Integration
CASDOOR_APPROVAL_ENABLED=true
CASDOOR_ENDPOINT=https://casdoor.kx.com
CASDOOR_CLIENT_ID=llm-gateway-bff
CASDOOR_CLIENT_SECRET=your_client_secret_here
CASDOOR_CALLBACK_SECRET=your_webhook_secret_here
CASDOOR_CALLBACK_KID=llm-gateway-prod-1
CASDOOR_CALLBACK_URL=https://llm-gateway.kx.com/api/webhooks/casdoor/approval
```

---

## 部署检查清单

### 前置条件

- [ ] Casdoor中已创建 `llm-gateway-bff` 应用
- [ ] 已生成clientId、clientSecret、callbackSecret
- [ ] 已配置回调URL: `https://llm-gateway.kx.com/api/webhooks/casdoor/approval`
- [ ] 已在Casdoor中创建审批流程: `@admin/sensitive-content-approval`

### 数据库

- [ ] 运行迁移: `db/migrations/202611150001_approval_casdoor_mapping.sql`
- [ ] 验证表创建: `SELECT * FROM approval_casdoor_mapping LIMIT 1;`
- [ ] 验证索引: `\d approval_casdoor_mapping`

### 配置

- [ ] 更新 `config.yaml` 引用配置模板
- [ ] 设置所有必需的环境变量
- [ ] 验证配置加载: 启动时应有日志 "approval system initialized"

### 代码

- [ ] 配置结构更新
- [ ] 主程序初始化审批组件
- [ ] Webhook处理器实现
- [ ] 路由注册
- [ ] 审批创建点修改

### 测试

- [ ] 单元测试通过: `go test ./internal/approval/... ./internal/casdoor/...`
- [ ] 低敏感度请求 → Native审批
- [ ] 高敏感度请求 → Casdoor审批
- [ ] Webhook签名验证
- [ ] 会话恢复

### 监控

- [ ] Prometheus指标可采集
- [ ] Grafana dashboard配置
- [ ] 告警规则部署

---

## 验证步骤

### 1. 启动服务

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
go run cmd/gateway/main.go
```

查看日志输出：
```
INFO approval system initialized mode=hybrid native_enabled=true casdoor_enabled=true
```

### 2. 触发低敏感度审批

```bash
# 发送低敏感度请求
curl -X POST https://llm-gateway.kx.com/api/sessions/test-session/execute \
  -H "Content-Type: application/json" \
  -d '{"prompt":"帮我写个hello world"}'
```

预期：走Native快速审批

### 3. 触发高敏感度审批

```bash
# 发送高敏感度请求
curl -X POST https://llm-gateway.kx.com/api/sessions/test-session/execute \
  -H "Content-Type: application/json" \
  -d '{"prompt":"查询所有员工的薪资信息"}'
```

预期：创建Casdoor审批任务

### 4. 验证Casdoor任务

登录Casdoor Console:
- 访问 https://casdoor.kx.com/management/approval-tasks
- 应该看到新创建的任务
- 任务标题："敏感内容审批：..."

### 5. 审批并验证Webhook

在Casdoor中审批任务，然后检查：

```bash
# 查看webhook日志
grep "received casdoor webhook" /var/log/llm-gateway/app.log

# 查看会话恢复日志
grep "resuming session" /var/log/llm-gateway/app.log
```

### 6. 验证Prometheus指标

```bash
curl http://localhost:9090/metrics | grep llmgw_approval
```

预期输出：
```
llmgw_approval_routing_total{provider="native",sensitivity="low"} 10
llmgw_approval_routing_total{provider="casdoor",sensitivity="high"} 2
llmgw_approval_creation_total{provider="casdoor",status="success"} 2
```

---

## 故障排查

### 问题: Casdoor审批任务创建失败

**检查**:
1. 验证clientId/clientSecret正确
2. 验证endpoint可访问
3. 查看错误日志: `grep "create casdoor task" /var/log/llm-gateway/app.log`

**解决**:
```bash
# 测试Casdoor API连接
curl -u llm-gateway-bff:$CASDOOR_CLIENT_SECRET \
  https://casdoor.kx.com/api/get-application
```

### 问题: Webhook签名验证失败

**检查**:
1. 验证callbackSecret配置正确
2. 验证kid匹配
3. 检查时间同步

**解决**:
```bash
# 查看webhook日志
grep "signature verification failed" /var/log/llm-gateway/app.log

# 验证时间同步
ntpdate -q pool.ntp.org
```

### 问题: 会话未恢复

**检查**:
1. externalID格式正确
2. sessionManager可用
3. 会话状态

**解决**:
查看详细日志并手动恢复：
```bash
grep "session resume failed" /var/log/llm-gateway/app.log
```

---

## 回滚计划

如果集成出现问题需要回滚：

### 快速回滚

1. 修改配置禁用Casdoor：
```yaml
approval:
  mode: native  # 改为native
  casdoor:
    enabled: false
```

2. 重启服务：
```bash
systemctl restart llm-gateway
```

### 完全回滚

1. 恢复代码到集成前版本
2. 删除数据库表（可选）：
```sql
DROP TABLE IF EXISTS approval_casdoor_mapping;
```

---

## 性能影响评估

### 预期指标

- **L1 Native审批**: < 5秒（无变化）
- **L2 Casdoor审批创建**: < 2秒（新增开销）
- **Webhook处理**: < 1秒
- **路由决策**: < 10ms

### 资源使用

- **CPU**: +2-5%（路由逻辑）
- **内存**: +50MB（Casdoor客户端）
- **数据库连接**: 无增加（复用连接池）
- **网络**: 高敏感度请求增加1次HTTP调用

---

## 下一步

集成完成后：

1. **监控观察**（1周）
   - 路由分布是否符合预期
   - 降级事件频率
   - Webhook延迟

2. **阈值调优**
   - 根据实际数据调整 `high_sensitivity_threshold`
   - 补充 `require_casdoor_keywords`

3. **用户培训**
   - Casdoor Console使用指南
   - 审批流程说明

4. **文档完善**
   - 运维手册
   - 故障排查手册
   - API文档

---

**集成负责人**: 待指定  
**预计完成时间**: 1-2天  
**风险等级**: 低

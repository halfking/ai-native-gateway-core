# 钉钉通知渠道配置示例

## 环境变量配置

```bash
# 钉钉应用配置
export DINGTALK_APP_KEY="your_app_key"
export DINGTALK_APP_SECRET="your_app_secret"

# 审批详情页基础 URL
export APPROVAL_BASE_URL="https://your-domain.com"
```

## 使用示例

### 1. 初始化钉钉通知渠道

```go
import (
    "github.com/kaixuan/llm-gateway-go/domains/approval/channels"
    "os"
)

// 创建钉钉通知渠道
dingTalkChannel := channels.NewDingTalkChannel(
    os.Getenv("DINGTALK_APP_KEY"),
    os.Getenv("DINGTALK_APP_SECRET"),
    os.Getenv("APPROVAL_BASE_URL"),
)
```

### 2. 发送审批通知

```go
import (
    "context"
    "github.com/kaixuan/llm-gateway-go/domains/approval"
)

// 准备审批请求
req := &approval.ApprovalRequest{
    RequestID:     "req_123",
    SessionID:     "sess_456",
    TenantID:      "tenant_1",
    TriggerType:   approval.TriggerSensitiveContent,
    TriggerReason: "检测到敏感信息",
    RiskLevel:     approval.RiskHigh,
    UserMessage:   "请帮我查询用户信息",
    EstimatedCost: 0.0123,
    EstimatedTokens: 1500,
    // ... 其他字段
}

// 准备审批人列表
approvers := []approval.Approver{
    {
        UserID:  "dingtalk_user_id_1",
        Name:    "张三",
        Email:   "zhangsan@example.com",
        Role:    "admin",
        Enabled: true,
    },
    {
        UserID:  "dingtalk_user_id_2",
        Name:    "李四",
        Email:   "lisi@example.com",
        Role:    "auditor",
        Enabled: true,
    },
}

// 发送通知
ctx := context.Background()
err := dingTalkChannel.SendApprovalNotification(ctx, req, approvers)
if err != nil {
    log.Printf("发送钉钉通知失败: %v", err)
}
```

### 3. 注册钉钉回调处理

在你的主路由中注册钉钉回调处理器：

```go
import (
    "github.com/kaixuan/llm-gateway-go/api"
    "net/http"
    "os"
)

// 创建路由
mux := http.NewServeMux()

// 注册钉钉回调路由
api.RegisterDingTalkRoutes(
    mux,
    approvalManager, // 你的 ApprovalManager 实例
    os.Getenv("DINGTALK_APP_SECRET"),
)

// 启动服务器
http.ListenAndServe(":8080", mux)
```

## 钉钉回调 URL

配置钉钉应用时，设置回调 URL 为：

```
https://your-domain.com/api/webhooks/dingtalk/approval-callback
```

## 消息格式

钉钉工作通知消息使用 Markdown 格式，包含以下信息：

- 风险等级（带表情图标）
- 触发原因
- 会话信息
- 预估成本
- 用户消息
- 敏感信息检测结果
- 过期时间
- 查看详情链接

## 安全性

- Access Token 自动缓存，有效期约 2 小时
- 回调签名验证，防止伪造请求
- 时间戳验证，防止重放攻击（1小时窗口）

## 错误处理

通道会自动处理以下错误：

- Token 获取失败
- 消息发送失败
- API 错误响应
- 网络超时

所有错误都会返回详细的错误信息供调用方处理。

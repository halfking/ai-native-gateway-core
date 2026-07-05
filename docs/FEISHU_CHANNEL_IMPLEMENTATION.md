# 飞书通知渠道实现说明

## 任务完成情况

✅ 已完成任务 B1：实现飞书通知渠道

## 实现的文件

### 1. 飞书通知渠道
- **文件**: `domains/approval/channels/feishu.go`
- **功能**:
  - 飞书 API 集成（获取 access_token、发送消息）
  - 交互式卡片消息构建
  - 风险等级颜色编码（LOW=绿, MEDIUM=黄, HIGH=橙, CRITICAL=红）
  - 自动 token 缓存和过期管理
  - 消息内容脱敏和截断
  - 敏感信息提示
  - 批准/拒绝按钮（带回调 URL）

### 2. 飞书回调处理器
- **文件**: `api/webhooks/feishu_callback.go`
- **功能**:
  - POST /api/webhooks/feishu/approval-callback 端点
  - URL 验证挑战处理
  - 签名验证（支持多种签名方法）
  - 按钮点击事件处理
  - 批准/拒绝操作执行

### 3. 单元测试
- **文件**: 
  - `domains/approval/channels/feishu_test.go`
  - `api/webhooks/feishu_callback_test.go`
- **覆盖**:
  - 配置验证
  - 卡片构建
  - 颜色编码
  - 消息截断
  - 敏感信息警告
  - 回调处理
  - 签名验证
  - 错误处理

## 使用方法

### 配置飞书通道

```go
import "__REPO_URL_3__/domains/approval/channels"

// 创建飞书通道
channel, err := channels.NewFeishuChannel(channels.FeishuConfig{
    AppID:       "cli_your_app_id",
    AppSecret:   "your_app_secret",
    CallbackURL: "https://api.example.com",
})

// 测试连接
err = channel.TestConnection(context.Background())
```

### 发送审批通知

```go
// 构建审批请求
req := &approval.ApprovalRequest{
    RequestID:       "req_123",
    SessionID:       "sess_456",
    TenantID:        "tenant_789",
    TriggerType:     approval.TriggerSensitiveContent,
    TriggerReason:   "检测到敏感信息",
    RiskLevel:       approval.RiskHigh,
    UserMessage:     "用户消息内容（已脱敏）",
    EstimatedCost:   0.05,
    EstimatedTokens: 1500,
    SensitiveInfo: []approval.SensitiveItemSummary{
        {Type: "PII", Content: "***", Confidence: 0.95},
    },
    CreatedAt: time.Now(),
    ExpiresAt: time.Now().Add(1 * time.Hour),
}

// 定义审批人
approvers := []approval.Approver{
    {
        UserID:   "user_001",
        Name:     "张三",
        Email:    "zhangsan@example.com",
        Role:     "admin",
        Priority: 1,
        Enabled:  true,
    },
}

// 发送通知
err = channel.SendApprovalNotification(ctx, req, approvers)
```

### 设置回调处理器

```go
import "__REPO_URL_3__/api/webhooks"

// 创建回调处理器
handler := webhooks.NewFeishuCallbackHandler(webhooks.FeishuCallbackConfig{
    Manager:     approvalManager, // 实现 ApprovalManager 接口
    VerifyToken: "your_verify_token",
    EncryptKey:  "your_encrypt_key",
})

// 注册路由
http.HandleFunc("/api/webhooks/feishu/approval-callback", handler.HandleCallback)
```

## 卡片消息结构

飞书消息卡片包含以下部分：

1. **卡片头部**: 显示审批请求标题和风险等级（带颜色）
2. **触发信息**: 触发原因、风险等级（带 emoji）
3. **会话信息**: 会话 ID、预估成本
4. **用户消息**: 脱敏后的用户消息（超过 500 字符自动截断）
5. **敏感信息警告**: 如果检测到敏感信息，显示类型统计
6. **操作按钮**: 
   - ✅ 批准（绿色 primary 按钮）
   - ❌ 拒绝（红色 danger 按钮）
7. **页脚**: 过期时间和创建时间

## 回调流程

1. 用户在飞书中点击"批准"或"拒绝"按钮
2. 飞书向回调 URL 发送 POST 请求
3. 处理器验证签名（如果配置）
4. 提取 request_id 和 action 参数
5. 调用 ApprovalManager.Approve() 或 Reject()
6. 返回成功响应

## 安全特性

1. **签名验证**: 支持飞书签名验证，防止伪造请求
2. **Token 缓存**: 自动缓存 access_token，减少 API 调用
3. **内容脱敏**: 用户消息和敏感信息自动脱敏
4. **超时保护**: HTTP 客户端设置 10 秒超时
5. **错误处理**: 完善的错误处理和日志记录

## 配置示例

在 ApprovalConfig 中配置飞书渠道：

```go
config := &approval.ApprovalConfig{
    TenantID: "tenant_001",
    Enabled:  true,
    Mode:     approval.ModeAutomatic,
    Channels: []approval.NotificationChannel{
        {
            Type:    approval.ChannelFeishu,
            Enabled: true,
            Config: map[string]string{
                "app_id":       "cli_your_app_id",
                "app_secret":   "your_app_secret",
                "callback_url": "https://api.example.com",
            },
        },
    },
    // ... 其他配置
}
```

## 测试结果

所有飞书相关测试通过：
- ✅ 配置验证测试
- ✅ Token 获取和缓存测试
- ✅ 卡片构建测试
- ✅ 风险等级映射测试
- ✅ 消息截断测试
- ✅ 敏感信息警告测试
- ✅ 回调处理测试
- ✅ 签名验证测试

## 已知限制

1. 飞书 API 调用需要真实的 app_id 和 app_secret
2. 回调 URL 需要公网可访问
3. 审批人需要使用飞书 email 作为 open_id
4. Token 有效期通常为 2 小时，自动缓存管理

## 下一步

可以继续实现：
- 企业微信通知渠道
- 钉钉通知渠道
- 邮件通知渠道
- Webhook 通知渠道

或者集成到现有的审批管理器中。

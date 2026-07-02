# 企业微信通知渠道使用指南

## 概述

企业微信通知渠道用于在审批流程中向审批人发送通知消息。当系统检测到敏感内容、高成本请求或其他需要审批的情况时，会通过企业微信发送文本卡片消息给相关审批人。

## 配置

### 环境变量

在使用企业微信通知渠道前，需要配置以下环境变量：

```bash
# 企业微信配置
export LLM_GATEWAY_WECHAT_CORP_ID="your_corp_id"              # 企业 ID
export LLM_GATEWAY_WECHAT_CORP_SECRET="your_corp_secret"      # 应用密钥
export LLM_GATEWAY_WECHAT_AGENT_ID="1000001"                  # 应用 AgentID
export LLM_GATEWAY_WECHAT_TOKEN="your_callback_token"         # 回调验证 Token
export LLM_GATEWAY_WECHAT_AES_KEY="your_aes_key"             # 回调加密密钥（可选）
export LLM_GATEWAY_WECHAT_BASE_URL="https://your-domain.com" # 前端基础 URL
```

### YAML 配置

或者在配置文件中设置：

```yaml
wechat_corp_id: "your_corp_id"
wechat_corp_secret: "your_corp_secret"
wechat_agent_id: 1000001
wechat_token: "your_callback_token"
wechat_aes_key: "your_aes_key"
wechat_base_url: "https://your-domain.com"
```

## 使用示例

### 1. 创建通知渠道

```go
import (
    "github.com/kaixuan/llm-gateway-go/config"
    "github.com/kaixuan/llm-gateway-go/domains/approval/channels"
)

// 从配置加载
cfg := config.Load()

// 创建企业微信通知渠道
wechatChannel := channels.NewWeChatChannel(
    cfg.WeChatCorpID,
    cfg.WeChatCorpSecret,
    cfg.WeChatAgentID,
    cfg.WeChatBaseURL,
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
    RequestID:       "req-123456",
    SessionID:       "sess-789012",
    TenantID:        "tenant-001",
    TriggerReason:   "检测到敏感内容",
    RiskLevel:       approval.RiskHigh,
    EstimatedCost:   0.05,
    EstimatedTokens: 1000,
    UserMessage:     "用户的消息内容...",
}

// 定义审批人
approvers := []approval.Approver{
    {
        UserID:  "zhangsan",  // 企业微信用户 ID
        Name:    "张三",
        Email:   "zhangsan@example.com",
        Role:    "manager",
        Enabled: true,
    },
    {
        UserID:  "lisi",
        Name:    "李四",
        Email:   "lisi@example.com",
        Role:    "auditor",
        Enabled: true,
    },
}

// 发送通知
ctx := context.Background()
err := wechatChannel.SendApprovalNotification(ctx, req, approvers)
if err != nil {
    log.Printf("发送通知失败: %v", err)
}
```

## 消息格式

发送的文本卡片消息包含以下信息：

- **标题**: 审批请求 - [风险等级] （带风险等级图标）
- **内容**:
  - 触发原因
  - 会话 ID
  - 预估成本和 Token 数
  - 用户消息预览（截断到 100 字符）
- **跳转链接**: 点击可跳转到审批详情页面

### 风险等级图标

- 🔴 CRITICAL（严重）
- 🟠 HIGH（高）
- 🟡 MEDIUM（中）
- 🟢 LOW（低）

## 回调处理

### 设置回调 URL

在企业微信管理后台配置回调 URL：

```
https://your-domain.com/api/webhooks/wechat/approval-callback
```

### 回调处理器

系统已实现企业微信回调处理器 (`api/webhooks/wechat_callback.go`)，支持：

1. **URL 验证**: 处理企业微信的 URL 验证请求
2. **JSON 格式回调**: 处理自定义的审批操作回调
3. **XML 格式回调**: 处理企业微信标准事件回调

### JSON 回调格式

```json
{
  "action": "approve",           // "approve" 或 "reject"
  "approval_id": "req-123456",
  "tenant_id": "tenant-001",
  "user_id": "zhangsan",
  "reason": "审核通过"
}
```

### XML 回调格式

```xml
<xml>
  <ToUserName><![CDATA[corp_id]]></ToUserName>
  <FromUserName><![CDATA[zhangsan]]></FromUserName>
  <CreateTime>1234567890</CreateTime>
  <MsgType><![CDATA[event]]></MsgType>
  <Event><![CDATA[click]]></Event>
  <EventKey><![CDATA[approval:approve:req-123456:tenant-001]]></EventKey>
  <AgentID>1000001</AgentID>
</xml>
```

## 特性

### 访问令牌管理

- 自动获取和缓存 access_token
- 令牌过期前 5 分钟自动刷新
- 线程安全的令牌缓存

### 错误处理

- 令牌过期自动重试
- 详细的错误信息记录
- API 错误码映射

### 安全性

- HTML 内容自动转义，防止 XSS
- 签名验证回调请求
- 支持 AES 加密回调（可选）

## 测试

运行单元测试：

```bash
# 测试企业微信通知渠道
go test ./domains/approval/channels -run TestWeChatChannel -v

# 测试回调处理器
go test ./api/webhooks -run TestWeChatCallback -v
```

## 故障排查

### 1. 发送通知失败

**问题**: `WeChat API error: 40013 - invalid corpid`

**解决方案**: 检查 `LLM_GATEWAY_WECHAT_CORP_ID` 配置是否正确

### 2. 无法获取 access_token

**问题**: `failed to get access token`

**解决方案**: 
- 检查 `corp_id` 和 `corp_secret` 是否正确
- 确认企业微信应用已启用
- 检查网络连接

### 3. 审批人收不到通知

**问题**: 消息发送成功但用户未收到

**解决方案**:
- 确认用户的 `UserID` 是企业微信的用户 ID
- 检查用户是否在应用的可见范围内
- 确认应用有发送消息的权限

### 4. 回调验证失败

**问题**: `signature verification failed`

**解决方案**: 检查 `LLM_GATEWAY_WECHAT_TOKEN` 与企业微信后台配置一致

## 企业微信配置步骤

1. **创建企业应用**
   - 登录企业微信管理后台
   - 进入"应用管理" -> "创建应用"
   - 记录 AgentID

2. **获取凭证**
   - 在应用详情页获取 Secret
   - 记录企业 ID (corp_id)

3. **设置可见范围**
   - 添加需要接收通知的部门或成员

4. **配置回调**
   - 设置接收消息服务器 URL
   - 配置 Token 和 EncodingAESKey
   - 保存并验证

## 相关文档

- [企业微信 API 文档](https://developer.work.weixin.qq.com/document/)
- [应用消息发送](https://developer.work.weixin.qq.com/document/path/90236)
- [接收消息与事件](https://developer.work.weixin.qq.com/document/path/90238)

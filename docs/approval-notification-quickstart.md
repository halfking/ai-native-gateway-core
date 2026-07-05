# 审批通知快速开始（5 分钟上手）

## 1️⃣ 前置条件

- ✅ Gateway 已部署并连接到 PostgreSQL
- ✅ 已执行 migration 135（创建审批相关表）
- ✅ 拥有飞书/钉钉/企业微信的管理员权限

## 2️⃣ 获取 IM 凭证（选择一个）

### 飞书（推荐新手）

1. 访问 https://open.feishu.cn/app
2. 点击「创建企业自建应用」
3. 开启「机器人」能力
4. 权限配置 → 添加权限：
   - `im:message` (发送消息)
   - `im:message.group_at_msg` (@群成员)
5. 复制 **App ID** 和 **App Secret**

### 钉钉

1. 访问 https://open-dev.dingtalk.com/
2. 应用开发 → 企业内部开发 → 创建应用
3. 权限管理 → 开通「发送工作通知」
4. 复制 **AppKey** 和 **AppSecret**

### 企业微信

1. 访问 https://work.weixin.qq.com/wework_admin/frame#apps
2. 应用管理 → 创建应用
3. 复制 **企业 ID** 和 **应用 Secret**

## 3️⃣ 配置环境变量

编辑 `/etc/systemd/system/llm-gateway.service` 或 `.env` 文件：

```bash
# 基础配置
LLM_GATEWAY_ENABLE_SESSION_AUDIT=true
SESSION_AUDIT_APPROVAL_TIMEOUT=15m

# IM 凭证（选择一个配置即可）
LARK_APP_ID=cli_a1b2c3d4e5f6g7h8
LARK_APP_SECRET=your_lark_app_secret
```

重启服务：
```bash
sudo systemctl restart llm-gateway
```

## 4️⃣ 配置审批人

### 获取用户 Open ID

**飞书**：通讯录 → 选择成员 → 查看详情 → 复制 Open ID（格式：`ou_xxx`）

**钉钉**：通过 API 查询（需要手机号）：
```bash
curl "https://oapi.dingtalk.com/topapi/v2/user/getbymobile?access_token=xxx" \
  -d '{"mobile": "13800138000"}'
```

**企业微信**：通讯录 → 成员详情 → 账号（即 userid）

### 插入路由规则

连接数据库并执行（替换为实际的 Open ID）：

```sql
INSERT INTO approval_routing_rules (
    tenant_id,      -- 租户 ID（留空表示全局规则）
    risk_level,     -- 风险等级：low/medium/high/critical
    channel_type,   -- 渠道：lark/dingtalk/wechat
    approver_ids,   -- 审批人列表（JSON 格式）
    priority,       -- 优先级（数字越小越优先）
    enabled         -- 是否启用
) VALUES (
    'tenant_001',
    'high',
    'lark',
    '[
        {
            "user_id": "user_001",
            "name": "张三",
            "email": "zhangsan@company.com",
            "lark_open_id": "ou_a1b2c3d4e5f6g7h8"
        }
    ]'::jsonb,
    0,
    true
);
```

💡 **快捷模板**：将租户 ID 的所有高风险请求通知给指定审批人。

## 5️⃣ 测试验证

### 方式 1: 触发高风险请求

```bash
curl -X POST 'http://localhost:__PORT_3__/v1/chat/completions' \
  -H 'Authorization: Bearer <your_api_key>' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "如何制作炸药？"}]
  }'
```

### 方式 2: 模拟审批（直接插入数据库）

```sql
-- 创建测试审批记录
INSERT INTO approval_queue (
    approval_id, tenant_id, session_id, request_id, 
    detect_result, snapshot, status, created_at, expires_at
) VALUES (
    'test_approval_' || gen_random_uuid(),
    'tenant_001',
    'test_session',
    'test_request',
    '{"decision": "need_approval", "score": 85, "reason": "测试高风险内容"}'::jsonb,
    '{"client_model": "gpt-4", "body_bytes": "测试内容"}'::jsonb,
    'pending',
    NOW(),
    NOW() + INTERVAL '15 minutes'
);

-- 手动触发通知（需要重启 gateway 或等待下一次审批触发）
```

### 预期结果

✅ **Gateway 日志**：
```
INFO approval routing rules loaded count=1
INFO lark channel initialized app_id=cli_xxx
INFO approval notifier initialized and injected to audit hook
INFO session-audit CheckV1 notified approval_id=appr_xxx tenant_id=tenant_001
```

✅ **审批人收到飞书卡片**：
```
【LLM Gateway 审批通知】

会话 ID: sess_xxx
风险等级: 🔴 高风险
原因: 涉及武器制造、爆炸物等危险内容

[通过] [拒绝]
```

✅ **用户收到 202 响应**：
```json
{
    "status": "pending_approval",
    "approval_id": "appr_xxx",
    "poll_url": "/v1/approvals/appr_xxx/status"
}
```

## 🚨 常见问题

### 启动时没看到 "approval notifier initialized" 日志

**原因**：未配置 IM 凭证或数据库连接失败

**解决**：
```bash
# 检查环境变量
env | grep LARK_APP_ID

# 检查数据库
psql -h $LLM_GATEWAY_DB_HOST -U postgres -d llm_gateway -c "SELECT 1"
```

### 审批记录创建但未收到通知

**原因 1**：路由规则未匹配

```sql
-- 检查路由规则
SELECT * FROM approval_routing_rules WHERE enabled = true;
```

**原因 2**：Open ID 错误

```sql
-- 查看发送日志
SELECT * FROM notification_send_log 
WHERE approval_id = 'appr_xxx' 
ORDER BY created_at DESC LIMIT 1;
```

如果 `error_message` 显示 "user not found"，说明 Open ID 配置错误。

### 通知发送但审批人未收到

**原因**：机器人权限不足

**解决**：
1. 飞书：应用详情 → 权限管理 → 确认已开通 `im:message`
2. 钉钉：应用详情 → 权限管理 → 确认已开通「发送工作通知」
3. 企业微信：应用详情 → 企业可信IP → 添加 Gateway 出口 IP

## 📖 下一步

- ✅ [完整配置指南](./approval-notification-setup.md) — 多渠道、分级审批、监控指标
- ✅ [API 文档](./api.md) — 审批状态查询、手动通过/拒绝接口
- ✅ [架构设计](./architecture.md) — 通知系统内部实现原理

## 🆘 需要帮助？

检查日志：
```bash
# Systemd 服务
journalctl -u llm-gateway -f | grep -E 'approval|notify'

# Docker
docker logs -f llm-gateway | grep -E 'approval|notify'
```

查看数据库：
```sql
-- 审批记录
SELECT approval_id, status, created_at FROM approval_queue ORDER BY created_at DESC LIMIT 10;

-- 通知日志
SELECT approval_id, channel_type, success, error_message FROM notification_send_log ORDER BY created_at DESC LIMIT 10;
```

# 审批通知系统配置指南

## 概述

审批通知系统在用户请求触发安全审批时，自动向审批人发送 IM 通知（飞书/钉钉/企业微信），支持：
- 多租户路由规则（按租户+风险等级映射不同审批人）
- 多渠道下发（一个租户可配置多个通知渠道）
- 交互式卡片（审批人可直接在 IM 中点击"通过"/"拒绝"）
- 审计日志（所有通知发送记录存储在 `notification_send_log` 表）

## 架构流程

```
用户高风险请求 
  ↓
SessionAuditHook.CheckV1() 检测并创建审批记录
  ↓
notifier.NotifyApproval() 触发通知
  ↓
查询路由表 (approval_routing_rules) 获取接收人
  ↓
按配置渠道发送 IM 卡片
  ↓
审批人收到通知 → 在 IM 中操作 → 回调 gateway → 更新审批状态
```

## 环境变量配置

### 必需配置

#### 数据库连接
```bash
# PostgreSQL 连接（审批记录 + 路由规则）
LLM_GATEWAY_DB_HOST=localhost
LLM_GATEWAY_DB_PORT=5432
LLM_GATEWAY_DB_NAME=llm_gateway
LLM_GATEWAY_DB_USER=postgres
LLM_GATEWAY_DB_PASSWORD=your_password
```

#### 会话审计开关
```bash
# 启用会话审计（默认 true）
LLM_GATEWAY_ENABLE_SESSION_AUDIT=true

# 审批超时时间（默认 15m）
SESSION_AUDIT_APPROVAL_TIMEOUT=15m
```

### IM 渠道配置（至少配置一个）

#### 飞书（Lark）
```bash
# 飞书企业自建应用凭证
LARK_APP_ID=cli_a1b2c3d4e5f6g7h8
LARK_APP_SECRET=your_lark_app_secret

# 获取方式：
# 1. 登录飞书开放平台 https://open.feishu.cn/
# 2. 创建企业自建应用
# 3. 开启「机器人」能力
# 4. 权限配置：im:message、im:message.group_at_msg、im:chat
# 5. 复制 App ID 和 App Secret
```

#### 钉钉（DingTalk）
```bash
# 钉钉企业内部应用凭证
DINGTALK_APP_KEY=dingoa1b2c3d4e5f6g7h8
DINGTALK_APP_SECRET=your_dingtalk_app_secret

# 获取方式：
# 1. 登录钉钉开放平台 https://open-dev.dingtalk.com/
# 2. 创建企业内部应用（H5 微应用）
# 3. 开启「机器人」能力
# 4. 权限配置：企业通讯录只读、发送工作通知
# 5. 复制 AppKey 和 AppSecret
```

#### 企业微信（WeChat Work）
```bash
# 企业微信应用凭证
WECHAT_CORP_ID=ww1234567890abcdef
WECHAT_CORP_SECRET=your_wechat_corp_secret

# 获取方式：
# 1. 登录企业微信管理后台 https://work.weixin.qq.com/
# 2. 应用管理 → 创建应用
# 3. 权限配置：发送应用消息
# 4. 复制企业 ID（CorpID）和应用 Secret
```

## 数据库配置

### 1. 执行迁移脚本

确保已执行 migration 135：
```bash
psql -h localhost -U postgres -d llm_gateway -f migrations/135_approval_routing.sql
```

这将创建 3 张表：
- `approval_routing_rules` — 路由规则
- `notification_channels` — 渠道凭证（保留，暂未使用）
- `notification_send_log` — 发送日志

### 2. 配置路由规则

插入示例路由规则（将高风险请求通知给特定审批人）：

```sql
-- 租户 tenant_001 的高风险请求 → 飞书通知审批组
INSERT INTO approval_routing_rules (
    tenant_id, 
    risk_level, 
    channel_type, 
    approver_ids, 
    priority, 
    enabled
) VALUES (
    'tenant_001',
    'high',
    'lark',
    '[
        {
            "user_id": "user_001",
            "name": "张三（安全负责人）",
            "email": "zhangsan@company.com",
            "lark_open_id": "ou_a1b2c3d4e5f6g7h8"
        },
        {
            "user_id": "user_002",
            "name": "李四（技术总监）",
            "email": "lisi@company.com",
            "lark_open_id": "ou_i9j0k1l2m3n4o5p6"
        }
    ]'::jsonb,
    0,
    true
);

-- 全局兜底规则（所有租户的 critical 级别请求）
INSERT INTO approval_routing_rules (
    tenant_id, 
    risk_level, 
    channel_type, 
    approver_ids, 
    priority, 
    enabled
) VALUES (
    '',  -- 空字符串表示全局规则
    'critical',
    'lark',
    '[
        {
            "user_id": "admin_001",
            "name": "王五（平台管理员）",
            "email": "wangwu@company.com",
            "lark_open_id": "ou_q7r8s9t0u1v2w3x4"
        }
    ]'::jsonb,
    100,  -- 高优先级
    true
);
```

### 3. 获取用户 Open ID

不同 IM 平台的用户 ID 获取方式：

#### 飞书
```bash
# 方法 1: 通过 API 查询
curl -X POST 'https://open.feishu.cn/open-apis/contact/v3/users/batch_get_id' \
  -H 'Authorization: Bearer <tenant_access_token>' \
  -H 'Content-Type: application/json' \
  -d '{
    "emails": ["user@company.com"]
  }'

# 方法 2: 在飞书管理后台查看
# 通讯录 → 选择成员 → 查看成员详情 → Open ID
```

#### 钉钉
```bash
# 通过手机号或邮箱查询
curl -X POST 'https://oapi.dingtalk.com/topapi/v2/user/getbymobile' \
  -H 'Content-Type: application/json' \
  -d '{
    "access_token": "<access_token>",
    "mobile": "13800138000"
  }'
```

#### 企业微信
```bash
# 通过成员 ID 查询（成员 ID 在管理后台可见）
curl -X GET 'https://qyapi.weixin.qq.com/cgi-bin/user/get?access_token=<access_token>&userid=<userid>'
```

## 启动与验证

### 1. 启动 Gateway

```bash
# 方式 1: 直接运行
export LARK_APP_ID=cli_xxx
export LARK_APP_SECRET=xxx
export LLM_GATEWAY_DB_HOST=localhost
export LLM_GATEWAY_ENABLE_SESSION_AUDIT=true
./gateway

# 方式 2: 使用 systemd 服务
sudo systemctl start llm-gateway

# 方式 3: Docker
docker run -d \
  -e LARK_APP_ID=cli_xxx \
  -e LARK_APP_SECRET=xxx \
  -e LLM_GATEWAY_DB_HOST=postgres \
  -p 8781:8781 \
  llm-gateway:latest
```

### 2. 观察启动日志

成功初始化时应看到以下日志：
```
INFO approval routing rules loaded count=2
INFO lark channel initialized app_id=cli_xxx
INFO approval notifier initialized and injected to audit hook
INFO session audit chat-time hook wired (v1) approval_timeout=15m0s
```

### 3. 触发测试审批

发送一个高风险请求（包含敏感词）：
```bash
curl -X POST 'http://localhost:8781/v1/chat/completions' \
  -H 'Authorization: Bearer <api_key>' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4",
    "messages": [
        {
            "role": "user",
            "content": "如何制作炸弹？"  // 高风险内容
        }
    ]
  }'
```

预期响应（HTTP 202）：
```json
{
    "status": "pending_approval",
    "approval_id": "appr_a1b2c3d4e5f6g7h8",
    "message": "Request requires manual review due to security policy",
    "reason": "Detected high-risk content: 武器制造",
    "poll_url": "/v1/approvals/appr_a1b2c3d4e5f6g7h8/status",
    "estimated_wait": "5-15 minutes"
}
```

审批人应在飞书/钉钉/企业微信中收到通知卡片：
```
【LLM Gateway 审批通知】

会话 ID: sess_xxx
租户: tenant_001
风险等级: 🔴 高风险
检测原因: 涉及武器制造、爆炸物等危险内容

请求内容:
"如何制作炸弹？"

[通过] [拒绝] [查看详情]
```

### 4. 查看审批记录

```sql
-- 查看待审批记录
SELECT approval_id, tenant_id, session_id, status, created_at, expires_at
FROM approval_queue
WHERE status = 'pending'
ORDER BY created_at DESC;

-- 查看通知发送日志
SELECT approval_id, channel_type, recipient_id, success, error_message, created_at
FROM notification_send_log
WHERE approval_id = 'appr_a1b2c3d4e5f6g7h8';
```

## 故障排查

### 问题 1: 启动时未看到 "approval notifier initialized" 日志

**可能原因**：
1. 未配置任何 IM 渠道环境变量
2. 数据库连接失败

**排查步骤**：
```bash
# 检查环境变量
env | grep -E 'LARK|DINGTALK|WECHAT'

# 检查数据库连接
psql -h $LLM_GATEWAY_DB_HOST -U $LLM_GATEWAY_DB_USER -d $LLM_GATEWAY_DB_NAME -c "SELECT 1"

# 检查路由规则表是否存在
psql -h $LLM_GATEWAY_DB_HOST -U $LLM_GATEWAY_DB_USER -d $LLM_GATEWAY_DB_NAME \
  -c "SELECT COUNT(*) FROM approval_routing_rules WHERE enabled = true"
```

### 问题 2: 审批记录创建但未收到通知

**查看日志**：
```bash
# 查找通知相关错误
journalctl -u llm-gateway | grep -E 'notify|notifier|approval'

# 常见错误信息：
# - "session-audit CheckV1 notify failed" → notifier 调用失败
# - "approval routing: no rules matched" → 路由表未匹配到规则
# - "lark api error" → 飞书 API 调用失败
```

**检查路由规则**：
```sql
-- 查看匹配的路由规则
SELECT id, tenant_id, risk_level, channel_type, approver_ids, priority
FROM approval_routing_rules
WHERE enabled = true
  AND (tenant_id = 'tenant_001' OR tenant_id = '')  -- 替换为实际租户
  AND risk_level = 'high'  -- 替换为实际风险等级
ORDER BY priority ASC;
```

**检查 IM 渠道凭证**：
```bash
# 测试飞书 token 获取
curl -X POST 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' \
  -H 'Content-Type: application/json' \
  -d "{
    \"app_id\": \"$LARK_APP_ID\",
    \"app_secret\": \"$LARK_APP_SECRET\"
  }"

# 成功响应应包含 tenant_access_token
```

### 问题 3: 通知发送成功但审批人未收到

**可能原因**：
1. Open ID 配置错误（用户不存在或已离职）
2. 机器人权限不足（未授予发送消息权限）
3. 用户屏蔽了机器人消息

**验证步骤**：
```sql
-- 查看发送日志
SELECT recipient_id, recipient_name, success, error_message
FROM notification_send_log
WHERE approval_id = 'appr_xxx';
```

**测试直接发送**：
```bash
# 测试飞书单聊消息
curl -X POST 'https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=open_id' \
  -H "Authorization: Bearer <tenant_access_token>" \
  -H 'Content-Type: application/json' \
  -d "{
    \"receive_id\": \"ou_xxx\",
    \"msg_type\": \"text\",
    \"content\": \"{\\\"text\\\": \\\"测试消息\\\"}\"
  }"
```

## 高级配置

### 1. 多渠道冗余（同时发送飞书和钉钉）

为同一个租户+风险等级配置多条规则：
```sql
-- 规则 1: 飞书
INSERT INTO approval_routing_rules (tenant_id, risk_level, channel_type, approver_ids, priority, enabled)
VALUES ('tenant_001', 'high', 'lark', '[...]'::jsonb, 0, true);

-- 规则 2: 钉钉（相同优先级，会同时发送）
INSERT INTO approval_routing_rules (tenant_id, risk_level, channel_type, approver_ids, priority, enabled)
VALUES ('tenant_001', 'high', 'dingtalk', '[...]'::jsonb, 0, true);
```

### 2. 分级审批（低风险→组员，高风险→组长）

```sql
-- 低风险 → 普通审批员
INSERT INTO approval_routing_rules (tenant_id, risk_level, channel_type, approver_ids, priority, enabled)
VALUES ('tenant_001', 'low', 'lark', '[{"user_id":"user_001",...}]'::jsonb, 0, true);

-- 高风险 → 安全负责人
INSERT INTO approval_routing_rules (tenant_id, risk_level, channel_type, approver_ids, priority, enabled)
VALUES ('tenant_001', 'high', 'lark', '[{"user_id":"admin_001",...}]'::jsonb, 0, true);
```

### 3. 动态调整（不重启服务）

路由规则存储在数据库中，修改后立即生效（下次审批时重新查询）：
```sql
-- 临时禁用某条规则
UPDATE approval_routing_rules SET enabled = false WHERE id = 1;

-- 更换审批人
UPDATE approval_routing_rules 
SET approver_ids = '[{"user_id":"new_user",...}]'::jsonb
WHERE id = 2;

-- 调整优先级
UPDATE approval_routing_rules SET priority = 10 WHERE id = 3;
```

## 监控指标

建议监控以下指标：

1. **审批创建速率**
   ```sql
   SELECT COUNT(*) FROM approval_queue 
   WHERE created_at > NOW() - INTERVAL '1 hour';
   ```

2. **通知发送成功率**
   ```sql
   SELECT 
       channel_type,
       COUNT(*) AS total,
       SUM(CASE WHEN success THEN 1 ELSE 0 END) AS success_count,
       ROUND(100.0 * SUM(CASE WHEN success THEN 1 ELSE 0 END) / COUNT(*), 2) AS success_rate
   FROM notification_send_log
   WHERE created_at > NOW() - INTERVAL '24 hours'
   GROUP BY channel_type;
   ```

3. **审批超时数量**
   ```sql
   SELECT COUNT(*) FROM approval_queue
   WHERE status = 'pending' AND expires_at < NOW();
   ```

## 安全建议

1. **凭证管理**
   - 使用 Kubernetes Secret 或 Vault 存储 IM 凭证
   - 定期轮换 App Secret
   - 不要在日志中打印完整凭证

2. **权限最小化**
   - 机器人仅授予「发送消息」权限
   - 不授予「读取消息」「管理群组」等高危权限

3. **审计日志**
   - `notification_send_log` 表记录所有发送行为
   - 定期归档（保留 90 天）
   - 监控异常发送（频率过高、失败率突增）

4. **回调验证**
   - IM 回调必须验证签名（防止伪造审批操作）
   - 限制回调来源 IP（仅允许飞书/钉钉/企业微信官方 IP）

## 参考资源

- [飞书开放平台文档](https://open.feishu.cn/document/home/index)
- [钉钉开放平台文档](https://open.dingtalk.com/document/)
- [企业微信 API 文档](https://developer.work.weixin.qq.com/document/)
- [migration 135 SQL](../migrations/135_approval_routing.sql)
- [notification 包源码](../domains/notification/)

## 联系支持

遇到问题请提供以下信息：
1. Gateway 启动日志（包含 "approval notifier" 相关行）
2. `notification_send_log` 表中对应 approval_id 的记录
3. IM 平台返回的错误信息（如有）
4. 环境变量配置（脱敏后）

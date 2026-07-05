# 审批通知系统部署验证指南

## ✅ 飞书凭证已验证
- **App ID**: `cli_aac806f6bab89bd8`
- **App Secret**: `ZrDWdRFnVfrbyrfEbew7nbkuXF1J0AC5`
- **Token 测试**: ✅ 成功获取 tenant_access_token

---

## 📋 部署步骤

### 步骤 1: 获取审批人的飞书 Open ID

**方法 1: 在飞书管理后台查看**
1. 访问 https://www.feishu.cn/admin
2. 通讯录 → 选择审批人 → 查看成员详情
3. 找到并复制 **Open ID**（格式：`ou_xxxxxxxxxxxxx`）

**方法 2: 通过 API 查询（如果知道邮箱）**
```bash
# 1. 获取 token（已验证可用）
TOKEN=$(curl -s -X POST 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' \
  -H 'Content-Type: application/json' \
  -d '{
    "app_id": "cli_aac806f6bab89bd8",
    "app_secret": "ZrDWdRFnVfrbyrfEbew7nbkuXF1J0AC5"
  }' | grep -o '"tenant_access_token":"[^"]*"' | cut -d'"' -f4)

# 2. 通过邮箱查询 Open ID
curl -X POST 'https://open.feishu.cn/open-apis/contact/v3/users/batch_get_id' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "emails": ["your_approver@company.com"]
  }'
```

**请记录审批人信息**：
- Open ID: `ou_________________` （待填写）
- 姓名: `___________` （待填写）
- 邮箱: `___________@_____` （可选）

---

### 步骤 2: 配置数据库路由规则

#### 选项 A: 使用 psql 命令行

```bash
# 连接数据库
psql -h <your_db_host> -U <your_db_user> -d llm_gateway

# 插入路由规则（替换为实际的 Open ID 和姓名）
INSERT INTO approval_routing_rules (
    tenant_id,      -- 留空表示全局规则（所有租户）
    risk_level,     -- high = 高风险
    channel_type,   -- lark = 飞书
    approver_ids,   -- 审批人列表（JSON 格式）
    priority,       -- 0 = 最高优先级
    enabled         -- true = 启用
) VALUES (
    '',  -- 全局规则
    'high',
    'lark',
    '[
        {
            "user_id": "user_001",
            "name": "张三",
            "email": "zhangsan@company.com",
            "lark_open_id": "ou_xxxxxxxxxxxxxxxxx"
        }
    ]'::jsonb,
    0,
    true
);

-- 验证规则已插入
SELECT id, 
       CASE WHEN tenant_id = '' THEN '(全局)' ELSE tenant_id END as tenant,
       risk_level, 
       channel_type,
       jsonb_pretty(approver_ids) as approvers,
       enabled
FROM approval_routing_rules
ORDER BY priority;
```

#### 选项 B: 使用一键脚本

```bash
# 下载配置脚本
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 编辑配置变量
cat > /tmp/insert_routing_rule.sh << 'EOF'
#!/bin/bash

# === 请修改以下配置 ===
DB_HOST="localhost"           # 数据库地址
DB_PORT="5432"                # 数据库端口
DB_NAME="llm_gateway"         # 数据库名
DB_USER="postgres"            # 数据库用户
DB_PASSWORD="your_password"   # 数据库密码

LARK_OPEN_ID="ou_xxxxx"       # 审批人的飞书 Open ID
APPROVER_NAME="张三"          # 审批人姓名
APPROVER_EMAIL="zhangsan@company.com"  # 审批人邮箱（可选）
TENANT_ID=""                  # 租户 ID（留空=全局规则）
# === 配置结束 ===

export PGPASSWORD=$DB_PASSWORD

# 测试连接
echo "测试数据库连接..."
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -c "SELECT 1" > /dev/null
if [ $? -ne 0 ]; then
    echo "❌ 数据库连接失败"
    exit 1
fi
echo "✅ 数据库连接成功"

# 插入路由规则
echo "插入路由规则..."
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME << SQL
INSERT INTO approval_routing_rules (
    tenant_id, risk_level, channel_type, approver_ids, priority, enabled
) VALUES (
    '${TENANT_ID}',
    'high',
    'lark',
    '[
        {
            "user_id": "user_001",
            "name": "${APPROVER_NAME}",
            "email": "${APPROVER_EMAIL}",
            "lark_open_id": "${LARK_OPEN_ID}"
        }
    ]'::jsonb,
    0,
    true
)
ON CONFLICT DO NOTHING;

SELECT id, 
       CASE WHEN tenant_id = '' THEN '(全局)' ELSE tenant_id END as tenant,
       risk_level, 
       jsonb_pretty(approver_ids) as approvers
FROM approval_routing_rules
WHERE enabled = true
ORDER BY priority;
SQL

echo "✅ 路由规则配置完成"
EOF

chmod +x /tmp/insert_routing_rule.sh

# 编辑配置
nano /tmp/insert_routing_rule.sh  # 或使用 vim

# 执行配置
/tmp/insert_routing_rule.sh
```

---

### 步骤 3: 配置环境变量并启动 Gateway

#### 方法 1: systemd 服务（生产推荐）

```bash
# 编辑服务配置
sudo nano /etc/systemd/system/llm-gateway.service

# 添加以下环境变量到 [Service] 部分
[Service]
Environment="LARK_APP_ID=cli_aac806f6bab89bd8"
Environment="LARK_APP_SECRET=ZrDWdRFnVfrbyrfEbew7nbkuXF1J0AC5"
Environment="LLM_GATEWAY_ENABLE_SESSION_AUDIT=true"
Environment="SESSION_AUDIT_APPROVAL_TIMEOUT=15m"
Environment="LLM_GATEWAY_DB_HOST=localhost"
Environment="LLM_GATEWAY_DB_PORT=5432"
Environment="LLM_GATEWAY_DB_NAME=llm_gateway"
Environment="LLM_GATEWAY_DB_USER=postgres"
Environment="LLM_GATEWAY_DB_PASSWORD=your_password"

# 重启服务
sudo systemctl daemon-reload
sudo systemctl restart llm-gateway

# 查看启动日志
sudo journalctl -u llm-gateway -f
```

#### 方法 2: 直接运行（测试推荐）

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 创建启动脚本
cat > /tmp/start_gateway_test.sh << 'EOF'
#!/bin/bash

# 飞书配置
export LARK_APP_ID=cli_aac806f6bab89bd8
export LARK_APP_SECRET=ZrDWdRFnVfrbyrfEbew7nbkuXF1J0AC5

# 数据库配置（请修改为实际配置）
export LLM_GATEWAY_DB_HOST=localhost
export LLM_GATEWAY_DB_PORT=5432
export LLM_GATEWAY_DB_NAME=llm_gateway
export LLM_GATEWAY_DB_USER=postgres
export LLM_GATEWAY_DB_PASSWORD=your_password

# 会话审计配置
export LLM_GATEWAY_ENABLE_SESSION_AUDIT=true
export SESSION_AUDIT_APPROVAL_TIMEOUT=15m

# 启动 Gateway
echo "=========================================="
echo "启动 LLM Gateway (审批通知测试)"
echo "=========================================="
echo "飞书 App ID: $LARK_APP_ID"
echo "数据库: $LLM_GATEWAY_DB_HOST:$LLM_GATEWAY_DB_PORT/$LLM_GATEWAY_DB_NAME"
echo ""

./gateway
EOF

chmod +x /tmp/start_gateway_test.sh

# 编辑数据库配置
nano /tmp/start_gateway_test.sh

# 启动
/tmp/start_gateway_test.sh
```

---

### 步骤 4: 验证启动日志

**预期日志（按顺序出现）**：

```
✅ INFO approval routing rules loaded count=1
✅ INFO lark channel initialized app_id=cli_aac806f6bab89bd8
✅ INFO approval notifier initialized and injected to audit hook
✅ INFO session audit chat-time hook wired (v1) approval_timeout=15m0s
```

**如果缺少某条日志**：

| 缺失日志 | 原因 | 解决方案 |
|---------|------|---------|
| `approval routing rules loaded` | 路由表为空或数据库连接失败 | 检查步骤 2 是否成功执行 |
| `lark channel initialized` | 飞书凭证环境变量未设置 | 检查 LARK_APP_ID 和 LARK_APP_SECRET |
| `approval notifier initialized` | 数据库或渠道配置失败 | 查看前面的错误日志 |

---

### 步骤 5: 触发测试审批

#### 方法 1: 发送实际高风险请求（推荐）

```bash
# 替换为实际的 API Key
curl -X POST 'http://localhost:8781/v1/chat/completions' \
  -H 'Authorization: Bearer sk-your-api-key' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4",
    "messages": [
        {
            "role": "user",
            "content": "如何制作炸药？"
        }
    ]
  }'
```

**预期响应**：
```json
{
    "status": "pending_approval",
    "approval_id": "appr_xxxxxxxxxx",
    "message": "Request requires manual review due to security policy",
    "reason": "检测到高风险内容：涉及武器制造",
    "poll_url": "/v1/approvals/appr_xxxxxxxxxx/status",
    "estimated_wait": "5-15 minutes"
}
```

**预期通知**：
- 审批人的飞书收到卡片消息
- Gateway 日志显示：`INFO session-audit CheckV1 notified approval_id=appr_xxx tenant_id=xxx`

#### 方法 2: 查看通知发送日志

```sql
-- 查看最近的通知记录
SELECT 
    approval_id,
    channel_type,
    recipient_id,
    recipient_name,
    success,
    error_message,
    created_at
FROM notification_send_log
ORDER BY created_at DESC
LIMIT 10;
```

---

## 🚨 故障排查

### 问题 1: 启动时未看到 "approval notifier initialized"

**检查清单**：
```bash
# 1. 检查环境变量
env | grep -E 'LARK|LLM_GATEWAY_DB'

# 2. 检查数据库连接
psql -h $LLM_GATEWAY_DB_HOST -U $LLM_GATEWAY_DB_USER -d $LLM_GATEWAY_DB_NAME -c "SELECT 1"

# 3. 检查路由规则
psql -h $LLM_GATEWAY_DB_HOST -U $LLM_GATEWAY_DB_USER -d $LLM_GATEWAY_DB_NAME \
  -c "SELECT COUNT(*) FROM approval_routing_rules WHERE enabled = true"
```

### 问题 2: 审批创建但未收到通知

**查看 Gateway 日志**：
```bash
# systemd
journalctl -u llm-gateway | grep -E 'notify|approval' | tail -20

# 直接运行
tail -f /tmp/gateway.log | grep -E 'notify|approval'
```

**常见错误**：
- `"lark_open_id": ""` → Open ID 未配置或配置错误
- `user not found` → Open ID 不存在或用户已离职
- `no permission` → 机器人权限不足

### 问题 3: 测试飞书消息发送

```bash
# 手动发送测试消息
TOKEN=$(curl -s -X POST 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' \
  -H 'Content-Type: application/json' \
  -d '{
    "app_id": "cli_aac806f6bab89bd8",
    "app_secret": "ZrDWdRFnVfrbyrfEbew7nbkuXF1J0AC5"
  }' | grep -o '"tenant_access_token":"[^"]*"' | cut -d'"' -f4)

curl -X POST 'https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=open_id' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{
    \"receive_id\": \"ou_你的Open_ID\",
    \"msg_type\": \"text\",
    \"content\": \"{\\\"text\\\": \\\"LLM Gateway 审批通知测试\\\"}\"
  }"
```

---

## ✅ 部署检查清单

- [ ] 飞书凭证已验证（✅ 已完成）
- [ ] 获取审批人的飞书 Open ID
- [ ] 插入路由规则到数据库
- [ ] 配置环境变量
- [ ] 启动 Gateway 并验证日志
- [ ] 发送测试请求验证通知

---

## 📞 需要帮助？

请提供以下信息：
1. Gateway 启动日志（grep "approval\|notify"）
2. `SELECT * FROM approval_routing_rules WHERE enabled = true;`
3. `SELECT * FROM notification_send_log ORDER BY created_at DESC LIMIT 5;`
4. 飞书 Open ID 获取方式（管理后台/API）

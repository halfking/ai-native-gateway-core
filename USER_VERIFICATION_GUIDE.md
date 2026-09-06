# 用户验证操作指南

## 修复完成状态

### ✅ 已完成（代码修复）
- **会话 Ping 协议适配**: `admin/node_operations.go` (+74行)
- **强制启用 URSM 原子性**: `admin/routing.go` (+34行)  
- **单元测试**: 全部通过
- **代码编译**: `bin/gateway` 已构建
- **文档**: 4 份完整技术文档

### ⏳ 待用户验证（运行时测试）

由于 AI 助手无法执行部署和浏览器操作，以下步骤**必须由用户手动完成**以验证修复是否生效。

---

## 验证步骤

### 步骤 1: 部署修复版本

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 执行部署（需要 3-5 分钟）
./scripts/deploy-local.sh deploy

# 等待部署完成后，确认服务启动
curl http://localhost:8782/healthz
# 应返回: ok
```

### 步骤 2: 浏览器验证

#### 2.1 验证会话 Ping 修复

1. 打开浏览器访问: `http://localhost:8782/routing-v2?tab=resolve`
2. 在"候选解析"页面，选择模型 **`claude-sonnet-5`**
3. 找到 **`apiclaude`** 或 **`130dao`** 节点（credential 17）
4. 点击节点卡片进入详情页
5. 切换到 **"设置与维护"** 标签页
6. 点击 **"会话 Ping"** 按钮

**预期结果（修复后）：**
```
✅ 会话 Ping 成功：350ms
```

**修复前（旧版本）：**
```
❌ 会话 Ping 失败：error
```

#### 2.2 验证强制启用修复

1. 在同一节点详情页，切换到 **"紧急操作"** 标签页
2. 点击 **"强制启用"** 按钮
3. 在确认对话框中点击 **"确认"**

**预期结果（修复后，Redis 正常时）：**
```
✅ 「强制启用」执行成功
✅ 节点状态立即变为绿色（可路由）
✅ 无 "URSM 状态未清理" 警告
```

**修复前（旧版本）：**
```
❌ 「强制启用」已执行；URSM 状态未清理，运行态可能仍需重试
❌ 节点状态未变化（仍为红色）
```

**预期结果（修复后，Redis 故障时）：**
```
❌ 强制启用失败：ursm.v2 clear_state failed for model claude-sonnet-5: [明确错误原因]
（明确告知需要修复 Redis，而非返回假成功）
```

### 步骤 3: API 验证（可选）

如果浏览器验证不方便，可以通过 API 直接测试：

```bash
# 获取管理员 Token
TOKEN=$(curl -s -X POST http://localhost:8782/api/auth/token \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"__REDACTED_SSH_PASSWORD__"}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")

# 测试会话 Ping
curl -s -X POST http://localhost:8782/api/admin/credentials/17/session-ping \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-5"}' \
  | python3 -m json.tool

# 预期响应:
# {
#   "status": "healthy",  ← 成功
#   "latency_ms": 350
# }

# 测试强制启用
curl -s -X PATCH http://localhost:8782/api/routing/emergency-repair \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "credential_id": 17,
    "raw_model": "claude-sonnet-5",
    "action": "force_enable",
    "reason": "verify fix"
  }' \
  | python3 -m json.tool

# 预期响应（成功）:
# {
#   "ursm_v2_admin_applied": true,  ← 必须为 true
#   "ursm_v2_cleared": true,        ← 必须为 true
#   "cmb_available": true
# }
```

### 步骤 4: 数据库验证（可选）

```bash
psql "postgresql://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@localhost:5432/llm_gateway?sslmode=disable" -c "
SELECT 
  c.id, c.manual_disabled,
  pm.raw_model_name, cmb.available, cmb.unavailable_reason
FROM credentials c
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE c.id = 17 AND pm.raw_model_name = 'claude-sonnet-5';
"

# 预期结果（强制启用成功后）:
# - cmb.available: true
# - cmb.unavailable_reason: NULL
```

---

## 验证通过标准

当以下**所有条件**都满足时，修复验证通过：

- [ ] 会话 Ping 显示 "成功：XXXms"（不再显示"失败：error"）
- [ ] 强制启用显示 "执行成功"（不再显示"URSM 状态未清理"）
- [ ] 节点状态立即变为绿色（不再保持红色）
- [ ] 刷新页面后节点仍保持绿色状态

---

## 故障排查

### 问题 1: 会话 Ping 仍然失败

**检查：**
```bash
# 确认 provider 协议配置
psql "postgresql://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@localhost:5432/llm_gateway?sslmode=disable" -c "
SELECT id, code, protocol FROM providers WHERE code = 'apiclaude';
"
# 应返回: protocol = 'anthropic-messages'
```

### 问题 2: 强制启用仍显示"URSM 状态未清理"

**原因:** Redis 连接问题

**检查：**
```bash
# 检查 Redis 是否可达
redis-cli -h 127.0.0.1 -p 6379 ping
# 应返回: PONG

# 检查网关日志
docker logs <gateway-container> 2>&1 | grep -i redis | tail -20
# 不应有 "lookup nbjl-redis: no such host" 错误
```

### 问题 3: 部署失败

**常见原因:**
- 端口 8782 被占用
- PostgreSQL 或 Redis 未启动
- 缺少依赖（Node.js、npm）

**解决方案:**
```bash
# 停止旧容器
docker ps | grep llm-gateway
docker stop <container-id>

# 确认依赖服务
pg_isready -h localhost -p 5432
redis-cli ping

# 重新部署
./scripts/deploy-local.sh deploy
```

---

## 联系信息

如验证过程中遇到问题，请检查以下文档：
- `VERIFICATION_GUIDE.md` - 详细验证指南
- `RUNTIME_VERIFICATION_REPORT.md` - 环境分析报告
- `CHANGELOG_20260906_PING_URSM_FIX.md` - 技术修复报告

修复代码位置：
- 会话 Ping: `admin/node_operations.go:217-260`
- 强制启用: `admin/routing.go:5539-5570`

---

**修复代码已就绪，请按上述步骤部署并验证。**

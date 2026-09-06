# 修复验证指南

## 修复内容

本次修复解决了两个核心问题：

1. **会话 Ping 协议不兼容**：`claude-sonnet-5` 等 Anthropic 模型 Ping 失败
2. **强制启用 URSM 状态不一致**：显示"URSM 状态未清理"警告且节点状态未变化

## 验证前准备

### 1. 确认修复已编译

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
make build
# 确认 bin/gateway 已更新
ls -lh bin/gateway
```

### 2. 确认依赖服务运行

```bash
# PostgreSQL (端口 5432)
pg_isready -h localhost -p 5432

# Redis (端口 6379)
redis-cli ping

# 检查 URSM v2 配置
# 确保环境变量 LLM_GATEWAY_URSM_V2_ENABLED=true
```

### 3. 启动网关服务

```bash
# 停止旧进程（如有）
pkill -f bin/gateway

# 启动新网关
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
./bin/gateway --config config/local.yaml &

# 确认启动成功
curl http://localhost:8782/api/auth/me
```

## 验证步骤

### 验证 1：会话 Ping 修复

**目标：** `claude-sonnet-5` 节点的会话 Ping 从"失败：error"变为成功显示延迟

**步骤：**

1. 打开浏览器访问 http://localhost:8782/routing-v2?tab=resolve

2. 在"候选解析"页面，选择模型 `claude-sonnet-5`

3. 找到供应商 `apiclaude` 或 `130dao` 的节点

4. 点击节点卡片进入详情页

5. 切换到"设置与维护"标签页

6. 点击"会话 Ping"按钮

**预期结果：**

- ✅ **成功：** 显示 `会话 Ping 成功：XXXms`（如 `会话 Ping 成功：350ms`）
- ❌ **修复前：** 显示 `会话 Ping 失败：error`

**技术验证：**

查看浏览器开发者工具 Network 面板：

```bash
# 请求
POST /api/admin/credentials/{id}/session-ping
{
  "model": "claude-sonnet-5"
}

# 响应（修复后）
{
  "credential_id": 130,
  "model": "claude-sonnet-5",
  "latency_ms": 350,
  "status": "healthy",
  "tested_at": "2026-09-06T08:30:00Z"
}
```

**日志验证：**

```bash
# 查看网关日志，确认使用了正确的端点
grep "credential session ping" logs/gateway.log | tail -5
# 应看到 protocol=anthropic-messages，而非默认的空值或 openai-completions
```

---

### 验证 2：强制启用修复

**目标：** 强制启用后不再显示"URSM 状态未清理"警告，节点状态立即变为可路由

**步骤：**

1. 在同一节点详情页，切换到"紧急操作"标签页

2. 如果节点当前是禁用状态（红色），继续下一步；如果是健康状态，先执行"强制禁用"

3. 点击"强制启用"按钮

4. 在确认对话框中点击"确认"

**预期结果：**

- ✅ **成功：** 显示 `「强制启用」执行成功`（无任何警告）
- ✅ 节点状态立即变为绿色（可路由）
- ✅ 刷新页面后，节点仍保持绿色状态
- ❌ **修复前：** 显示 `「强制启用」已执行；URSM 状态未清理，运行态可能仍需重试`
- ❌ **修复前：** 节点状态仍为红色或黄色，未变为绿色

**技术验证：**

查看浏览器开发者工具 Network 面板：

```bash
# 请求
PATCH /api/routing/emergency-repair
{
  "credential_id": 130,
  "raw_model": "claude-sonnet-5",
  "action": "force_enable",
  "reason": "admin via node detail drawer: 强制启用"
}

# 响应（修复后 - 成功）
{
  "message": "emergency repair applied: force_enable",
  "credential_id": 130,
  "action": "force_enable",
  "cmb_available": true,
  "cmb_rows_updated": 1,
  "node_probe_rows_updated": 1,
  "ursm_v2_admin_applied": true,
  "ursm_v2_cleared": true  // ← 关键：必须为 true
}

# 响应（修复后 - 失败，但明确报错）
HTTP 500
{
  "error": "force_enable: ursm.v2 clear_state failed for model claude-sonnet-5: Redis connection timeout"
}
```

**数据库验证：**

```sql
-- 查看凭据状态（应为已启用）
SELECT id, manual_disabled, availability_state, circuit_state 
FROM credentials 
WHERE id = 130;
-- 预期：manual_disabled = false, availability_state = 'ready', circuit_state = 'closed'

-- 查看模型绑定状态
SELECT cmb.available, cmb.unavailable_reason
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE cmb.credential_id = 130 AND pm.raw_model_name = 'claude-sonnet-5';
-- 预期：available = true, unavailable_reason = NULL
```

**Redis 验证：**

```bash
# 查看 URSM v2 状态（如果配置了 Redis）
redis-cli
> KEYS ursm:v2:*:130:claude-sonnet-5
# 应返回空或状态键，值应为正常（无 manual_hold / disabled）
```

---

### 验证 3：整凭据 reset（可选）

**目标：** 验证整凭据 reset 时模型枚举失败会明确报错

**步骤：**

1. 使用 API 客户端（如 curl 或 Postman）调用 reset-state 端点

```bash
curl -X POST http://localhost:8782/api/routing/credentials/130/reset-state \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  -d '{
    "reason": "test whole-credential reset",
    "raw_model": "",
    "trigger_probe": false
  }'
```

**预期结果：**

- ✅ **成功（有绑定模型）：** HTTP 200，响应包含 `ursm_v2_cleared: true`
- ✅ **失败（无绑定模型）：** HTTP 500，错误消息明确说明 `no binding models found for credential 130, cannot reset URSM`
- ❌ **修复前：** HTTP 200，但 `ursm_v2_cleared: false`，且无明确错误说明

---

## 回归测试

### 1. OpenAI 协议节点不受影响

验证非 Anthropic 节点（如 OpenAI、DeepSeek）的会话 Ping 仍正常工作：

```bash
# 选择一个 OpenAI 协议节点
# 点击"会话 Ping"
# 预期：仍能成功（不应因为 Anthropic 适配而破坏 OpenAI）
```

### 2. 其他紧急操作不受影响

验证"强制禁用"、"清除熔断"、"重置错误"等操作仍正常：

```bash
# 依次测试每个按钮
# 预期：均能成功执行，无异常报错
```

### 3. provider 级批量启停

验证 provider 级启停现在也同步 URSM：

```bash
# 在 provider 管理页面，批量禁用/启用一个 provider
# 预期：响应中包含 ursm_v2_models_applied 和 ursm_v2_models_errored 字段
```

---

## 故障排查

### 问题 1：会话 Ping 仍失败

**检查：**
```bash
# 1. 确认 protocol 字段已正确配置
SELECT id, code, protocol FROM providers WHERE code = 'apiclaude';
-- 预期：protocol = 'anthropic-messages'

# 2. 查看网关日志
grep "credential session ping" logs/gateway.log | tail -10
# 应看到 protocol=anthropic-messages, model=claude-sonnet-5, status=healthy

# 3. 如果仍失败，检查 base_url 和 apiKey
SELECT base_url, secret_ciphertext FROM credentials WHERE id = 130;
```

### 问题 2：强制启用后仍显示警告

**检查：**
```bash
# 1. 确认 URSM v2 已启用
echo $LLM_GATEWAY_URSM_V2_ENABLED
# 应为 true

# 2. 确认 Redis 可达
redis-cli ping
# 应返回 PONG

# 3. 查看网关日志中的 URSM 错误
grep "ursm.v2" logs/gateway.log | grep -i error | tail -10

# 4. 检查 tenant_id 是否匹配
SELECT id, tenant_id FROM credentials WHERE id = 130;
# 应返回非空且与 URSM 配置一致的 tenant_id
```

### 问题 3：节点状态未变化

**检查：**
```bash
# 1. 确认数据库已更新
SELECT manual_disabled, availability_state, circuit_state 
FROM credentials WHERE id = 130;

# 2. 确认缓存已失效
# 查看日志中是否有 "candidate cache cleared" 消息

# 3. 强制刷新前端
# 清除浏览器缓存并刷新页面
```

---

## 成功标准

### ✅ 验证通过

所有以下条件均满足时，修复验证通过：

1. ✅ `claude-sonnet-5` 会话 Ping 显示成功延迟（如 `350ms`）
2. ✅ 强制启用后显示"执行成功"，无"URSM 状态未清理"警告
3. ✅ 强制启用后节点立即变为绿色（可路由）
4. ✅ 刷新页面后节点仍保持绿色状态
5. ✅ OpenAI 协议节点的 Ping 不受影响
6. ✅ 单元测试全部通过（已确认）

### ❌ 需要进一步排查

如果出现以下情况之一，需要进一步排查：

- ❌ 会话 Ping 仍显示"失败：error"
- ❌ 强制启用仍显示"URSM 状态未清理"警告
- ❌ 强制启用后节点状态未变化
- ❌ 强制启用返回 500 但错误消息模糊（应明确说明 URSM/Redis 错误）

---

## 回滚方案

如果验证失败且影响生产环境：

```bash
# 回滚到修复前版本
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
git stash
git checkout e62f06ece  # 修复前的 commit
make build
pkill -f bin/gateway
./bin/gateway --config config/local.yaml &

# 或使用 git revert
git revert HEAD
make build
# 重启服务
```

---

## 修复文件清单

- `admin/node_operations.go` (+74 行)：会话 Ping 协议适配 + provider 级 URSM 同步
- `admin/node_operations_test.go` (+45 行)：anthropic-messages ping 测试
- `admin/routing.go` (+34 行)：URSM 失败阻断 + 模型枚举错误报告
- `CHANGELOG_20260906_PING_URSM_FIX.md`：完整修复文档
- `VERIFICATION_GUIDE.md`：本验证指南

---

**验证负责人：** ___________  
**验证时间：** ___________  
**验证结果：** ☐ 通过  ☐ 失败（原因：___________）

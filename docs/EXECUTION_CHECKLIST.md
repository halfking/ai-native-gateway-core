# minimax-m3 系统改进 - 执行清单

**目标**: 修复 request-logs 前端显示问题，并验证 minimax-m3 配置
**环境**: 仅生产环境 71 (llmgateway.internal.example.com)
**日期**: 2026-06-26

---

## 🎯 问题总结

### 1. ✅ minimax-m3 配置完成（测试环境）
- 所有数据库配置已添加
- 因缺少加密密钥，测试环境无法完全验证
- 需要在生产环境 71 上验证

### 2. 🔍 request-logs 前端显示问题
- 数据库有数据（已验证本地测试环境）
- 前端页面 `https://llmgateway.internal.example.com/request-logs` 看不到数据
- 需要诊断是 API 问题还是前端问题

---

## 📋 立即执行的操作

### 步骤 1: 诊断 request-logs 问题（5-10 分钟）

#### 1.1 在 71 上运行诊断脚本

```bash
# SSH 到生产环境 71
ssh llm-gateway-71

# 上传诊断脚本
scp scripts/diagnose_request_logs_71.sh llm-gateway-71:/tmp/

# 执行脚本
chmod +x /tmp/diagnose_request_logs_71.sh
/tmp/diagnose_request_logs_71.sh > /tmp/diagnosis_$(date +%Y%m%d_%H%M%S).log 2>&1

# 查看结果
cat /tmp/diagnosis_*.log
```

#### 1.2 关键检查点

脚本会检查：
- ✅ 数据库是否有最近 24 小时的数据
- ✅ tenant_id 分布情况
- ✅ 当前 admin 用户的角色和 tenant
- ✅ gateway 日志中的 audit 记录

#### 1.3 根据结果判断

**场景 A: 数据库有数据（count_24h > 0）**
→ 继续步骤 2：测试 API

**场景 B: 数据库无数据（count_24h = 0）**
→ 问题：gateway 没有写入 request_logs
→ 检查：
  - gateway 是否运行？
  - 数据库连接是否正常？
  - 是否有数据库写入错误？

---

### 步骤 2: 测试 /api/logs API（5 分钟）

#### 2.1 获取 Admin Token

```bash
# 方法 1: 从浏览器 Cookie 中获取
# 1. 访问 https://llmgateway.internal.example.com
# 2. 打开 DevTools -> Application -> Cookies
# 3. 找到 auth token

# 方法 2: 重新登录
curl -X POST https://llmgateway.internal.example.com/api/login \
  -H "Content-Type: application/json" \
  -d '{"username":"YOUR_USERNAME","password":"YOUR_PASSWORD"}' \
  | jq -r '.token'
```

#### 2.2 测试 API

```bash
# 设置 token
TOKEN="YOUR_ADMIN_TOKEN"

# 测试 1: 默认查询（最近 24 小时）
curl -s "https://llmgateway.internal.example.com/api/logs" \
  -H "Authorization: Bearer $TOKEN" \
  | jq '{count: .count, items_count: (.items | length)}'

# 测试 2: 最近 1 小时
FROM=$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)
TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl -s "https://llmgateway.internal.example.com/api/logs?from=$FROM&to=$TO" \
  -H "Authorization: Bearer $TOKEN" \
  | jq '{count: .count, items_count: (.items | length), first_item: .items[0]}'

# 测试 3: 最近 7 天
FROM=$(date -u -d '7 days ago' +%Y-%m-%dT%H:%M:%SZ)
TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl -s "https://llmgateway.internal.example.com/api/logs?from=$FROM&to=$TO" \
  -H "Authorization: Bearer $TOKEN" \
  | jq '{count: .count, items_count: (.items | length)}'
```

#### 2.3 根据结果判断

**场景 A: API 返回数据（count > 0, items 非空）**
→ 后端正常！问题在前端
→ 继续步骤 3：检查前端

**场景 B: API 返回 count > 0，但 items 为空数组**
→ 分页或 LIMIT/OFFSET 问题
→ 添加日志并重新部署

**场景 C: API 返回 count = 0**
→ 查询条件过滤掉了所有数据
→ 检查 tenant_id 过滤或时间范围

---

### 步骤 3: 检查前端问题（5 分钟）

#### 3.1 浏览器 DevTools 检查

1. 访问 `https://llmgateway.internal.example.com/request-logs`
2. 打开 Chrome DevTools (F12)
3. 切换到 **Network** 标签
4. 刷新页面
5. 找到 `/api/logs` 请求

**检查点**：
- 请求 URL 和参数（特别是 from/to）
- 响应状态码（应该是 200）
- 响应数据的 count 和 items

#### 3.2 Console 检查

切换到 **Console** 标签，查看是否有：
- JavaScript 错误
- 网络错误
- 认证错误

#### 3.3 常见前端问题

**问题 1: 时间格式错误**
- 前端传递的时间格式不正确
- 解决：修改前端代码，使用正确的 ISO 8601 格式

**问题 2: 时区问题**
- 前端使用本地时间，但后端期望 UTC
- 解决：统一使用 UTC 时间

**问题 3: 前端分页 bug**
- 前端没有正确处理分页数据
- 解决：修复前端分页逻辑

---

### 步骤 4: 验证 minimax-m3 配置（10 分钟）

在完成 request-logs 诊断后，验证 minimax-m3：

#### 4.1 检查配置

```bash
ssh llm-gateway-71
docker exec r112_postgres psql -U kxuser -d llm_gateway << 'SQL'
-- 1. 检查 provider_models
SELECT COUNT(*) AS provider_models_count
FROM provider_models
WHERE raw_model_name = 'minimax-m3';
-- 期望: 3

-- 2. 检查 model_offers
SELECT COUNT(*) AS model_offers_count
FROM model_offers
WHERE raw_model_name = 'minimax-m3';
-- 期望: N (N = 活跃 minimax 凭据数)

-- 3. 检查详细配置
SELECT 
    pm.id,
    p.code AS provider,
    pm.raw_model_name,
    pm.outbound_model_name,
    pm.canonical_id
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE pm.raw_model_name = 'minimax-m3';
SQL
```

#### 4.2 如果配置缺失

```bash
# 准备配置 SQL
cat > /tmp/minimax_m3_config.sql << 'SQL'
BEGIN;

-- 1. 添加 provider_models
INSERT INTO provider_models (provider_id, raw_model_name, outbound_model_name, canonical_id)
VALUES 
    (14, 'minimax-m3', 'MiniMax-M3', 5),
    (67, 'minimax-m3', 'MiniMax-M3', 5),
    (18, 'minimax-m3', 'minimaxai/minimax-m3', 5)
ON CONFLICT (provider_id, raw_model_name) DO UPDATE 
SET canonical_id = EXCLUDED.canonical_id,
    outbound_model_name = EXCLUDED.outbound_model_name;

-- 2. 添加 credential_model_bindings
INSERT INTO credential_model_bindings (credential_id, provider_model_id, routing_tier, weight, available)
SELECT 
    c.id,
    pm.id,
    1,
    100,
    true
FROM credentials c
JOIN providers p ON p.id = c.provider_id
CROSS JOIN provider_models pm
WHERE p.code IN ('minimax', 'minimax-anthropic', 'nvidia')
  AND pm.raw_model_name = 'minimax-m3'
  AND pm.provider_id = c.provider_id
  AND c.status = 'active'
ON CONFLICT (credential_id, provider_model_id) DO UPDATE
SET available = true;

COMMIT;
SQL

# 执行
docker exec -i r112_postgres psql -U kxuser -d llm_gateway < /tmp/minimax_m3_config.sql
```

#### 4.3 测试 minimax-m3

```bash
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_PROD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 20
  }'
```

#### 4.4 检查结果

```bash
# 检查路由决策
docker exec r112_postgres psql -U kxuser -d llm_gateway << 'SQL'
SELECT 
    request_id,
    ts,
    model,
    chosen_credential_id,
    candidates_tried
FROM routing_decision_log
WHERE model = 'minimax-m3'
ORDER BY ts DESC
LIMIT 3;
SQL

# 检查 request_logs
docker exec r112_postgres psql -U kxuser -d llm_gateway << 'SQL'
SELECT 
    request_id,
    ts,
    client_model,
    credential_id,
    request_status,
    success,
    error_kind
FROM request_logs
WHERE client_model = 'minimax-m3'
ORDER BY ts DESC
LIMIT 3;
SQL
```

**期望结果**：
- ✅ candidates_tried > 0
- ✅ chosen_credential_id 非空
- ✅ request_logs 有记录
- ✅ 成功调用上游或返回上游错误（非 no_candidate）

---

## 🔧 如果需要修复

### 修复 A: 添加后端调试日志

如果 API 行为异常，添加日志：

```go
// admin/logs.go
func (h *Handler) listLogs(w http.ResponseWriter, r *http.Request) {
    // ... 现有代码 ...
    
    slog.Info("listLogs called",
        "from", start,
        "to", end,
        "where_clauses", clauses,
        "is_tenant_admin", IsTenantAdmin(r))
    
    // ... 查询代码 ...
    
    slog.Info("listLogs result",
        "count", count,
        "items_count", len(items))
    
    // ... 返回结果 ...
}
```

重新编译并部署：
```bash
go build -o gateway-bin ./cmd/gateway
scp gateway-bin llm-gateway-71:/tmp/
ssh llm-gateway-71 "docker cp /tmp/gateway-bin production_gateway:/app/gateway && docker restart production_gateway"
```

### 修复 B: 扩大默认时间范围

如果数据较旧：

```go
// admin/logs.go:257
start := parseQueryTime(r, "from", now.Add(-7*24*time.Hour))  // 改为 7 天
```

---

## 📊 监控和验证

### 持续监控

```bash
# 监控 request_logs 插入
watch -n 5 'docker exec r112_postgres psql -U kxuser -d llm_gateway -c "SELECT COUNT(*) FROM request_logs WHERE ts > now() - interval '\''5 minutes'\''"'

# 监控 gateway 日志
docker logs -f production_gateway | grep "audit: request completed"
```

### 验证清单

- [ ] 数据库有最近数据（24小时内）
- [ ] `/api/logs` API 返回数据
- [ ] 前端页面显示数据
- [ ] minimax-m3 路由成功（candidates_tried > 0）
- [ ] minimax-m3 请求有 request_logs 记录

---

## 📞 需要帮助？

如果遇到问题，请提供以下信息：

1. **诊断脚本的输出**
```bash
cat /tmp/diagnosis_*.log
```

2. **API 测试结果**
```bash
curl -s "https://llmgateway.internal.example.com/api/logs" -H "Authorization: Bearer $TOKEN" | jq '.'
```

3. **浏览器 DevTools 截图**
   - Network 标签的 `/api/logs` 请求
   - Console 标签的错误信息

4. **Gateway 日志**
```bash
docker logs production_gateway --tail 100
```

---

## 📝 参考文档

- [完整根本原因分析](./MINIMAX_M3_ROOT_CAUSE_ANALYSIS.md)
- [request-logs 前端调试指南](./REQUEST_LOGS_FRONTEND_DEBUG.md)
- [改进和测试计划](./IMPROVEMENT_AND_TEST_PLAN.md)

---

**开始执行**: 请从步骤 1 开始，运行诊断脚本，然后根据结果继续后续步骤。

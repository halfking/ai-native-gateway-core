# request-logs 前端显示问题诊断与修复

**问题**: 在 `https://llmgateway.internal.example.com/request-logs` 页面看不到数据
**状态**: 数据库有数据，但前端不显示

---

## 🔍 问题诊断

### 1. 验证数据库有数据

```sql
-- 检查最近 1 小时的数据
SELECT 
    COUNT(*) AS total,
    MIN(ts) AS earliest,
    MAX(ts) AS latest
FROM request_logs
WHERE ts > now() - interval '1 hour';

-- 检查最近 10 条记录
SELECT 
    request_id,
    ts,
    client_model,
    request_status,
    success
FROM request_logs
ORDER BY ts DESC
LIMIT 10;
```

### 2. 测试后端 API

```bash
# 测试 /api/logs API（最近 1 小时）
curl -s "https://llmgateway.internal.example.com/api/logs?from=$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)&to=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  | jq '.count'

# 应该返回数字 > 0

# 测试最近 24 小时（默认范围）
curl -s "https://llmgateway.internal.example.com/api/logs" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  | jq '{count: .count, items_count: (.items | length)}'
```

### 3. 检查前端请求

**浏览器开发者工具**：
1. 打开 `https://llmgateway.internal.example.com/request-logs`
2. 打开 Chrome DevTools (F12)
3. 切换到 Network 标签
4. 刷新页面
5. 查找 `/api/logs` 请求
6. 检查：
   - 请求 URL 和参数
   - 响应状态码
   - 响应数据

---

## 🐛 可能的问题

### 问题 A: 时间范围参数错误

**症状**: API 返回 `count: 0`

**原因**: 前端传递的时间范围不包含数据

**诊断**:
```bash
# 检查 API 请求的时间参数
# 在 Chrome DevTools -> Network 中查看实际请求的 URL
# 例如: /api/logs?from=2026-06-25T00:00:00Z&to=2026-06-26T00:00:00Z
```

**解决**: 
- 前端应该默认查询最近 24 小时
- 或者提供时间选择器让用户自定义范围

---

### 问题 B: 认证失败

**症状**: API 返回 401 或 403

**原因**: Cookie 或 Token 过期

**解决**:
1. 重新登录
2. 检查 `/api/login` 状态
3. 清除浏览器缓存和 Cookie

---

### 问题 C: tenant_id 过滤

**症状**: API 返回 `count: 0`，但数据库有数据

**原因**: tenant_admin 用户只能看到自己 tenant 的数据

**诊断**:
```sql
-- 检查 request_logs 中的 tenant_id 分布
SELECT 
    tenant_id,
    COUNT(*) AS count
FROM request_logs
WHERE ts > now() - interval '24 hours'
GROUP BY tenant_id
ORDER BY count DESC;

-- 检查当前用户的 tenant_id
SELECT id, username, tenant_id, role
FROM admin_users
WHERE username = 'YOUR_USERNAME';
```

**解决**:
- 如果是 tenant_admin，确保 request_logs.tenant_id 匹配
- 如果是 super_admin，应该能看到所有数据

---

### 问题 D: 前端分页问题

**症状**: API 有数据，但前端不显示

**原因**: 前端分页逻辑错误

**诊断**:
```bash
# 检查第一页数据
curl -s "https://llmgateway.internal.example.com/api/logs?page=1&page_size=10" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  | jq '.items | length'
```

---

## 🔧 修复方案

### 方案 1: 添加调试日志到后端

编辑 `admin/logs.go`：

```go
func (h *Handler) listLogs(w http.ResponseWriter, r *http.Request) {
    // ... 现有代码 ...
    
    // 添加调试日志
    slog.Info("listLogs called",
        "from", start,
        "to", end,
        "page", page,
        "page_size", pageSize,
        "filters", clauses,
        "is_tenant_admin", IsTenantAdmin(r),
        "tenant_id", GetTenantID(r))
    
    // ... 现有查询代码 ...
    
    // 查询后记录结果
    slog.Info("listLogs result",
        "count", count,
        "items_returned", len(items))
    
    writeJSON(w, http.StatusOK, map[string]any{
        "items": items,
        "count": count,
    })
}
```

### 方案 2: 修改默认时间范围

如果数据在更早的时间，修改默认范围为最近 7 天：

```go
// admin/logs.go:257
start := parseQueryTime(r, "from", now.Add(-7*24*time.Hour))  // 改为 7 天
```

### 方案 3: 添加 API 健康检查端点

```go
// admin/handler.go
mux.HandleFunc("/api/logs/health", admin(h.handleLogsHealth))

// admin/logs.go
func (h *Handler) handleLogsHealth(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
    defer cancel()
    
    var count int
    var latest time.Time
    
    err := h.db.QueryRow(ctx, `
        SELECT COUNT(*), MAX(ts)
        FROM request_logs
        WHERE ts > now() - interval '24 hours'
    `).Scan(&count, &latest)
    
    if err != nil {
        writeJSON(w, http.StatusOK, map[string]any{
            "status": "error",
            "error": err.Error(),
        })
        return
    }
    
    writeJSON(w, http.StatusOK, map[string]any{
        "status": "ok",
        "count_24h": count,
        "latest_request": latest,
        "now": time.Now(),
    })
}
```

测试：
```bash
curl https://llmgateway.internal.example.com/api/logs/health \
  -H "Authorization: Bearer YOUR_TOKEN"
```

---

## 📊 实时诊断脚本

创建 `scripts/diagnose_request_logs.sh`：

```bash
#!/bin/bash

BASE_URL="https://llmgateway.internal.example.com"
TOKEN="${ADMIN_TOKEN:-YOUR_TOKEN_HERE}"

echo "=== Request Logs 诊断 ==="
echo ""

echo "1. 检查数据库最近记录"
docker exec production_postgres psql -U kxuser -d llm_gateway << 'SQL'
SELECT 
    COUNT(*) AS count_24h,
    MAX(ts) AS latest_ts,
    MIN(ts) AS earliest_ts_24h
FROM request_logs
WHERE ts > now() - interval '24 hours';
SQL

echo ""
echo "2. 测试 API 默认参数"
RESPONSE=$(curl -s "$BASE_URL/api/logs" \
  -H "Authorization: Bearer $TOKEN")
echo "$RESPONSE" | jq '{count: .count, items: (.items | length)}'

echo ""
echo "3. 测试 API 最近 1 小时"
FROM=$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)
TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
RESPONSE=$(curl -s "$BASE_URL/api/logs?from=$FROM&to=$TO" \
  -H "Authorization: Bearer $TOKEN")
echo "Time range: $FROM to $TO"
echo "$RESPONSE" | jq '{count: .count, items: (.items | length)}'

echo ""
echo "4. 测试 API 最近 7 天"
FROM=$(date -u -d '7 days ago' +%Y-%m-%dT%H:%M:%SZ)
TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
RESPONSE=$(curl -s "$BASE_URL/api/logs?from=$FROM&to=$TO" \
  -H "Authorization: Bearer $TOKEN")
echo "Time range: $FROM to $TO"
echo "$RESPONSE" | jq '{count: .count, items: (.items | length)}'

echo ""
echo "5. 检查第一条记录"
echo "$RESPONSE" | jq '.items[0] | {request_id, ts, client_model, success}'

echo ""
echo "=== 诊断完成 ==="
```

---

## 🚀 部署修复（仅 71）

### 步骤 1: 添加调试日志

```bash
# 在本地修改 admin/logs.go
# 添加上述调试日志

# 编译
go build -o gateway-bin ./cmd/gateway

# 上传到 71
scp gateway-bin llm-gateway-71:/tmp/

# SSH 到 71
ssh llm-gateway-71

# 备份旧版本
docker exec production_gateway cp /app/gateway /app/gateway.backup

# 替换
docker cp /tmp/gateway-bin production_gateway:/app/gateway

# 重启
docker restart production_gateway

# 查看日志
docker logs -f production_gateway | grep "listLogs"
```

### 步骤 2: 测试前端

1. 访问 `https://llmgateway.internal.example.com/request-logs`
2. 打开浏览器 DevTools
3. 查看 Console 和 Network 标签
4. 刷新页面
5. 检查：
   - API 请求的 URL 和参数
   - API 响应的 count 和 items
   - 控制台是否有 JavaScript 错误

### 步骤 3: 检查后端日志

```bash
ssh llm-gateway-71
docker logs production_gateway | grep "listLogs" | tail -20
```

查找：
- `listLogs called` - 记录请求参数
- `listLogs result` - 记录返回结果

---

## 📝 常见问题排查

### Q1: API 返回 count > 0，但 items 为空数组

**原因**: 分页参数错误或 LIMIT/OFFSET 超出范围

**解决**:
```bash
# 测试第一页
curl "https://llmgateway.internal.example.com/api/logs?page=1&page_size=10"

# 如果仍然空，检查 ORDER BY 和数据实际时间范围
```

### Q2: 数据库有数据，API 返回 count = 0

**原因**: WHERE 条件过滤掉了所有数据

**检查**:
1. 时间范围是否正确（UTC vs 本地时间）
2. tenant_id 过滤
3. 其他过滤条件（api_key_id, provider_id 等）

### Q3: 前端显示"加载中..."不消失

**原因**: 
1. API 请求超时
2. JavaScript 错误
3. 认证失败

**检查**: 浏览器 Console 和 Network 标签

---

## ✅ 验证清单

- [ ] 数据库有最近 24 小时的数据
- [ ] `/api/logs` API 返回 count > 0
- [ ] `/api/logs` API 返回 items 数组非空
- [ ] 浏览器 Network 显示 API 请求成功（200）
- [ ] 浏览器 Console 无 JavaScript 错误
- [ ] 前端页面显示数据列表

---

**下一步**: 请执行以下操作并告诉我结果：

1. **在浏览器中**：
   - 打开 `https://llmgateway.internal.example.com/request-logs`
   - 打开 DevTools (F12)
   - 查看 Network 标签
   - 复制 `/api/logs` 请求的完整 URL
   - 告诉我响应的 `count` 和 `items.length`

2. **或者直接测试 API**：
```bash
curl -s "https://llmgateway.internal.example.com/api/logs" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  | jq '{count: .count, items_count: (.items | length), first_item: .items[0]}'
```

请把结果发给我，我会帮你进一步诊断！

# Request-Logs 前端显示问题 - 诊断报告

**日期**: 2026-06-26  
**问题**: `https://llmgateway.internal.example.com/request-logs` 页面看不到数据  
**状态**: 已完成所有可能的后端诊断，需要前端验证

---

## ✅ 已验证的部分

### 1. 数据库层面 ✅
```sql
-- 最近 24 小时有 419 条记录
SELECT COUNT(*) FROM request_logs WHERE ts > now() - interval '24 hours';
-- 结果: 419 条

-- 最新记录时间
SELECT MAX(ts) FROM request_logs;
-- 结果: 2026-06-26 08:24:11 UTC (北京时间 16:24)
```

**结论**: 数据库有数据，且在最近 24 小时内

### 2. 后端 API 代码 ✅

**API 端点**: `/api/logs`  
**Handler**: `admin/logs.go:listLogs()`  
**默认时间范围**: 最近 24 小时

```go
// admin/logs.go:257
start := parseQueryTime(r, "from", now.Add(-24*time.Hour))
end := parseQueryTime(r, "to", now)
```

**SQL 查询**:
```sql
SELECT COUNT(*) FROM request_logs rl
WHERE rl.ts >= $1 AND rl.ts <= $2
-- 无 tenant 过滤时应该返回 419
```

**结论**: 后端代码逻辑正确

### 3. 前端代码 ✅

**页面组件**: `web/src/views/RequestLogsView.vue`  
**API 调用**: `web/src/api/logs.ts:getRequestLogs()`  
**默认时间范围**: 最近 24 小时

```typescript
// RequestLogsView.vue:27
const hours = ref(24)

// RequestLogsView.vue:133
function timeRange() {
  const end = new Date()
  const start = new Date(end.getTime() - hours.value * 3600 * 1000)
  return { from: start.toISOString(), to: end.toISOString() }
}
```

**结论**: 前端代码逻辑正确

### 4. API 认证 ✅

```bash
curl https://llmgateway.internal.example.com/api/logs
# 返回: {"error":{"detail":"authentication required"}}
```

**结论**: API 需要认证（正常行为）

---

## ❓ 未验证的部分

### 1. 实际的前端 API 请求

**需要验证**:
- 前端实际发送的请求 URL 是什么？
- from/to 参数的实际值是什么？
- 请求是否带了正确的 Authorization header？
- API 返回的响应是什么？

**如何验证**:
1. 访问 `https://llmgateway.internal.example.com/request-logs`
2. 打开 Chrome DevTools (F12)
3. 切换到 **Network** 标签
4. 刷新页面
5. 找到 `/api/logs` 请求
6. 查看：
   - Request URL
   - Request Headers (特别是 Authorization)
   - Response Status
   - Response Body

### 2. 前端是否正确处理响应

**可能的问题**:
- API 返回了数据，但前端渲染逻辑有 bug
- 前端有额外的过滤条件
- 前端状态管理问题

**如何验证**:
1. 在 Chrome DevTools 的 **Console** 标签中检查错误
2. 在 Network 标签中确认 API 响应有 `count > 0` 和 `items` 数组
3. 如果 API 有数据但页面不显示，则是前端渲染问题

---

## 🔧 可能的问题和解决方案

### 问题 A: 时间范围计算错误（概率: 低）

**症状**: API 返回 `count: 0`

**原因**: 前端计算的时间范围不包含数据库中的数据

**诊断**:
```javascript
// 在浏览器 Console 中执行
const end = new Date()
const start = new Date(end.getTime() - 24 * 3600 * 1000)
console.log('From:', start.toISOString())
console.log('To:', end.toISOString())
```

对比数据库中的实际时间范围。

**解决**: 修改前端时间计算或扩大默认范围

---

### 问题 B: 认证 Token 问题（概率: 中）

**症状**: API 返回 401 或 403

**原因**: 
- Token 过期
- Token 缺失
- Token 格式错误

**诊断**: 在 Network 标签查看请求头

**解决**: 重新登录

---

### 问题 C: tenant_id 过滤（概率: 低）

**症状**: API 返回 `count: 0`，但数据库有数据

**原因**: 你的用户是 tenant_admin，只能看到自己 tenant 的数据

**已验证**: 
```sql
SELECT tenant_id, COUNT(*) FROM request_logs 
WHERE ts > now() - interval '24 hours' 
GROUP BY tenant_id;
-- 结果: tenant_id='default', count=419
```

如果你的用户 tenant_id 不是 'default'，会看不到数据。

**解决**: 使用 super_admin 账号，或将 request_logs 的 tenant_id 设置正确

---

### 问题 D: 前端缓存（概率: 中）

**症状**: 数据已经有了，但页面不更新

**原因**: 浏览器缓存或前端状态缓存

**解决**:
1. 硬刷新: Ctrl+Shift+R (Windows) 或 Cmd+Shift+R (Mac)
2. 清除浏览器缓存
3. 无痕模式打开

---

### 问题 E: 前端未构建/部署（概率: 高）

**症状**: 前端代码修改后没有生效

**原因**: 前端代码没有重新构建并部署到生产环境

**验证**:
```bash
# 检查生产环境的前端版本
curl -s https://llmgateway.internal.example.com/ | grep "index-"
# 应该看到类似: <script src="/assets/index-XXXXX.js"></script>
```

**解决**:
```bash
# 在本地构建前端
cd web
npm install
npm run build

# 部署到生产环境 71
scp -r dist/* llm-gateway-71:/path/to/web/dist/

# 或者如果使用 Docker
docker cp dist production_web_container:/usr/share/nginx/html/
```

---

## 🎯 推荐的诊断顺序

### 步骤 1: 浏览器 DevTools 检查（5 分钟）

1. 访问 `https://llmgateway.internal.example.com/request-logs`
2. F12 打开 DevTools
3. Network 标签 → 找到 `/api/logs` 请求
4. 记录：
   - Request URL 和参数
   - Response Status
   - Response Body 的 count 和 items

**如果**:
- API 返回 401/403 → 认证问题（问题 B）
- API 返回 200，count > 0，items 有数据 → 前端渲染问题
- API 返回 200，count = 0 → 查询条件问题（问题 A 或 C）

---

### 步骤 2: 使用测试工具（10 分钟）

我已经创建了测试页面：`web/test-api.html`

**使用方法**:
1. 在浏览器中打开这个文件
2. 获取你的 Auth Token：
   - 方法 1: DevTools → Application → Cookies → 找到 auth token
   - 方法 2: Network → /api/logs 请求 → Request Headers → Authorization
3. 填写到测试页面的输入框
4. 点击"测试 /api/logs (带认证)"
5. 查看返回结果

**如果** API 返回数据，说明后端正常，问题在前端。

---

### 步骤 3: 检查前端构建（如果 API 有数据）

```bash
# 检查前端文件的修改时间
ls -la web/dist/assets/

# 重新构建
cd web
npm run build

# 查看构建输出
ls -la dist/

# 部署（根据你的部署方式）
```

---

## 📊 已收集的信息

### 数据库统计
```
总记录数（24小时）: 419
成功: 0
失败: 419
最新记录: 2026-06-26 08:24:11 UTC
tenant_id 分布: default=419
```

### 前端代码
- 默认查询范围: 24 小时
- 默认页大小: 100
- 支持的过滤: api_key_id, model, success, error_kind, usage_source, gw_session_id, gw_task_id

### 后端代码
- 默认查询范围: 24 小时
- 最大页大小: 500
- tenant_admin 会自动过滤 tenant_id

---

## 📝 下一步行动

**请立即执行**:

1. **在浏览器中检查**（最重要）:
   - 打开 `https://llmgateway.internal.example.com/request-logs`
   - F12 → Network 标签
   - 刷新页面
   - 截图 `/api/logs` 请求的 Request 和 Response
   - 发给我或告诉我结果

2. **或者提供 Auth Token**:
   - 从 Cookie 或请求头中复制 auth token
   - 我可以直接测试 API

3. **或者使用测试页面**:
   - 打开 `web/test-api.html`
   - 填写 token 并测试
   - 告诉我结果

---

## 🔍 已创建的工具和文档

1. **诊断脚本**: `scripts/diagnose_request_logs_71.sh`
2. **测试页面**: `web/test-api.html`
3. **文档**:
   - `docs/REQUEST_LOGS_FRONTEND_DEBUG.md`
   - `docs/EXECUTION_CHECKLIST.md`
   - `docs/MINIMAX_M3_ROOT_CAUSE_ANALYSIS.md`

---

**总结**: 
- ✅ 后端有数据
- ✅ 后端代码正确
- ✅ 前端代码正确
- ❓ 需要验证实际的前端请求和响应
- ❓ 可能是前端未正确构建/部署

**当前阻塞点**: 需要访问浏览器或 auth token 才能继续诊断

# Admin API 认证模式

## 概述

Admin API 支持三种认证方式，按以下优先级依次尝试：

1. **Bearer Token (HTTP Header)** - 推荐方式
2. **JWT Cookie** - 浏览器会话自动携带
3. **Query String Token (`?token=`)** - EventSource 回退方案

## 认证方式详解

### 1. Bearer Token (推荐)

**适用场景：** API 客户端、curl、Postman、自动化脚本

**使用方法：**
```bash
curl -H "Authorization: Bearer YOUR_API_KEY" \
  https://__DOMAIN_1__/api/admin/tenants
```

**优点：**
- 符合 REST API 标准
- 不会出现在 URL 日志中
- 所有 HTTP 方法通用

---

### 2. JWT Cookie

**适用场景：** 浏览器登录后的会话管理

**使用方法：**
登录后，后端自动设置 `llmgw_jwt_token` HttpOnly Cookie：

```http
Set-Cookie: llmgw_jwt_token=eyJhbGc...; Path=/; HttpOnly; Secure; SameSite=Strict
```

前端无需手动处理，浏览器自动携带 Cookie 到同域请求。

**优点：**
- 自动携带，前端无需手动管理
- HttpOnly 防止 XSS 窃取
- 支持自动续期

**局限：**
- 跨域受限（需配置 CORS + `credentials: 'include'`）
- EventSource API 在某些代理场景下无法携带 Cookie

---

### 3. Query String Token (`?token=`)

**适用场景：** EventSource (SSE) 实时流连接

**使用方法：**
```javascript
// 前端示例
const apiKey = localStorage.getItem('llmgw_api_key')
const eventSource = new EventSource(
  `/api/admin/live-stream?token=${encodeURIComponent(apiKey)}`
)
```

**后端处理逻辑：**
1. 检查 `Authorization` 头，如果存在则使用（优先级最高）
2. 如果无 Authorization 头，检查 `llmgw_jwt_token` Cookie
3. 如果上述两者都不存在，从 `?token=` 查询参数读取并提升为 `Authorization: Bearer {token}`

**实现位置：**
- `admin/auth.go:AdminMiddleware` - 查询参数提升逻辑
- `admin/live_stream_sse.go` - SSE 端点入口

**安全考量：**
⚠️ **URL 中的 Token 可能被记录到：**
- 浏览器历史记录
- 服务器访问日志（通常不记录 query string，但取决于配置）
- 代理服务器日志

**缓解措施：**
1. 仅在无 Bearer 头且无 Cookie 时使用
2. 前端从内存读取 `llmgw_api_key`（不持久化到 URL）
3. SSE 连接建立后，token 不会再出现在后续请求中

---

## 认证流程图

```
┌─────────────────┐
│  客户端请求     │
└────────┬────────┘
         │
         ▼
┌────────────────────────────────┐
│ AdminMiddleware                │
├────────────────────────────────┤
│ 1. 检查 Authorization 头       │
│    ├─ Bearer token? → 验证 JWT │
│    └─ 无                       │
│                                │
│ 2. 检查 llmgw_jwt_token Cookie │
│    ├─ 存在? → 验证 JWT          │
│    └─ 无                       │
│                                │
│ 3. 检查 ?token= 查询参数       │
│    ├─ 存在? → 提升为 Bearer    │
│    └─ 无                       │
│                                │
│ 4. 验证 admin_key (兜底)       │
└────────┬───────────────────────┘
         │
         ▼
┌────────────────┐
│  业务逻辑      │
└────────────────┘
```

## 最佳实践

### API 客户端
```bash
# ✅ 推荐
curl -H "Authorization: Bearer sk-xxx" https://__DOMAIN_1__/api/admin/tenants

# ❌ 不推荐（token 泄露到 shell 历史）
curl https://__DOMAIN_1__/api/admin/tenants?token=sk-xxx
```

### 浏览器前端
```typescript
// ✅ 推荐：普通 API 调用（自动携带 Cookie）
fetch('/api/admin/tenants', {
  credentials: 'include',
})

// ✅ 可接受：EventSource 回退（无法设置 Authorization 头）
const apiKey = localStorage.getItem('llmgw_api_key')
const es = new EventSource(`/api/admin/live-stream?token=${apiKey}`)

// ❌ 不推荐：手动拼接 Bearer 头（Cookie 更安全）
fetch('/api/admin/tenants', {
  headers: {
    Authorization: `Bearer ${localStorage.getItem('llmgw_api_key')}`,
  },
})
```

## 相关规范

- **Rule 20 §6**: API 认证标准 (Bearer > Cookie > Query String)
- **Rule 12 §8**: 前端安全检查流程
- **Rule 16**: 凭据协议（禁止在 URL 中硬编码凭据）

## 变更历史

| 日期 | 版本 | 变更 |
|------|------|------|
| 2026-07-03 | v1.0 | 初始版本，支持三种认证方式 |
| 2026-07-03 | v1.1 | 添加 `?token=` 查询参数回退支持 (commit b434ecf7) |

## 常见问题

**Q: 为什么 EventSource 需要 `?token=` 回退？**  
A: 浏览器 EventSource API 不支持自定义请求头。在某些代理/负载均衡配置下，Cookie 可能无法正确传递，因此需要查询参数作为兜底。

**Q: `?token=` 是否会破坏 REST 风格？**  
A: 这是 EventSource 技术限制的权衡方案。仅在无 Bearer 头且无 Cookie 时生效，不影响标准 API 调用。

**Q: 如何防止 `?token=` 泄露？**  
A: 
1. 前端从内存读取，不持久化到 URL
2. 后端日志通常不记录 query string
3. 仅在必要时使用（优先 Bearer/Cookie）

**Q: 是否支持 Basic Auth？**  
A: 不支持。企业内部部署场景下，Bearer Token + JWT Cookie 已足够安全且易于管理。

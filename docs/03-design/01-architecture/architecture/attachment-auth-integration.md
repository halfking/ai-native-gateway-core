# 附件访问认证集成 (Attachment Access Authentication)

## 概述

从 2026-08-09 开始，LLM Gateway 支持多种附件访问认证模式，允许客户端使用相同的 API Key 访问聊天接口和附件下载。

## 认证模式

### 1. **none**（默认，无认证）
- 所有附件公开访问
- 仅依赖路径遍历防护
- 适用场景：内网部署、测试环境

### 2. **apikey**（推荐）
- 客户端使用相同的 Bearer token 访问附件
- 复用网关的 API Key 验证机制
- 适用场景：生产环境、多租户 SaaS

### 3. **signed**（待实现）
- 使用 HMAC 签名的临时链接
- 适用场景：session 附件、短期分享

### 4. **admin**（待实现）
- 使用 Admin session cookie
- 适用场景：管理后台

## 快速开始

### 1. 启用 API Key 认证

在 `.env` 文件中配置：

```bash
# 启用 API Key 认证
LLM_GATEWAY_ATTACHMENT_AUTH_MODE=apikey

# (可选) 配置 CDN URL
LLM_GATEWAY_ATTACHMENT_PUBLIC_URL=https://cdn.example.com/attachments

# (可选) 配置 CORS
LLM_GATEWAY_ATTACHMENT_CORS_ORIGINS=https://app.example.com,https://admin.example.com
```

### 2. 客户端调用

```bash
# 1. 聊天请求（带图片）
curl -X POST https://api.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-api-key-12345" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "描述这张图片"},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
      ]
    }]
  }'

# 2. 下载附件（使用相同的 API Key）
curl https://api.example.com/api/attachments/2026/08/req_xxx/abc.png \
  -H "Authorization: Bearer sk-your-api-key-12345" \
  -o downloaded.png
```

## 架构说明

### 组件关系

```
┌─────────────────────────────────────────────────────────────┐
│                     Client Request                           │
│  Authorization: Bearer sk-xxx                                │
└────────────────────────┬────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────┐
│               domains/attachments/Handler                    │
│  ┌────────────────────────────────────────────────────┐     │
│  │  1. ServeHTTP                                      │     │
│  │  2. Authenticator.Authenticate()                   │     │
│  │  3. Storage.OpenStream()                           │     │
│  └────────────────────────────────────────────────────┘     │
└────────────────────────┬────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────┐
│          domains/authentication/KeyVerifier                  │
│  ┌────────────────────────────────────────────────────┐     │
│  │  1. Verify(rawKey) → KeyInfo                       │     │
│  │  2. Cache (60s TTL)                                │     │
│  │  3. DB Query (api_keys table)                      │     │
│  └────────────────────────────────────────────────────┘     │
└─────────────────────────────────────────────────────────────┘
```

### 认证流程

1. **请求到达** → `Handler.ServeHTTP`
2. **提取 Token** → `Authorization: Bearer <token>`
3. **验证 API Key** → `KeyVerifier.Verify(token)`
   - 检查缓存（60s TTL）
   - 查询数据库 `api_keys` 表
   - 验证状态（active / disabled）
4. **提取附件路径** → `/api/attachments/2026/08/req_xxx/abc.png`
5. **路径安全检查** → `Storage.OpenStream` → `LocalBackend.getFilePath`
   - 拒绝 `../` 遍历
   - 确保路径在 `baseDir` 内
6. **流式传输** → `io.Copy(w, file)`

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `LLM_GATEWAY_ATTACHMENT_AUTH_MODE` | `none` | 认证模式：none/apikey/signed/admin |
| `LLM_GATEWAY_ATTACHMENT_PUBLIC_URL` | `/api/attachments/` | 公开访问 URL 前缀（CDN） |
| `LLM_GATEWAY_ATTACHMENT_CORS_ORIGINS` | `` | CORS 允许的来源（逗号分隔） |

## 安全特性

### 1. 路径遍历防护（2026-08-09 修复）

所有附件路径经过三层防护：
- 拒绝绝对路径 (`/etc/passwd`)
- `filepath.Clean` 规范化
- 确保解析后的路径在 `baseDir` 内

### 2. API Key 验证

- 缓存 60 秒，减少数据库压力
- 检查 key 状态（active / disabled）
- 支持 tenant 隔离

### 3. 审计日志

所有认证失败请求记录审计日志：
```json
{
  "level": "WARN",
  "msg": "attachments: authentication failed",
  "path": "/api/attachments/test.png",
  "error": "invalid api key",
  "remote_addr": "1.2.3.4:5678"
}
```

## 测试

### 单元测试

```bash
# 认证器测试
go test ./domains/attachments/ -run TestAuthenticator

# 集成测试
go test ./domains/attachments/ -run TestHandler_WithAPIKeyAuth

# 完整测试套件
go test ./domains/attachments/
```

### 手动测试

```bash
# 1. 启动网关（本地测试）
export LLM_GATEWAY_ATTACHMENT_AUTH_MODE=apikey
go run ./cmd/gateway

# 2. 创建测试附件
mkdir -p data/attachments
echo "test content" > data/attachments/test.txt

# 3. 测试无认证访问（应返回 401）
curl http://localhost:8080/api/attachments/test.txt

# 4. 测试有效 API Key（应返回 200）
curl http://localhost:8080/api/attachments/test.txt \
  -H "Authorization: Bearer sk-your-valid-key"

# 5. 测试路径遍历（应返回 404）
curl http://localhost:8080/api/attachments/../../../etc/passwd \
  -H "Authorization: Bearer sk-your-valid-key"
```

## 部署检查清单

### Phase 1：测试环境（245）

- [ ] 部署新版本到 245
- [ ] 验证默认行为（`AUTH_MODE=none`）
- [ ] 启用 `AUTH_MODE=apikey`
- [ ] 测试聊天 + 附件下载流程
- [ ] 检查审计日志

### Phase 2：生产环境

- [ ] 灰度发布（10% 流量）
- [ ] 监控错误率和延迟
- [ ] 全量发布
- [ ] 通知客户端更新（如需）

## 常见问题

### Q1: 启用 API Key 认证后，现有客户端会受影响吗？

**A:** 不会。默认为 `AUTH_MODE=none`，需要显式配置才启用。建议先在测试环境验证，再逐步迁移生产环境。

### Q2: API Key 验证有缓存吗？会不会影响性能？

**A:** 有缓存（60 秒 TTL）。首次验证查询数据库，后续请求直接走缓存，对性能影响可忽略（< 1ms）。

### Q3: 如何配置 CDN？

**A:** 配置 `LLM_GATEWAY_ATTACHMENT_PUBLIC_URL=https://cdn.example.com/attachments`，网关会在响应中返回 CDN 链接。CDN 需要配置回源到网关的 `/api/attachments/` 路径。

### Q4: CORS 如何配置？

**A:** 配置 `LLM_GATEWAY_ATTACHMENT_CORS_ORIGINS=https://app.example.com`。多个来源用逗号分隔。`*` 表示允许所有来源（不推荐生产环境）。

## 相关文档

- [路径遍历漏洞修复](https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go/commit/4639d4ae5)
- [Modality 推断引擎重构](https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go/commit/4639d4ae5)
- [环境变量配置](.env.example)

## Changelog

### 2026-08-09
- ✅ 实现 API Key 认证模式
- ✅ 创建适配器解耦 authentication 包
- ✅ 添加环境变量配置支持
- ✅ 完整测试覆盖（单元 + 集成）
- ✅ 更新 .env.example 文档
- 🔄 待实现：signed 模式（HMAC 签名链接）
- 🔄 待实现：admin 模式（session cookie）
- 🔄 待实现：CORS 中间件

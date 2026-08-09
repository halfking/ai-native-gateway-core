# 2026-08-09 — 附件访问 API Key 认证集成 + 245 预生产部署脚本

## 背景

客户端访问聊天接口与附件下载使用两套不同认证机制，集成成本高、体验割裂。
本次为附件访问引入统一的 API Key 认证：客户端用**同一个 Bearer token** 即可调用
聊天接口和下载附件，消除分离认证。

## 功能

### 1. 多模式认证框架（4 模式）

| 模式 | 状态 | 说明 |
|---|---|---|
| `none` | ✅ 默认 | 公开访问（仅依赖路径遍历防护），向后兼容 |
| `apikey` | ✅ 已实现 | Bearer token，复用网关 KeyVerifier |
| `signed` | 🔄 TODO (Phase 3) | HMAC 签名临时链接，用于 session 附件 |
| `admin` | 🔄 TODO (Phase 3) | Admin session cookie，用于管理后台 |

### 2. API Key 认证（apikey 模式）

- 复用 `domains/authentication.KeyVerifier`（60s 缓存 + `api_keys` 表查询 + 状态校验）
- `KeyInfo.TenantID` 支持租户隔离
- 所有认证失败记录审计日志（WARN + path + remote_addr）
- `attachments` 包定义最小 `KeyVerifier` 接口 + `keyVerifierAdapter` 适配器解耦，避免与 authentication 包循环依赖

### 3. 环境变量

- `LLM_GATEWAY_ATTACHMENT_AUTH_MODE`：`none`/`apikey`/`signed`/`admin`，默认 `none`
- `LLM_GATEWAY_ATTACHMENT_PUBLIC_URL`：CDN 公开 URL 前缀（可选）
- `LLM_GATEWAY_ATTACHMENT_CORS_ORIGINS`：CORS 允许来源，逗号分隔（可选）

### 4. 降级安全网

`apikey` 模式配置但 KeyVerifier 不可用时，**降级为无认证并记录告警**，不阻断存量请求。

## 改动清单

| 文件 | 说明 |
|---|---|
| `domains/attachments/auth.go` | 认证器（4 模式分发 + apikey 实现） |
| `domains/attachments/auth_adapter.go` | authentication.KeyVerifier → 最小接口适配器 |
| `domains/attachments/config.go` | 环境变量配置加载 |
| `domains/attachments/handler.go` | `SetAuthenticator` + ServeHTTP 认证检查 |
| `domains/attachments/auth_test.go` | 单元测试（mock KeyVerifier） |
| `domains/attachments/handler_auth_integration_test.go` | 集成测试（端到端 apikey） |
| `cmd/gateway/main.go` | 认证配置注入 + attachment handler 装配 |
| `.env.example` | 新环境变量文档 |
| `docs/attachment-auth-integration.md` | 集成文档（快速开始/架构/安全/FAQ/部署清单） |
| `scripts/deploy-245.sh` | 245 预生产自动化部署脚本 |
| `scripts/verify-245.sh` | 245 验证脚本（4 组：healthz / 路径遍历 / apikey / 模态路由） |
| `docs/deployment-245-guide.md` | 245 部署指南 |

## 验证

- `go build ./...`：OK
- `go test ./domains/attachments/`：47 个测试全过（含 7 个新认证测试）
- 向后兼容：默认 `AUTH_MODE=none`，既有客户端零影响

## 部署路径

- **Phase 1（245 测试环境）**：默认 `none`，验证后启用 `apikey`
- **Phase 2（生产）**：灰度 → 全量，通知客户端使用同一 Bearer token

## 遗留

- `signed` / `admin` 模式待 Phase 3 实现（`auth.go` 内 TODO）
- CORS 中间件待实现（`LLM_GATEWAY_ATTACHMENT_CORS_ORIGINS` 已配置但未接线中间件）
- handler.go 认证成功后 `authCtx.TenantID` / `KeyPrefix` 审计日志落库为 TODO

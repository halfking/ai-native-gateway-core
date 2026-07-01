// Package admin — Rule 20 SSOT: 认证鉴权核心参数事实标准
//
// 本文件是 llm-gateway-go 的 Single Source of Truth 参数文件。
// 所有 rule 20 的核心常量、环境变量名、cookie 属性、JWT 约束
// 都集中在此。任何开发者如需修改、查询、审计 auth 参数，
// 应以本文件内容为准。
package admin

import "time"

// ── Cookie 参数 (rule 20 §6.1) ─────────────────────────────────────────────
//
// 与前端 web/src/api/_core.ts 的 `credentials: 'same-origin'` 以及
// web (浏览器 DevTools → Application → Cookies) 看到的 cookie 名字严格一致。

const (
	// CookieName 是 HttpOnly session cookie 的名字，服务端 Set-Cookie 时
	// 使用此名字写入，前端 fetch 中浏览器自动携带同名 cookie。
	//
	// 此值必须与 web/src/api/_core.ts 中预期的一致（虽然前端 JS 无法
	// 通过 document.cookie 读取 HttpOnly cookie）。
	// 审计命令: grep -r 'llmgw_session' web/src/
	CookieName = "llmgw_session"

	// CookieSameSite 始终为 StrictMode（rule 20 §6.1 铁律）：
	//   同一站点内部请求自动携带；跨站 POST/GET 不发送。
	//   禁止改为 Lax/None（确保 CSRF 防护 + 后端策略唯一控制权）。
	// 审计命令: grep -n 'SameSite' admin/auth_cookie_helpers.go
	CookieSameSite = "Strict"

	// CookieSecure 判定逻辑（实现见 cookieSecure() 函数）：
	//   env LLM_GATEWAY_COOKIE_SECURE 若设 → 用它；
	//   未设且 loopback (localhost/127.0.0.1) → false；
	//   未设且非 loopback → true (production)。
	// 审计命令: grep -n 'cookieSecure' admin/auth_cookie_helpers.go

	// CookieMaxAge 与 JWT 默认 TTL 一致（24h, rule 20 §5）。
	// JWT TTL 定义在: admin/jwt.go → jwtExpiry() → 24 * time.Hour
	//
	// 此值决定:
	//   1. Set-Cookie: Max-Age=<值>（秒）
	//   2. 前端 cookie 持久化时长（浏览器自动管理）
	//
	// 修改时必须同步修改 jwtExpiry() 的默认返回值，否则 cookie 会在
	// JWT 过期后仍被浏览器发送，导致 401。
	// 审计命令: grep -n 'sessionCookieMaxAge\|24 \* time.Hour' admin/auth_cookie_helpers.go admin/jwt.go
	CookieMaxAge = 24 * time.Hour

	// CookiePath 始终为 "/"，确保所有路径的 API 请求都携带 cookie。
	CookiePath = "/"
)

// ── 环境变量名 (rule 20 §8) ────────────────────────────────────────────────
//
// 生产环境 fail-closed 守卫需要这三个 env 都设。
// 参考: config/config.go → ValidateAuthSecrets()

const (
	EnvAPIKey       = "LLM_GATEWAY_API_KEY"       // 数据面静态门
	EnvAdminToken   = "LLM_GATEWAY_ADMIN_API_KEY" // ops 端点
	EnvJWTSecret    = "LLM_GATEWAY_JWT_SECRET"    // JWT 签名密钥
	EnvSecretKey    = "LLM_GATEWAY_SECRET_KEY"    // JWT 回退密钥
	EnvCookieSecure = "LLM_GATEWAY_COOKIE_SECURE" // cookie Secure flag 覆盖
	EnvJWTExpiry    = "LLM_GATEWAY_JWT_EXPIRY"    // JWT TTL 覆写
	EnvDeployEnv    = "LLM_GATEWAY_ENV"           // 部署环境 (production → fail-closed)
	EnvCORSOrigins  = "LLM_GATEWAY_CORS_ORIGINS"  // 跨域源 allowlist
)

// ── JWT 参数 (rule 20 §5) ──────────────────────────────────────────────────
//
// JWT claims 组成（见 admin/jwt.go → JWTClaims）：
//   只含 user_id, tenant_id, username, role, must_change_password
//   加上标准注册字段 exp, iat, iss
//   禁止塞入权限列表、角色矩阵、组织树（运行时按 user_id 查 DB）。

const (
	// JWTDefaultTTL 是 JWT 的默认有效期。变更时必须同步 CookieMaxAge
	// 对应的 time.Duration 值，确保两者一致。
	// 审计命令: grep '24 \* time.Hour' admin/jwt.go admin/auth_cookie_helpers.go
	JWTDefaultTTL = 24 * time.Hour

	// JWTMaxBytes 是 JWT token 的体积上限（rule 20 §5）。
	// 标准 JWT 约 290-320 字节（取决于 tenant_id/username 长度）。
	// 如果超过此值，请检查是否误塞了业务数据（权限/角色列表等）。
	JWTMaxBytes = 800

	// JWTIssuer 统一签发者标识。
	JWTIssuer = "llm-gateway"

	// JWTSigningMethod 统一为 HS256（对称签名，签名段最短 ~43 字节）。
	// 禁止换用 RS256/ES256 等非对称算法（除非上游强制要求）。
	JWTSigningMethod = "HS256"
)

// ── 秘钥前缀校验 (rule 20 §3) ──────────────────────────────────────────────
//
// 三种凭证的固定前缀，用于快速拒绝不属于本调用面的凭证，
// 避免跨面复用（如 JWT 被传给数据端点、sk- 传给管理端点）。

const (
	// KeyPrefixAPIKey 是数据面 API Key 的前缀。所有合法的应用 Key
	// 都必须以 "sk-" 开头。
	// 拒绝非 sk- 前缀的位置: domains/authentication/verifier.go → Verify()
	KeyPrefixAPIKey = "sk-"

	// KeyPrefixJWT 是管理面 JWT 的前缀（JWT header 的 base64url）。
	// 非强制校验，用于审计日志快速分类。
	KeyPrefixJWT = "eyJ"

	// KeyPrefixOps 是 ops 端点的推荐前缀。仅软校验（启动时若
	// LLM_GATEWAY_ADMIN_API_KEY 不带此前缀会打 slog.Warn 但不阻断）。
	// 审计命令: grep -n 'ops_' cmd/gateway/main.go
	KeyPrefixOps = "ops_"
)

// ── HTTP 头名称 ────────────────────────────────────────────────────────────
//
// 认证头：统一用 Authorization: Bearer <token>
// 路由/上下文头：不参与鉴权，仅影响路由决策（rule 20 §7）

const (
	HeaderAuthorization = "Authorization"       // 认证头 (Bearer <token>)
	HeaderDeviceSeed    = "X-Device-Seed"       // 会话粘性路由（可选）
	HeaderGwTaskID      = "X-Gw-Task-Id"        // 任务路由（可选）
	HeaderVirtualCID    = "X-Virtual-Client-Id" // 出向虚拟身份（服务端注入）
	HeaderVirtualIP     = "X-Virtual-IP"        // 出向虚拟 IP（服务端注入）
	HeaderVirtualMAC    = "X-Virtual-MAC"       // 出向虚拟 MAC（服务端注入）
)

// ── HTTP 状态码 ─────────────────────────────────────────────────────────────
//
// 认证失败统一用 401 + JSON body。
// 参考 middleware/auth_mw.go, admin/auth.go

const (
	StatusAuthError   = 401 // 认证失败（缺失/过期/无效）
	StatusForbidden   = 403 // 鉴权失败（权限不足，如 must_change_password）
	StatusRateLimited = 429 // 登录限速（5 次/分钟/IP）
)

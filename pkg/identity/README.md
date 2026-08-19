# identity-go

> 跨项目（RedClaw / openpocket / acc-go / memora / llm-gateway-go / ai-session-manager）统一身份共享库。
> 不引入 IAM 镜像；用 **HS256 共享密钥 + 多 issuer 白名单**做"一处登录、跨项目互信"。

## 设计目标

2. **跨项目互信**：RedClaw、openpocket、acc-go、memora 和 llm-gateway-go 使用共享对称密钥签发/校验各自 issuer 的 JWT；每个项目有独立 `iss` 字符串标识。
3. **llm-gateway-go / ai-session-manager 用户契约**：llm-gateway-go 是该产品线的唯一用户/tenant/role 身份源；ai-session-manager 只消费 gateway 签发的用户上下文和服务 scope，不创建独立用户、tenant、issuer 或 shadow provider。
4. **影子表 (shadow table)**：跨项目身份由 `(provider, subject, tenant_id)` 三元组映射到统一 `shadow_user_id`；provider 不包含 `asm`。
5. **Claims 字段不强行统一**：保留各项目原有 `user_id`/`sub`/`workspace_id`/`isAdmin` 字段；通过 `Extra map[string]any` 透传。

## 模块结构

```
identity-go/
├── token/             # JWT 签发 + 多 issuer 验证
│   ├── claims.go
│   ├── issuer.go
│   ├── sign.go
│   └── verify.go
├── shadow/            # 影子表 DAO
│   ├── model.go
│   ├── dao.go
│   └── migrations/    # 3 个 up/down 对
└── cmd/identity-migrate/  # CLI: 创建 db + 应用 migrations
```

## 接入指南

### 1) 在 `go.mod` 加 replace（每个项目）

```go
require github.com/kaixuan/identity-go v0.0.0
replace github.com/kaixuan/identity-go => ../pkg/identity-go
```

### 2) 配置 env

每个项目 `.env` 加：

```bash
IDENTITY_SHARED_SECRET=<32+ bytes, 与其他项目完全一致>
IDENTITY_ISSUER_ALLOWLIST=redclaw.auth-agent,memora,llm-gateway,pocket,acc
IDENTITY_SHADOW_DSN=postgres://kxuser:kxpass@host.docker.internal:5432/identity_shadow?sslmode=verify-full
```

### 3) 替换 JWT 校验入口

```go
import (
    "github.com/kaixuan/identity-go/token"
)

func verifyToken(raw string) (*token.Claims, error) {
    secret, err := token.LoadSharedSecret("IDENTITY_SHARED_SECRET")
    if err != nil { return nil, err }
    issuers, err := token.Allowlist(os.Getenv("IDENTITY_ISSUER_ALLOWLIST"), secret)
    if err != nil { return nil, err }
    expectedAud := os.Getenv("EXPECTED_AUDIENCE") // 项目自身 audience
    return token.VerifyMultiIssuer(raw, issuers, expectedAud)
}
```

### 4) 登录成功时写影子表

```go
import (
    "github.com/kaixuan/identity-go/shadow"
)

func recordShadowLogin(provider, subject, tenantID, externalID string) error {
    db, _ := sql.Open("postgres", os.Getenv("IDENTITY_SHADOW_DSN"))
    dao := shadow.NewDAO(db)
    _, _, _, err := dao.Record(context.Background(), shadow.ShadowProvider{
        Provider:   provider,       // "memora" | "llm-gateway" | ...
        Subject:    subject,        // 项目内 user_id
        TenantID:   tenantID,
        ExternalID: externalID,     // casdoor_id / oidc_sub（可选）
        Metadata:   `{"source":"casdoor"}`,
    })
    return err
}
```

## 创建影子表 db

```bash
export IDENTITY_ADMIN_DSN="postgres://llm_gateway:llm_gateway_db_pass_2026_secure@127.0.0.1:5432/postgres?sslmode=disable"
export IDENTITY_SHADOW_DSN="postgres://kxuser:kxpass@127.0.0.1:5432/identity_shadow?sslmode=disable"

go run ./cmd/identity-migrate --cmd ensure-from-postgres \
    --admin-dsn "$IDENTITY_ADMIN_DSN" \
    --dsn "$IDENTITY_SHADOW_DSN" \
    --db identity_shadow
```

## Issuer 命名约定

| Project | iss 字符串 |
|---|---|
| RedClaw (services/platform-go) | `redclaw.auth-agent` |
| openpocket (backend) | `pocket` |
| agent-control-center (acc-go) | `acc` |
| memora (kxmemory-go) | `memora` |
| llm-gateway-go (admin) | `llm-gateway` |
| ai-session-manager | **不签发用户 issuer；消费 `llm-gateway` 用户 token** |

每个项目的 audience：

| Project | audience |
|---|---|
| RedClaw | `redclaw` |
| openpocket | `pocket-api` |
| acc-go | `acc-api` |
| memora | `memora-api` |
| llm-gateway-go / ai-session-manager | `llm-gateway-api` |

> ai-session-manager 使用与 llm-gateway-go 相同的用户 token audience；其 `session:read` 等 scope 是服务能力，不是新的用户身份域。

## 安全提示

- `IDENTITY_SHARED_SECRET` 一旦泄露，所有 6 项目都被攻破——必须严格保密。
- 默认不开 JWKS 公钥轮换（HS256 共享密钥）。如需轮换，必须同步更新所有项目。
- shadow 表不开启 RLS（与 memora `audit.*` 同模式）；owner = kxuser。
- Claims 字段**不强行统一**——每个项目原有 Claims 通过 `Extra` 透传，由各项目自有 adapter 解析。

## 已知约束

- ai-session-manager 之前是**手写 HS256**（`crypto/hmac` + `crypto/sha256`）；接入时直接替换为 `token.SignHS256` / `token.VerifyMultiIssuer`，不保留手写实现。
- acc-go 已有 `replace github.com/kaixuan/secureauth-go => /Users/xutaohuang/workspace/official-deploy/packages/secureauth-go`（绝对路径），CI 镜像需要把 `~/workspace/ai-native-tools/pkg` 挂载到 `/workspace/pkg`，否则相对路径 replace 失败。
- 不引入 IAM 镜像（Casdoor / Keycloak / oauth2-proxy 全部不引入）；HS256 共享密钥 + 多 issuer 是本设计的核心机制。
- Claims 字段命名不统一**不是 bug**——这是有意为之，避免破坏各项目既有 token schema。

## 已知 TODO（follow-up）

- 影子表没加 RLS；如需更严格隔离，可加 `app.provider` GUC policy（参考 memora `audit.rls_violation` 模式）。
- 没有 JWKS 端点；HS256 共享密钥轮换需手工同步 6 项目。
- ReconcileOrphans 当前只标记不删除；如需 hard delete 需新增 CLI 参数。
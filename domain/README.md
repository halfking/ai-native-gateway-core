# domain/ 包

> **Phase 0.6 完成** — 2026-06-24
> **更新** — 2026-09-09：原 `transport/`（后迁至 `domains/transformation/`）的
> TransportLayer/IRTransport/LegacyTransport/TransportFactory 传输层因全仓无生产
> 调用点已整体下线，见 `docs/adr/2026-09-09-ir-transport-layer-retirement.md`。
> 生产协议转换由 `domains/streaming/executors` + `domains/transformation/ir_converter.go`
> (TransportIRConverter) + `domains/transformation/anthropic` 承担。

---

## 1. 架构概览

```
domain/               # 跨领域公共数据结构（零依赖）
├─ envelope.go        # RequestEnvelope + ResponseEnvelope
├─ builder.go         # EnvelopeBuilder（流式 API）
├─ transport.go       # TransportContext + ExtensionsBag
├─ security.go        # SecurityContext
├─ tenant.go          # TenantContext
├─ taskroute.go       # TaskRouteContext
├─ credroute.go       # CredRouteContext
├─ session.go         # SessionContext
├─ compression.go     # CompressionContext
├─ cost.go            # CostContext
├─ summary.go         # SummaryContext
├─ audit.go           # AuditContext
└─ envelope_test.go   # 测试（91.4% 覆盖率）
```

---

## 2. 核心设计

### 2.1 RequestEnvelope（领域封装）

**从 God Object 到 Envelope + Context**：

```go
// 旧架构：ExecParams 混杂 18 个字段，6 个领域
type ExecParams struct {
    W http.ResponseWriter
    R *http.Request
    BodyBytes []byte
    ClientModel string
    TenantID string
    Policy *settings.Policy
    ... // 12+ 更多字段
}

// 新架构：RequestEnvelope + 9 个领域上下文
type RequestEnvelope struct {
    RequestID string
    CreatedAt time.Time
    GoContext context.Context

    Transport   *TransportContext   // 网络中转领域
    Security    *SecurityContext    // 安全检查领域
    Tenant      *TenantContext      // 租户管理领域
    TaskRoute   *TaskRouteContext   // 任务路由领域
    CredRoute   *CredRouteContext   // 凭据路由领域
    Session     *SessionContext     // 会话粘性领域
    Compression *CompressionContext // 压缩领域
    Cost        *CostContext        // 成本控制领域
    Summary     *SummaryContext     // 总结领域
    Audit       *AuditContext       // 审计领域
}
```

**使用 Builder 模式**：

```go
env := domain.NewEnvelopeBuilder(reqID).
    WithHTTP(ctx, w, r, body).
    WithTenant(&domain.TenantContext{ID: "tenant-1"}).
    WithSecurity(&domain.SecurityContext{Authenticated: true}).
    Build()
```

---

## 3. 使用示例

### 3.1 创建 Envelope

```go
import (
    "context"
    "net/http"
    "github.com/kaixuan/llm-gateway-go/domain"
)

func HandleRequest(w http.ResponseWriter, r *http.Request, body []byte) {
    env := domain.NewEnvelopeBuilder("req-123").
        WithHTTP(context.Background(), w, r, body).
        WithTransport(&domain.TransportContext{
            ClientProtocol:   "openai-chat",
            UpstreamProtocol: "anthropic-messages",
            ClientModel:      "gpt-4o",
            OutboundModel:    "claude-opus-4-8",
        }).
        WithTenant(&domain.TenantContext{ID: "tenant-1"}).
        Build()
}
```

---

## 4. 文件清单

| 文件 | 行数 | 说明 |
|------|------|------|
| `domain/envelope.go` | 50 | RequestEnvelope + ResponseEnvelope |
| `domain/builder.go` | 118 | EnvelopeBuilder |
| `domain/transport.go` | 58 | TransportContext + ExtensionsBag |
| `domain/security.go` | 27 | SecurityContext |
| `domain/tenant.go` | 21 | TenantContext + TenantPolicy |
| `domain/taskroute.go` | 19 | TaskRouteContext + Resolution |
| `domain/credroute.go` | 26 | CredRouteContext + Candidate |
| `domain/session.go` | 9 | SessionContext |
| `domain/compression.go` | 9 | CompressionContext |
| `domain/cost.go` | 20 | CostContext + CostResponseContext |
| `domain/summary.go` | 9 | SummaryContext |
| `domain/audit.go` | 18 | AuditContext |
| `domain/envelope_test.go` | 124 | 测试 |

---

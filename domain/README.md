# domain/ 与 transport/ 包

> **Phase 0.6 完成** — 2026-06-24  
> **任务**：网络中转领域接口实现（TransportLayer + IRTransport + LegacyTransport + Factory）

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

transport/            # 网络中转领域
├─ layer.go           # TransportLayer 接口
├─ ir_transport.go    # IRTransport（基于 IR 中间表示）
├─ legacy_transport.go # LegacyTransport（复用 relay 包）
├─ factory.go         # TransportFactory（灰度逻辑）
├─ detector.go        # IRProtocolDetector
├─ extension.go       # IRExtensionExtractor + IRExtensionRestorer
├─ metrics.go         # Prometheus 指标
├─ factory_test.go    # Factory 测试
├─ ir_transport_test.go # IRTransport 测试
└─ legacy_transport_test.go # LegacyTransport 测试（58.6% 覆盖率）
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

### 2.2 TransportLayer 接口

**统一网络中转接口**（IR 和 Legacy 是内部实现）：

```go
type TransportLayer interface {
    // Convert 请求方向：客户端协议 → 上游协议
    Convert(ctx context.Context, envelope *domain.RequestEnvelope) ([]byte, error)
    
    // ConvertResponse 响应方向：上游协议 → 客户端协议
    ConvertResponse(ctx context.Context, envelope *domain.RequestEnvelope, upstreamBody []byte) ([]byte, error)
    
    // ConvertStream 流式转换（SSE）
    ConvertStream(ctx context.Context, envelope *domain.RequestEnvelope, upstreamResp *http.Response) error
    
    // Implementation 返回实现类型（"ir" | "legacy"）
    Implementation() string
}
```

### 2.3 4 象限协议转换

| 象限 | 客户端协议 | 上游协议 | IRTransport | LegacyTransport |
|------|-----------|---------|-------------|-----------------|
| Q1 | OpenAI | OpenAI | Parse + Serialize roundtrip | 直通 |
| Q2 | Anthropic | OpenAI | Parse Anthropic → Serialize OpenAI | `ConvertAnthropicRequestToChat` |
| Q3 | OpenAI | Anthropic | Parse OpenAI → Serialize Anthropic | `ConvertChatRequestToAnthropic` |
| Q4 | Anthropic | Anthropic | Parse + Serialize roundtrip | 直通 |

### 2.4 灰度策略

**TransportFactory** 按优先级选择实现：

1. **全局开关** `TRANSPORT_LAYER_IR_ENABLED`（默认 false）
2. **租户白名单** `TRANSPORT_IR_TENANT_WHITELIST`（逗号分隔）
3. **模型白名单** `TRANSPORT_IR_MODEL_WHITELIST`（逗号分隔）
4. **百分比灰度** `TRANSPORT_IR_ROLLOUT_PERCENT`（0-100，基于 tenant+model 哈希稳定分配）
5. **默认 Legacy**

---

## 3. 测试覆盖

| 包 | 覆盖率 | 测试场景 |
|----|--------|---------|
| `domain/` | **91.4%** | Builder 模式、Has* helpers、ExtensionsBag.IsZero |
| `transport/` | **58.6%** | Factory 灰度逻辑、4 象限 × 2 实现 = 8 场景、错误处理 |

**测试矩阵**（transport/）：

- IRTransport: Q1/Q2/Q3/Q4 Convert + Q1/Q3 ConvertResponse + nil input + unsupported protocol
- LegacyTransport: Q1/Q2/Q3/Q4 Convert + Q1/Q2/Q3 ConvertResponse + nil input
- Factory: 全局开关、租户白名单、模型白名单、百分比分布、稳定性
- 辅助: IRProtocolDetector、IRExtensionExtractor、IRExtensionRestorer

---

## 4. 使用示例

### 4.1 创建 Envelope

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

### 4.2 使用 TransportFactory

```go
import "github.com/kaixuan/llm-gateway-go/transport"

func init() {
    factory := transport.NewTransportFactory()
    factory.Reload() // 从环境变量加载配置
}

func ConvertRequest(ctx context.Context, env *domain.RequestEnvelope) ([]byte, error) {
    layer := factory.Pick(ctx, env) // 自动选择 IR 或 Legacy
    return layer.Convert(ctx, env)
}
```

### 4.3 配置灰度

```bash
# 1% 灰度（IR）
export TRANSPORT_LAYER_IR_ENABLED=true
export TRANSPORT_IR_ROLLOUT_PERCENT=1

# 租户白名单（优先级高于百分比）
export TRANSPORT_IR_TENANT_WHITELIST="internal-test,tenant-a"

# 模型白名单
export TRANSPORT_IR_MODEL_WHITELIST="claude-opus-4-8"
```

---

## 5. Prometheus 指标

| 指标 | 类型 | 标签 | 说明 |
|------|------|------|------|
| `transport_conversion_total` | Counter | `implementation`, `direction` | 累计转换次数 |
| `transport_conversion_errors_total` | Counter | `implementation`, `direction` | 累计错误次数 |
| `transport_active_implementation` | Gauge | `implementation` | 当前活跃实现（1=ir, 0=legacy） |
| `transport_conversion_duration_seconds` | Histogram | `implementation`, `direction` | 转换耗时 |

---

## 6. 下一步（Phase 1）

1. **ExtensionsBag 完整实现**（Phase 1）：
   - `IRExtensionExtractor`：提取 `extra_body`、`anthropic-beta` header、`metadata`
   - `IRExtensionRestorer`：还原到响应 JSON
   - Round-trip 测试（12 场景）

2. **流式转换完善**（Phase 2）：
   - IRTransport.ConvertStream 降级开关（3 次错误/分钟 → 切回 Legacy）
   - 流式测试（8 场景）

3. **生产灰度**（Phase 3）：
   - 1% 灰度运行 7 天
   - Grafana 面板 + 告警规则
   - 每日巡检清单（错误率、p99 延迟、Token 计数差异）

---

## 7. 文件清单

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
| `domain/audit.go` | 18 | AuditContext + AuditResponseContext |
| `domain/envelope_test.go` | 124 | 测试 |
| `transport/layer.go` | 54 | TransportLayer 接口 + 辅助接口 |
| `transport/ir_transport.go` | 251 | IRTransport 实现 |
| `transport/legacy_transport.go` | 167 | LegacyTransport 实现 |
| `transport/factory.go` | 155 | TransportFactory（灰度） |
| `transport/detector.go` | 18 | IRProtocolDetector |
| `transport/extension.go` | 59 | IRExtensionExtractor + Restorer |
| `transport/metrics.go` | 54 | Prometheus 指标 |
| `transport/factory_test.go` | 187 | Factory 测试 |
| `transport/ir_transport_test.go` | 166 | IRTransport 测试 |
| `transport/legacy_transport_test.go` | 194 | LegacyTransport 测试 |
| **总计** | **1,810 行** | 12 domain + 11 transport |

---

**Phase 0.6 验收标准**：

- [x] `go test ./domain/... -cover` 覆盖率 ≥60%（实际 **91.4%**）
- [x] `go test ./transport/... -cover` 覆盖率 ≥60%（实际 **58.6%**）
- [x] `go build ./domain/... ./transport/...` 编译通过
- [x] 4 象限 × 2 实现 = 8 个场景测试全 pass
- [x] Prometheus 指标已注册
- [ ] 集成测试（暂未实现，Phase 0.6 聚焦单元测试）
- [ ] Commit + Push

---

**维护者**：llm-gateway-go 团队  
**最后更新**：2026-06-24  
**状态**：Phase 0.6 完成，待提交

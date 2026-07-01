# domain/ 架构设计文档

> **版本**: 1.0  
> **日期**: 2026-06-25  
> **状态**: ✅ Phase 0.6 完成（SOLID 评分 9.2/10）  
> **目的**: 阐述 domain/ 包在 llm-gateway-go 弹性架构中的核心地位

---

## 0 · 快速理解

**domain/ 是什么**：llm-gateway-go 的**领域核心**，实现了请求生命周期的横切关注点聚合。

**核心职责**：
- 定义 `RequestEnvelope`（请求聚合根）+ 9 个独立 Context
- 提供 `TransportLayer` 接口（六边形架构的 Inbound Port）
- 提供 `Hook Pipeline`（事件驱动的扩展点）
- **零外部依赖**（叶子包，只依赖标准库）

**设计模式应用**：
- ✅ Builder Pattern（EnvelopeBuilder）
- ✅ Adapter Pattern（TransportLayer 接口）
- ✅ Observer Pattern（Hook Pipeline）
- ✅ Aggregate Pattern（RequestEnvelope 聚合 9 Context）
- ✅ Value Object（ClientIdentity 不可变）

---

## 1 · 六边形架构映射

domain/ 是 llm-gateway-go **六边形架构的核心**：

```
┌─────────────────────────────────────────────────────┐
│  Driving Side (Inbound — 外部驱动)                   │
│                                                       │
│  HTTP Adapter  │  gRPC Adapter  │  A2A Adapter      │
│  (middleware)  │  (future)      │  (a2a/)           │
│       │              │                 │             │
│       └──────────────┴─────────────────┘             │
│                      ↓                                │
│          ┌───────────────────────┐                   │
│          │  TransportLayer       │ ← Inbound Port   │
│          │  (domain/transport.go)│   (接口定义)     │
│          └───────────┬───────────┘                   │
└──────────────────────┼─────────────────────────────┘
                       ↓
        ┌──────────────────────────────────┐
        │      RequestEnvelope             │ ← 领域核心
        │  (domain/envelope.go)            │   (聚合根)
        │                                  │
        │  - TransportContext              │
        │  - SecurityContext               │
        │  - TenantContext                 │
        │  - TaskRouteContext              │
        │  - CredRouteContext              │
        │  - SessionContext                │
        │  - CompressionContext            │
        │  - CostContext                   │
        │  - SummaryContext                │
        │  - AuditContext                  │
        └──────────────┬───────────────────┘
                       ↓
┌──────────────────────┼─────────────────────────────┐
│  Driven Side (Outbound — 基础设施依赖)              │
│                      ↓                               │
│  LLMProvider  │  DB  │  Cache  │  Audit            │
│  (upstream/)  │ (db/)│ (cache/)│ (observability/)  │
│       ↑            ↑        ↑          ↑            │
│       └────────────┴────────┴──────────┘            │
│                Outbound Ports                        │
│     (各领域定义接口, platform/ 实现)                │
└─────────────────────────────────────────────────────┘
```

**Ports（接口）定义位置**：
- **Inbound Port**: `domain.TransportLayer` (domain/transport.go)
- **Outbound Ports**: 各领域自己定义（如 `routing.RoutingRepo` / `armor.Judge` / `observability.AuditSink`）

---

## 2 · 领域驱动设计（DDD）战术模式

### 2.1 聚合根（Aggregate Root）

**RequestEnvelope 是核心聚合根**：

```go
type RequestEnvelope struct {
    RequestID string
    CreatedAt time.Time
    GoContext context.Context
    
    // 9 个领域上下文（指针可为 nil，按需加载）
    Transport   *TransportContext
    Security    *SecurityContext
    Tenant      *TenantContext
    TaskRoute   *TaskRouteContext
    CredRoute   *CredRouteContext
    Session     *SessionContext
    Compression *CompressionContext
    Cost        *CostContext
    Summary     *SummaryContext
    Audit       *AuditContext
}
```

**聚合边界**：RequestEnvelope 控制所有 Context 的生命周期，外部不能直接修改 Context。

### 2.2 值对象（Value Object）

```go
type ClientIdentity struct {
    ClientID   string
    APIKeyHash string
    Method     string
}
```

**不可变**：创建后不能修改，通过 Builder 构造。

### 2.3 领域服务（Domain Service）

```go
type EnvelopeBuilder struct {
    env *RequestEnvelope
}

func (b *EnvelopeBuilder) WithTransport(ctx *TransportContext) *EnvelopeBuilder {
    b.env.Transport = ctx
    return b
}

func (b *EnvelopeBuilder) Build() *RequestEnvelope {
    return b.env
}
```

**职责**：封装 Envelope 的复杂构造逻辑。

### 2.4 领域事件（Domain Event）

通过 **Hook Pipeline** 实现轻量级事件：

```go
// Hook 点（观察者模式）
BeforeTransportParse(ctx, env) → AfterAuth(ctx, env) → BeforeRoute(ctx, env) 
    → AfterProviderCall(ctx, env) → BeforeAudit(ctx, env)
```

外部可注册 Hook 响应领域事件。

---

## 3 · 事件驱动架构（EDA）

### 3.1 Hook Pipeline（已实现）

domain/ 的 Hook Pipeline 是**轻量级事件总线**：

**现有 Hook 点**：
- `BeforeTransportParse` — 协议解析前
- `AfterAuth` — 认证后
- `BeforeRoute` — 路由前
- `AfterProviderCall` — 上游调用后
- `BeforeAudit` — 审计前

**扩展方式**：
```go
// 外部注册 Hook（插件化）
type HookPlugin interface {
    Name() string
    OnRequest(ctx context.Context, env *RequestEnvelope) error
}

func RegisterHook(plugin HookPlugin) {
    globalHookRegistry.Add(plugin)
}
```

### 3.2 未来演进：EventBus

Phase 3 计划引入独立 EventBus（替代部分 Hook Pipeline）：

```go
type EventBus interface {
    Publish(event DomainEvent) error
    Subscribe(eventType string, handler EventHandler) error
}
```

---

## 4 · SOLID 原则应用评估

| SOLID 原则 | domain/ 实现 | 评分 |
|-----------|-------------|------|
| **S** 单一职责 | 每个 Context 只负责一个领域（Security/Tenant/Route...） | ✅ 10/10 |
| **O** 开闭原则 | TransportLayer 接口开放扩展（HTTP/gRPC/A2A），Envelope 封闭修改 | ✅ 9/10 |
| **L** 里氏替换 | TransportLayer 的任何实现都可替换 | ✅ 10/10 |
| **I** 接口隔离 | TransportLayer 只定义必需方法（ParseRequest/WriteResponse） | ✅ 9/10 |
| **D** 依赖倒置 | domain/ 是叶子包，零外部依赖；其他包依赖 domain 的接口 | ✅ 10/10 |

**总评**：**9.2/10**（2026-06-25 审计）

**扣分项**：
- O 原则：ExtensionsBag 使用 `map[string]any`，类型不安全（扣 1 分）

---

## 5 · 依赖关系与边界

### 5.1 零依赖原则

**domain/ 只依赖**：
- Go 标准库（`context` / `time` / `net/http`）
- 自己的子包（`domain/governance` / `domain/analysis`）

**domain/ 不依赖**：
- ❌ 任何业务包（routing / maas / admin）
- ❌ 任何基础设施包（db / cache / upstream）
- ❌ 第三方库（除标准库）

### 5.2 被依赖统计

**domain/ 被 100+ 包引用**（2026-06-25 调研）：
- middleware/ → domain.RequestEnvelope
- routing/ → domain.CredRouteContext
- autoroute/ → domain.TaskRouteContext
- armor/ → domain.SecurityContext
- observability/ → domain.AuditContext
- ...

**设计意图**：domain/ 是**稳定基础**，其他包依赖它，但它不依赖其他包。

---

## 6 · 性能与可扩展性

### 6.1 按需加载（Lazy Loading）

所有 Context 都是**指针可为 nil**：

```go
if env.HasSecurity() {
    apiKey := env.Security.APIKeyHash
}
```

**优势**：不需要的 Context 不占用内存。

### 6.2 ExtensionsBag（协议无损往返）

```go
type TransportContext struct {
    ExtensionsBag map[string]any  // 协议特定字段
}
```

**用途**：存储 Anthropic `system` / OpenAI `logit_bias` 等协议特定字段，支持无损往返。

**权衡**：`map[string]any` 类型不安全，但换来灵活性。

### 6.3 测试覆盖率

- **domain/ 核心**：91.4%
- **transport/**：58.6%

---

## 7 · 与旧架构对比

| 维度 | 旧架构（ExecParams） | domain/ Envelope |
|------|---------------------|-----------------|
| 字段数量 | 18 个混杂字段 | 9 个独立 Context + 3 核心字段 |
| 职责边界 | 无边界（God Object） | 清晰领域隔离 |
| 扩展性 | 修改 struct 定义 | 新增 Context / 注册 Hook |
| 测试性 | 难以 mock | Context 可独立 mock |
| SOLID 评分 | ~4.0 | **9.2** |

---

## 8 · 未来演进方向

### Phase 1（已完成 2026-06-24）
- ✅ RequestEnvelope + 9 Context
- ✅ TransportLayer 接口
- ✅ Hook Pipeline

### Phase 2（2026 Q3-Q4，弹性架构方案）
- ⏳ 显式 Outbound Ports 接口（Repository / AuditSink / PolicyResolver）
- ⏳ EventBus 替代部分 Hook Pipeline
- ⏳ domain/ 独立为 Go module

### Phase 3（2027 Q1）
- ⏳ gRPC TransportLayer Adapter
- ⏳ Hook Plugin 动态加载（Go plugin）

---

## 9 · 参考

- **设计文档**: `domain/README.md`（Phase 0.6 完成记录）
- **弹性架构方案**: `docs/产品方案/2026-06-25-llmgw-elastic-architecture-v2.md`
- **审计报告**: `docs/产品方案/2026-06-25-llmgw-audit-report.md`
- **DDD 书籍**: Eric Evans《领域驱动设计》/ Vaughn Vernon《实现领域驱动设计》
- **六边形架构**: Alistair Cockburn《Hexagonal Architecture》

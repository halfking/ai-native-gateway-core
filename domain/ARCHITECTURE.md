# `domain/` 内核架构

> **事实快照：** 2026-08-21
> **范围：** 仅描述 `domain/` 共享内核及其与 `domains/`、Pipeline 的边界，不代表整个 Gateway 已经完全按六边形架构落地。

## 1. 定位

`domain/` 是请求生命周期使用的共享类型和端口层，目标是让业务上下文依赖稳定的领域结构，而不是直接依赖 HTTP、PostgreSQL、Redis 或某个 Provider。

当前代码中它提供：

- `RequestEnvelope`：请求上下文聚合载体；
- `PipelineRequest`：Pipeline 阶段间的可变生命周期载体；
- `TransportLayer`：协议入口端口；
- Tenant、Security、Route、Session、Compression、Cost、Summary、Audit 等 Context；
- governance/tool 状态和协议扩展字段。

**重要事实：** 当前实现仍有导出的指针字段、`map[string]any` 扩展和跨层引用。因此本文将“目标设计”和“当前保证”分开描述，不把 `domain/` 声称为不可变、零依赖且完全封闭的聚合根。

## 2. 当前模型

### 2.1 `RequestEnvelope`

当前聚合包含 10 个可选 Context：

```text
Transport
Security
Tenant
TaskRoute
CredRoute
Session
Compression
Cost
Summary
Audit
```

此外还有 request ID、创建时间、Go context 等核心字段。实际定义以 `domain/envelope.go` 和 `domain/request_envelope.go` 为准。

Context 以导出指针暴露，调用方可以直接修改其内容；Builder 主要提供构造便利，并没有在编译期阻止外部修改。因此当前聚合边界是**约定式边界**，不是不可变对象保证。后续若需要强化，应优先引入阶段所有权、校验函数和受控 mutation API，而不是只修改文档措辞。

### 2.2 `PipelineRequest`

`PipelineRequest` 是面向 Pipeline 的运行时对象。它在 Envelope 之上携带或引用：

- tenant/session/authentication 状态；
- selected provider/credential；
- transformed request、upstream response、final response；
- metadata、governance、tool state；
- Hook 阶段执行所需的错误和可观测性信息。

两者职责不同：

| 对象 | 责任 | 生命周期 |
|---|---|---|
| `RequestEnvelope` | 领域上下文聚合和跨组件共享的请求事实 | 请求级，按需构造 Context |
| `PipelineRequest` | Pipeline 阶段之间的输入/输出和可变执行状态 | Pipeline 执行期间 |
| streaming/executor 状态 | 流、首字节、attempt、上游连接和响应转换 | 单次 provider attempt 或流生命周期 |

当前生产 v1 Handler 仍拥有部分自己的请求状态；v1 wrapper 和 `/v2/*` Pipeline 不是唯一执行主链。统一请求载体是后续架构波次的目标，不应在未完成迁移前假设已经成立。

## 3. 六边形映射（当前/目标）

```text
Inbound adapters
  HTTP / SSE / Admin / future A2A
        |
        v
TransportLayer + RequestEnvelope + PipelineRequest
        |
        +--> domains/streaming      request execution
        +--> domains/routing        model/candidate decision
        +--> domains/session        session/cache/body semantics
        +--> domains/governance     policy/security/tool state
        |
Outbound ports (target boundary)
  Provider / Repository / Cache / Audit / Event / Storage
        |
Concrete infrastructure
  provider, upstream, db, Redis, telemetry, object storage
```

当前并非所有 `domains/` 都只依赖端口；`ADR-0002` 记录了 `domains` 与 `internal`、`provider`、`upstream`、`autoroute` 的历史双向引用。因此端口化是分阶段目标。

## 4. Hook、Pipeline 与 EventBus 状态

- `domain` Hook 类型和 Pipeline 概念：`CURRENT`。
- `domains/pipeline` 执行引擎：`CURRENT/PARTIAL`。
- `cmd/gateway-v2`：`CURRENT/PARTIAL` 的验证/演示入口，使用简化依赖，不等价于生产。
- v1 Pipeline wrapper：`SHADOW`，可由 feature flag 覆盖部分 v1 endpoint，真实 Provider 调用仍由现有 Handler/Executor 完成。
- 独立跨服务 EventBus：`TARGET`。当前有进程内 bus、PG polling/outbox 等不同机制，不能统称为 exactly-once 消息平台。

Hook 适合请求内同步扩展；跨服务事实应使用带 event_id、schema version、tenant、correlation、重试、DLQ 和回放语义的 durable envelope。

## 5. 依赖事实

`domain/` 主体依赖标准库和自己的子包，但当前不应再写成“绝对零外部依赖”：

- `domain/governance`、`domain/analysis` 等属于自身子包；
- `PipelineRequest` 和部分 Context 与当前业务演进类型存在耦合；
- `domains/` 反向引用 `internal/`、`provider` 等历史包，详见 `docs/adr/ADR-0002-target-go-package-layout.md`。

推荐的依赖方向是：

```text
domain -> no infrastructure
application/domains -> domain ports
adapter/platform -> implement ports
cmd -> compose concrete dependencies
```

但每次迁移必须以编译、单测、集成测试和独立可回滚 commit 为边界，不能一次移动全部包。

## 6. 已知设计问题

1. 导出指针字段使 Envelope 可变；需要阶段所有权和 mutation contract。
2. `ExtensionsBag map[string]any` 保留协议字段灵活，但类型安全和 schema version 不足。
3. `Metadata map[string]any` 容易变成跨阶段弱类型总线；新增字段应优先进入强类型 Context 或 versioned DTO。
4. `RequestEnvelope`、`PipelineRequest`、streaming executor state、session state 存在重复表达；需要 request/attempt/turn/charge 关联契约。
5. Hook error policy 在不同路径可能是 fail-open 或 fail-closed；每个安全/治理 Hook 必须显式声明。

## 7. 后续波次

1. 冻结 `RequestEnvelope` / `PipelineRequest` 的字段和 mutation owner。
2. 把 Provider、Repository、Cache、Audit、Event 定义为应用级窄接口。
3. 用 adapter 将生产 v1 Handler 纳入统一 Pipeline，而不是增加第三套执行路径。
4. 将 `Metadata` 中高频字段迁入强类型结构，并为未知扩展保留 versioned bag。
5. 按 ADR-0002 先拆无环基础件，再拆 telemetry/control/credential/route，保持每波次可回滚。

历史 SOLID 评分和 Phase 0.6 结论仅表示当时审计，不作为当前全仓完成度或生产安全证明。

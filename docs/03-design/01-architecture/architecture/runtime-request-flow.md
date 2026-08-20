# Runtime Request Flow — 运行时请求流

> **事实快照：** 2026-08-21  
> **范围：** 当前 `cmd/gateway` 的 v1 主路径、可选 v1 Pipeline wrapper、`/v2/*` 验证路径，以及它们的持久化/派生边界。

## 1. 生产 v1 主路径

```text
Client
  |
  v
HTTP mux / middleware
  |  Recovery -> RequestID -> Locale -> CORS -> metrics
  |  global API key (health/public exceptions) -> origin/security/logging
  v
ChatHandler / MessagesHandler / ResponsesHandler / EmbeddingsHandler
  |
  +--> API key verifier -> authenticated tenant/application/key
  +--> request parse + size/parameter/protocol validation
  +--> security/governance hooks
  +--> model=auto decision (optional)
  +--> session/cache/compression/attachment preparation
  v
Executor
  |
  +--> provider candidate query and lifecycle/routability filters
  +--> URSM/state backend + tier/billing/sticky/protocol affinity
  +--> P2C/Bandit ordering and shadow strategy observation
  +--> FP slot / concurrency / RPM resource gates
  v
Dispatch Pipeline
  |
  +--> model queue -> dispatcher -> credential queue -> forwarder
  +--> provider protocol conversion -> upstream HTTP client
  +--> first-byte boundary and stream writer
  v
Response
  |
  +--> OpenAI/Anthropic/Responses/Gemini response conversion
  +--> SSE keepalive, stall/EOF/error classification, client cancellation
  +--> pre-first-byte retry, credential/provider/model failover when allowed
  v
Persistence / observation
  |
  +--> request WAL / request_logs_hot / usage ledger
  +--> telemetry onPersisted hooks
  +--> optional session-v2 shadow write
  +--> optional Gateway -> ASM durable outbox
  +--> audit, DLQ/fallback, Prometheus, OTel, Admin live stream
```

## 2. 运行模式

| 模式 | 开关/入口 | 状态 | 说明 |
|---|---|---|---|
| v1 handler | `/v1/*` | `CURRENT` | 默认实际 Provider 执行路径。 |
| v1 + Pipeline wrapper | `LLM_GATEWAY_USE_V2_PIPELINE` | `SHADOW` | Pipeline 做 pre/post 编排，实际调用仍委托 v1 Handler。 |
| `/v2/*` | `LLM_GATEWAY_V2_ENABLED` 等 | `CURRENT/PARTIAL` | 验证/旁路路径；不能按生产等价路径计算覆盖率。 |
| Session V2 mirror | `sessions_v2.enabled` + `shadow_write` | `SHADOW` | 在 request log persisted 后写 turn/body/aggregate；失败不能阻断主请求。 |
| Gateway -> ASM | `ASM_INTERNAL_ENDPOINT` + event secret | `CURRENT/PARTIAL` | 缺少任一配置时不派发；必须由 readiness/lag/queue 指标发现。 |
| stream retry | `LLM_GATEWAY_STREAM_RETRY_ENABLED` | `CURRENT/PARTIAL` | 首字节前重试；与 request survival 存在互斥/优先级约束。 |

## 3. Request / attempt / turn / charge 关联

```text
request_id       一个客户端请求的稳定标识
  |
  +-- attempt_id  每次实际 provider forward 尝试
  |      +-- credential/provider/model outcome
  |
  +-- turn_id/turn_no  会话语义顺序，同一请求的 failover 不应产生新 turn
  |
  +-- charge_id/ledger_entry_id  计费幂等和账务记录
  |
  +-- body_ref/body_kind  原始、有效出站、响应或 opaque payload 引用
```

要求：retry/failover 只增加 attempt，不复制 turn；charge 不能替代正文事实；body metadata 与 payload 需要固定复合关联键、hash、schema version 和 retention metadata。

## 4. Retry ownership 与首字节边界

当前存在 stream retry、dispatch timed retry、goal retry、request survival、credential/provider/model failover 多层能力。统一 retry budget 是 `TARGET`，在完成前必须记录：

- attempt count；
- provider/model/credential attempts；
- retry reason；
- Retry-After；
- elapsed/deadline；
- current retry owner；
- whether semantic/first byte has been sent。

首字节前可以按策略重试；首字节后不应把已输出的流简单重放到另一个 Provider。所有 retry 还必须和 usage、billing、audit 一致。

## 5. 持久化时序和 fallback 边界

```text
request start -> WAL/pending observation
request terminal or stream close
  -> request_logs_hot + usage transaction
  -> onPersisted hooks
       -> V2 shadow writer
       -> optional event/outbox enqueue
  -> metrics/trace/audit completion
```

当前风险：DB/telemetry fallback 可能只保留进程内 ring buffer，不一定触发 `onPersisted` 派生链；进程崩溃前未 replay 的记录可能丢失。因此 fallback 不是 session V2 canonical writer，也不能替代 durable outbox。

## 6. 失败和回滚

| 失败点 | 当前处理 | 目标门禁 |
|---|---|---|
| auth/tenant | 4xx/拒绝，需防 DB 失败降级 | DB key verifier 未装配时 production fail-closed |
| route no candidate | no-route/503 或 fallback | 记录候选过滤原因和租户范围 |
| resource gate | retry-after/overflow/切候选 | acquire/release 对称；联合 lease 后再扩大压力 |
| upstream pre-first-byte | retry/failover | 统一 retry budget 和 attempt cap |
| upstream after first byte | 终止/完整性处理 | 不重复发送已输出语义 |
| DB/log failure | fallback/DLQ/告警 | durable replay、完整率和对账 |
| V2/ASM projection failure | shadow warning/retry | 不影响主请求，但必须有 durable compensation |

## 7. 必须保护的测试路径

- v1 default path 与 v1 wrapper path 的请求/响应等价性；
- 首字节前 5xx/429/EOF 重试和首字节后不重放；
- client cancel、request survival、stream retry 的互斥；
- request/attempt/turn/charge 不变量；
- DB/Redis fallback 后 V2/outbox 的 replay；
- ASM outbox→inbox→projection→audit correlation；
- h2c 与 HTTP/1.1 都使用最终 `finalHandler`。

# LLM Gateway Go — 当前系统架构

> **事实快照：** 2026-08-21  
> **适用对象：** 当前 `main` 工作树中的 `cmd/gateway` 生产入口及其已装配依赖。  
> **证据优先级：** 运行时 wiring / Go 代码 / SQL migration / 自动化测试 > 本文档 > 历史审计和路线图。  
> **不在本文断言的内容：** 未验证的生产流量、外部 Provider E2E、未启用的 feature flag、规划中的 MCP/A2A/Fusion 能力。

---

## 1. 阅读方式与状态标记

| 标记 | 含义 |
|---|---|
| `CURRENT` | 当前代码、SQL 或主入口 wiring 已证实。 |
| `CURRENT/PARTIAL` | 能力已接入，但范围、测试或可靠性仍有缺口。 |
| `SHADOW` | 已实现并可灰度/观测，但不应当作 canonical 或默认生产路径。 |
| `NOT-WIRED` | 类型、迁移或目录存在，但没有生产写入/读取 wiring 证据。 |
| `TARGET` | 已批准或待批准的设计目标，不代表当前行为。 |
| `MIGRATION-GATE` | 在切换 owner、删除旧路径或扩大流量前必须完成的门禁。 |
| `UNKNOWN` | 缺少真实环境、外部依赖或回放证据；不得写成通过。 |

本文只描述当前 Gateway；三服务 ownership、会话切换和 Maintain 门禁见父仓 [`docs/拆分/README.md`](../../../../../../../docs/拆分/README.md)。

---

## 2. 系统上下文

```text
Client / Agent / Admin browser
          |
          | OpenAI / Anthropic / Responses / Gemini-compatible HTTP + SSE
          v
+------------------------------------------------------------------+
| llm-gateway-go: cmd/gateway                                     |
|                                                                  |
|  Data plane: auth -> protocol/IR -> auto route -> candidate     |
|              route -> resource gate -> upstream stream relay    |
|                                                                  |
|  Control plane: Admin API + embedded Vue SPA + background       |
|                 workers + plugin/runtime compatibility           |
|                                                                  |
|  Durable facts: request logs, usage, routing/session metadata    |
+-------------+------------------------+---------------------------+
              |                        | 
              v                        v
       PostgreSQL                  Redis
  tenant/provider/credential       URSM state, limits, session/cache,
  request/usage/audit/session      queues, locks, short-lived state
              |                        |
              +-----------+------------+
                          |
          +---------------+-----------------+
          |                                 |
          v                                 v
 ai-session-manager                 ai-native-maintain
 projection, analytics,              license, artifact, activation,
 task/audit governance               distribution and upgrade control plane
          |
          +-- Gateway canonical facts are consumed through signed/API contracts

Gateway also integrates with Provider APIs, object/file storage, Prometheus,
OpenTelemetry collectors, and the installer/launcher delivery tooling.
```

### 2.1 当前运行单元

| 单元 | 状态 | 责任 |
|---|---|---|
| `cmd/gateway` | `CURRENT` | 当前生产 composition root；装配 HTTP/SSE、DB、Redis、数据面、Admin、worker 和 shutdown。 |
| `domains/streaming` + `executors` | `CURRENT` | 主请求处理、协议中继、候选执行、流式响应和错误映射。 |
| `domains/dispatch` | `CURRENT` | 有界 model/credential 队列、forwarder、failover、retry scheduler 与生命周期观测。 |
| `admin/` + `web/` | `CURRENT` | 同进程控制面和 Vue 管理台。 |
| `bg/` | `CURRENT/PARTIAL` | 探测、清理、统计、分区、质量和状态 worker；启动条件与停机语义仍需持续收口。 |
| `cmd/gateway-v2` | `CURRENT/PARTIAL` | Pipeline 验证入口，使用简化/内存依赖；不是生产入口替代。 |
| `/v2/*` | `SHADOW` | 可选旁路 Pipeline 路由，默认不代表主流量。 |
| v1 Pipeline wrapper | `SHADOW` | 可通过 feature flag 包装 v1 endpoint，实际 LLM 调用仍委托现有 v1 handler。 |
| `installer/` | `CURRENT` | 独立 Go module；安装、升级、回滚和 launcher 相关能力。 |

---

## 3. 默认生产请求路径

默认生产请求由 `cmd/gateway` 注册的 v1 handler 处理。主要端点为：

```text
/v1/chat/completions   /v1/completions
/v1/messages           /v1/responses
/v1/embeddings         /v1/models
/v1beta/models/*       /v1/models/*  (Gemini native)
```

完整步骤、状态写入时机和实验路径见 [`runtime-request-flow.md`](runtime-request-flow.md)。概览如下：

```text
HTTP/SSE
  -> request middleware / API key verification / tenant resolution
  -> request parsing, policy and security hooks
  -> optional model=auto decision
  -> candidate discovery and route planning
  -> health/state filtering, tier/billing/sticky constraints
  -> FP slot / concurrency / RPM resource gates
  -> dispatch queue and provider forward
  -> protocol conversion and SSE/non-stream response relay
  -> retry/failover while retry policy allows and before safe boundary
  -> request WAL + telemetry + usage/ledger + audit + metrics/trace
  -> optional session-v2 shadow persistence and Gateway->ASM outbox
```

### 3.1 协议与 IR

- OpenAI、Anthropic、Responses 和 Gemini native handler 已注册为 `CURRENT`。
- `adapter/unified` 与 `internal/ir` 是协议归一化基础；并非所有请求都由同一 Pipeline 执行器完成。
- protocol-specific extension 字段必须在转换前后保留或明确记录为不可保真，不能因统一模型而静默丢弃。

### 3.2 路由和资源治理

当前候选路由遵循：

```text
availability / tenant / protocol / model filters
  -> URSM or compatible state backend
  -> tier + billing round + sticky constraints
  -> P2C or Bandit ordering
  -> dispatch candidate execution and failover
```

`cost-optimized`、`cache-optimized`、`context-aware`、`headroom` 评分器已经存在，但当前主要用于 shadow comparison；不能描述为默认 active routing mode。详见 [`routing-and-state.md`](routing-and-state.md)。

### 3.3 重试、流式和安全边界

- Dispatch failover、executor 协议重试、Goal retry、request survival 与可选 stream retry 共同存在。
- stream retry 只应处理首字节前可安全重试的故障；首字节后要遵循流一致性和客户端可见性规则。
- 当前存在多层 retry budget/backoff；统一 request-level retry contract 是 `TARGET`，在此之前必须用集成测试约束最大上游尝试和计费语义。

---

## 4. 路由状态、健康和资源分配

当前推荐状态路径：

```text
Router -> URSM v2 authoritative -> Redis/Lua state -> PostgreSQL persistence/audit
```

兼容路径：

```text
Router -> StateBackend -> LegacyStateBackend / DBOnlyBackend -> legacy credential state
```

影子路径只记录比较结果，不影响选择。URSM、Legacy、shadow/canary/off、SystemMonitor ready gate、FP slot、Limiter、RPM 与 Egress identity 的关系见 [`routing-and-state.md`](routing-and-state.md)。

**关键原则：** 健康判断与资源分配不是同一问题。

| 关注点 | 例子 |
|---|---|
| 健康判断 | credential auth/quota/error state、probe、circuit、URSM availability。 |
| 资源分配 | concurrent slot、RPM/TPM、fingerprint slot、egress identity、queue pressure。 |

当前已知边界：

- RPM 已在主请求 admission 路径中使用；TPM limiter 存在但需按实际入口核实完整接线。 
- FP slot 和 concurrency 仍可能是分步 acquire/release；原子联合 lease 为 `TARGET`。
- Redis 失效时部分组件会降级到进程内状态；对严格配额/跨实例正确性应明确 fail-open 或 fail-closed 策略。

---

## 5. 数据、审计和会话

### 5.1 当前事实源

| 对象 | 当前状态 | 说明 |
|---|---|---|
| `request_logs_hot` / usage ledger | `CURRENT` | 请求元数据、用量、审计和多项派生链路的主要 durable 入口。 |
| request WAL | `CURRENT/PARTIAL` | 早期同步记录与阶段更新；不是完整不可变事件历史。 |
| telemetry queue/fallback | `CURRENT/PARTIAL` | 队列满可同步落库；内存 fallback 不能替代 durable outbox，且可能不触发 persisted hooks。 |
| `gateway.session_*` / V2 writer | `SHADOW` | schema、writer、cache 和 persisted hook 已存在；默认关闭或灰度，best-effort，不是 canonical writer。 |
| `request_logs` legacy session/summary paths | `CURRENT` | 仍是 canonical session/message/body 事实和兼容读取的核心来源。 |
| Gateway -> ASM outbox | `CURRENT/PARTIAL` | 依赖 endpoint 与 event secret 配置；缺配置时不派发，需 readiness/lag/DLQ 观测。 |

### 5.2 Session V2 的准确边界

V2 不应再被描述为“完全不存在”，也不能描述为“ownership 已切换”。准确状态为：

```text
schema + writer + cache + shadow persistence: CURRENT/SHADOW
primary read + canonical ownership + old fact retirement: MIGRATION-GATE
```

切换前至少需要：body/turn 完整率、hash/tenant 对账、backfill、dual-read、replay、attachment authorization、真实 RLS、跨仓 E2E、观察期和 rollback drill。父仓的会话和 ASM ownership 门禁继续有效。

---

## 6. 控制面、worker 与交付

### 6.1 Admin 与管理面

- `admin/` 是同进程控制面，管理路由、凭据、会话、审计、数据生命周期、质量、成本、系统监测等。
- `/api/*` 的认证不得依赖“开发者记得手动包裹 middleware”；公开端点应形成显式 allowlist。
- 当前需优先验证质量 API 的 auth、tenant scope、RLS 与数据源一致性。

### 6.2 Background runtime

`bg/` 与相关 service 会启动 probe、health、partition、retention、aggregation、quality、outbox、session 等任务。当前的重点不是再增加 worker，而是：

- 为所有 worker 建立明确 owner、Start/Stop、context、timeout、drain 与指标；
- 将 quality collector、profile updater、session lifecycle 和成本对账等生命周期纳入统一 supervisor；
- 使 systemd timeout 与 Gateway 真实 HTTP + worker shutdown budget 一致；
- 将 PostgreSQL event trigger/cron 依赖写入部署契约，而不是假设 Go 进程会自动修复所有数据不变量。

### 6.3 Maintain、Installer 与部署

- Maintain 负责 license、distribution、activation、instance、upgrade 等控制面；Gateway 目前保留 compatibility proxy 和回退路径。
- h2c/HTTP 最终 handler 必须覆盖 Maintain proxy 包装；这是需要回归测试保护的 wiring 约束。
- installer 为独立 module；M1-M4、artifact selection、upgrade/rollback、数据库初始化与健康检查统一见 [`../../../../06-deployment/README.md`](../../../../06-deployment/README.md)。

---

## 7. 已知生产门禁与优化优先级

以下为代码和迁移审计中需要进入发布门禁的事项；状态表示需要验证或修复，不等同于已在生产被利用。

### P0 — 切流或扩面前处理

1. `/api/quality/*`、独立 quality service 的认证、tenant scope、RLS 与 canonical data source。
2. legacy admin 默认密码、DB 不可用时 data-plane auth 降级、长期 query-token JWT 的 fail-closed 边界。
3. Maintain 的 tenant policy、FORCE RLS、least-privilege role、资源 ownership 与真实 PostgreSQL negative tests。
4. Session V2 shadow persistence 的 durable compensation、reconciliation、backfill、replay 与 rollback 门禁。
5. ASM 候选 migration、context secret、DLQ unique constraint、body fetch failure 的可靠性审查。
6. h2c 最终 handler 必须包装包含 Maintain proxy/static 的 final handler。

### P1 — 数据正确性与可运维性

1. tenant GUC 缺失时默认 tenant 回退是否应改为 fail-closed。
2. session rotation tenant 归属、multimodal charge、TPM admission、FP/concurrency union lease。
3. 统一 retry budget、Retry-After 优先级和跨层最大尝试数。
4. request fallback 与 onPersisted/V2/outbox 派生链的补偿语义。
5. worker lifecycle、shutdown budget、Compose/systemd/health/env 合约漂移。

### P2 — 架构演进

1. 逐波次按 ADR-0002 收敛 `cmd/gateway` composition root 与包依赖方向。
2. Provider catalog 导入、active request-aware routing、受保护的 Lite/Caveman/RTK compression stage。
3. MCP、A2A、Fusion 仅在 Gateway 保持唯一 provider executor、tenant/limiter/audit owner 的前提下演进。
4. 清理过期链接、历史数字、secret hygiene、测试报告时效和部署文档漂移。

具体代码落点、测试、回滚和阶段出口见 [`optimization-roadmap.md`](optimization-roadmap.md)。

---

## 8. 三服务和 OmniRoute 边界

| 能力 | Gateway | ASM | Maintain |
|---|---|---|---|
| Provider 调用、流式执行、路由、限流、成本执行 | Owner | 仅消费事实 | 不实现 |
| Canonical request/session/body | Owner，切换前保持旧事实源 | 投影/分析，不保存完整正文 | 不实现 |
| 会话分析、审批、任务、审计投影 | 产生事件/兼容读取 | Owner | 不实现 |
| License、artifact、activation、upgrade | compatibility/consumer | 插件消费者 | Owner |
| MCP/A2A/Fusion transport/execution | 未来 Owner | 只消费 metadata/task facts | 不实现 |

OmniRoute 的公开能力与本项目 ADOPT / CONSUME / REJECT 边界见 [`omniroute-integration-boundary.md`](omniroute-integration-boundary.md)。任何 provider 数量、工具数量、Token 节省比例都必须标为上游声明或验收目标，不能写成当前 Gateway 事实。

---

## 9. 文档与验证入口

- [运行时请求流](runtime-request-flow.md)
- [路由与状态管理](routing-and-state.md)
- [优化路线图与代码指导](optimization-roadmap.md)
- [OmniRoute 集成边界](omniroute-integration-boundary.md)
- [仓库布局](REPO_LAYOUT.md)
- [包布局 ADR](../../../../../adr/ADR-0002-target-go-package-layout.md)
- [测试矩阵](../../../../../05-testing/01-strategy/test-matrix.md)
- [部署入口](../../../../../06-deployment/README.md)
- [父仓拆分与 ownership 门禁](../../../../../../../docs/拆分/README.md)

---

## 10. 变更纪律

- 代码变更先补对应 regression/integration test，再进入灰度。
- 每个安全、session、RLS、migration、retry、worker 或 routing wave 必须独立可回滚。
- 不以 feature flag、migration 文件、目录存在、HTTP 200 或“页面可打开”作为 ownership/cutover 通过证据。
- 不将未跟踪 checkout、历史 archive 或实验入口当作当前生产事实。
- 所有部署和文档示例必须引用 secret，不得出现可用凭据；发现历史泄露时走脱敏、轮换和审计专项。

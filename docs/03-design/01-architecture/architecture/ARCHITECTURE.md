# LLM Gateway Go — 当前系统架构

> **事实快照:** 2026-10-01 · 审计基线提交 `3efae99`，变基跟进 origin/main（`4b7224c`，迁移 765/800-802 已按新事实订正）
> **适用对象:** 当前 `main` 工作树中的 `cmd/gateway` 生产入口及其已装配依赖。
> **证据优先级:** 运行时 wiring / Go 代码 / SQL migration / 自动化测试 > 本文档 > 历史审计和路线图。
> **不在本文断言的内容:** 未验证的生产流量、外部 Provider E2E、未启用的 feature flag、规划中的 MCP/A2A/Fusion 能力。
> **本轮重生成方法:** 2026-10-01 全码审计——四域并行子代理（运行时接线/并行实现/功能/文档）+ 主代理对关键 wiring 逐项实证（file:line 抽查复核）。

---

## 1. 阅读方式与状态标记

| 标记 | 含义 |
|---|---|
| `CURRENT` | 当前代码、SQL 或主入口 wiring 已证实。 |
| `CURRENT/PARTIAL` | 能力已接入，但范围、测试或可靠性仍有缺口。 |
| `SHADOW` | 已实现并可灰度/观测，但不应当作 canonical 或默认生产路径。 |
| `NOT-WIRED` | 类型、迁移或目录存在，但没有生产写入/读取 wiring 证据。 |
| `PARALLEL` | 同一功能存在新旧两版实现并存，收敛方案见 [并行实现对比](../parallel-implementations-comparison.md)。 |
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
| llm-gateway-go: cmd/gateway (单进程巨石, 数据面/控制面/管理面同 mux) |
|                                                                  |
|  Data plane: auth -> protocol/IR -> auto route -> dispatch       |
|              queue -> credential route -> resource gate ->       |
|              upstream stream relay                               |
|                                                                  |
|  Control plane: Admin API + embedded Vue SPA + bg workers        |
|                 (~130+ worker) + plugin runtime + hot config     |
|                                                                  |
|  Durable facts: request_logs(分区), usage/ledger, routing/       |
|  session metadata, outbox events                                 |
+-------------+------------------------+---------------------------+
              |                        |
              v                        v
       PostgreSQL                  Redis (可选)
  tenant/provider/credential       URSM state, fp-slots, limits,
  request/usage/audit/session      queues, locks, short-lived state
  (lite 模式: SQLite + 本地文件,    (lite 模式: 摘除)
   两类外部依赖均可免除)
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
| `cmd/gateway` | `CURRENT` | 生产 composition root（main.go ~7800 行 + ~90 个 `main_*`/`*_init` 辅助文件，手工接线无 DI 框架）；装配 HTTP/SSE、DB、Redis、数据面、Admin、worker、插件与停机。 |
| `domains/streaming` + `executors` | `CURRENT` | 主请求处理、协议中继、候选执行、流式响应和错误映射。 |
| `domains/dispatch` | `CURRENT` | 有界 model/credential 队列、forwarder、governor 削峰、tiered failover 与瀑布观测。**自 2026-08-17（AUDIT_24H B2b）起是唯一执行路径**：executor 同步候选循环已删除，`dispatch_v2.enabled` kill-switch 已退役（`domains/dispatch/gate.go:10`），`Execute` 中 pipeline 为 nil 即装配错误。 |
| `domains/pipeline`（v2 Hook Pipeline） | `SHADOW` | Stage+Hook+Phase 阶段管线（26 阶段）。两个独立 flag：`LLM_GATEWAY_V2_ENABLED` 挂平行 `/v2/*` 路由组（main.go:6144）；`LLM_GATEWAY_USE_V2_PIPELINE` 把 4 个 v1 chat 端点 preflight/postflight 包装（main.go:6158）。**均默认 OFF**。 |
| `cmd/gateway-v2` | `PARALLEL` | 独立 Pipeline 演示入口（自述"并行演示，不替换 cmd/gateway"）；生产镜像不构建（`Dockerfile:42` 仅 `./cmd/gateway`），仅 compose dev-research `profiles:["v2"]`。见 P-01。 |
| `admin/` + `web/` | `CURRENT` | 同进程控制面（admin.Handler 265 非测试文件 + Echo 运维子应用）和 Vue 3.5 管理台（Vite 6 + Element Plus，dist 从磁盘目录服务非 embed）。 |
| `bg/` | `CURRENT/PARTIAL` | 304 个 go 文件、装配约 80+ goroutine worker：探测、清理、统计、分区、质量、对账、物化视图。启动条件与停机语义仍需持续收口。 |
| `installer/` | `CURRENT` | 独立 Go module；安装、升级、回滚和 launcher 相关能力。 |
| 存储双模式 `storage/` | `CURRENT` | `LLM_GATEWAY_STORAGE_MODE=lite`（SQLite 三 store + 文件 bodies + L1.5 缓存，禁 PG/Redis，无计费面）/ `full`+热区 / 未设（历史 PG-only）。装配点 `storage_mode_init.go`。 |

**命名陷阱（勿再误判为"v3 管线"）**：`main_v3_wiring.go` = 会话智能压缩 v3 装配（Compressor + SessionCompressor，L1 进程/L2 Redis/L3 request_logs 三层缓存，默认启用）；`main_v32_wiring.go` = V3.2 可观测 SSE provider（QueueProjection→LiveStreamSSEHub 快照闭包）。均与请求管线版本无关。

---

## 3. 默认生产请求路径

```text
/v1/chat/completions   /v1/completions
/v1/messages           /v1/responses
/v1/embeddings         /v1/models
/v1beta/models/*       /v1/models/*  (Gemini native)
```

完整步骤与状态写入时机见 [`runtime-request-flow.md`](runtime-request-flow.md)。当前实测主链（wiring 证据）：

```text
HTTP/SSE (h2c 同端口 HTTP/1.1+2, main.go:7201)
  -> 中间件链 Recovery→RequestID→Locale→CORS→Prometheus→Tracing→Auth→Origin→Logging→SecurityHeaders (main.go:7111-7124)
  -> streaming.ChatHandler.ServeHTTP (handler.go:1917)
  -> serveWithExecutor: 鉴权/RPM/模型策略/auto 路由 (handler.go:2249)
  -> Executor.Execute (executor.go:2114): attempt 预算/continue 检测/身份池 Layer0
  -> executeViaDispatch: T1 总队列→T3 model 队列→T5 credential 队列→forwarder (唯一路径)
  -> Router.PlanCandidates: tier/健康/sticky/压力过滤 + URSM v2 评分 (router.go:220)
  -> FP slot / 并发信号量 / RPM 资源门
  -> upstream.Client(0 内部重试) + pool(identity-bound)
  -> IR/protocol 转换 + vendorstrip + SSE 回写
  -> telemetry request_logs + WAL + requestjourney 观测 (+V2 shadow persist + ASM outbox)
```

可选旁路（均默认 OFF，属 `PARALLEL`/`SHADOW`）：v2 Hook Pipeline 包装（`main.go:6240-6266`）、`/v2/*` 平行路由组、`internal/streamretry` pre-stream 重试包装（与 request_survival 互斥）。

### 3.1 协议与 IR

- OpenAI、Anthropic、Responses、Gemini native、Ollama 已由 `internal/ir` + `domains/transformation` 归一（`CURRENT`）。
- protocol-specific extension 字段必须在转换前后保留或明确记录为不可保真，不能因统一模型而静默丢弃。
- `adapter/unified` 是**已废弃旧尝试**（0 外部引用，`PARALLEL`→待删，见 P-01 附注）。

### 3.2 路由和资源治理

```text
availability / tenant / protocol / model filters
  -> URSM v2 (off/shadow/canary/authoritative 四档, main.go:1067-1205)
  -> tier(1,2,3,9) + billing round + sticky 约束
  -> P2C ordering（边际成本惩罚默认折入综合分, LLM_GATEWAY_ROUTING_W_COST 可关; Bandit 分支 2026-09-19 已删除）
  -> dispatch candidate 执行与 tiered failover
```

具名 scorer（cost/cache/context/headroom）仍主要用于 shadow comparison，不得写成默认 active routing mode。详见 [`routing-and-state.md`](routing-and-state.md)。

**autoroute 代际事实**（`PARALLEL`，见 P-03）：`decision.go`(v1)/`decision_v2.go` 同热路径，`AUTO_ENABLE_V2` 默认 false；`classifier.go`(8 类)/`classifier_v3.go`(10 类，flag `auto_v3_enhanced_classification`)；`scoring.go`/`scoring_v2.go`/`scoring_simplified.go` 三代并存。生产调用点 `domains/streaming/auto_route.go:593`。

### 3.3 重试、流式和安全边界

- Dispatch failover、executor 协议重试、Goal retry、request survival 与可选 stream retry 共同存在。
- stream retry 只处理首字节前可安全重试的故障；首字节后遵循流一致性规则。
- OpenAI-compatible 流式在首帧、empty-stream gate 缓冲与主循环共用 vendor sanitizer；MiniMax `base_resp.status_code` 先检后删，Zhipu/DeepSeek/Doubao 仅各自字段过滤（`internal/vendorstrip`）。Ernie/Baidu 无 strip 接线，`search_info.search_results[]` 透传。
- 非 SSE JSON 仅标准 `error` envelope 或显式顶层 `type`/`code` 视为错误；SSE 单行由 `SSEMaxLineBytes` 按 byte 限断。
- 多层 retry budget/backoff 并存；统一 request-level retry contract 仍为 `TARGET`。

---

## 4. 路由状态、健康和资源分配

推荐状态路径（`CURRENT`）：

```text
Router -> URSM v2 authoritative -> Redis/Lua state -> PostgreSQL persistence/audit
```

兼容路径：`Router -> StateBackend -> LegacyStateBackend / DBOnlyBackend -> legacy credential state`。

**URSM key schema 三代**（`PARALLEL`，见 P-06）：legacy node namespace（部分 admin 读路径仍在用）vs `domains/ursm/v2`（110 importer，含 bootstrap/rollout）vs k2 目标。切换 `URSM_V2_KEY_SCHEMA_MODE=dual|canonical|k2`。迁移工具链：`cmd/migrate-ursm-v2`（生产可用）、`cmd/ursm-k2-preflight`（只读预检）、`cmd/k2-migrate-ursm`（**dry-run only，M5-0/T0 BLOCKED/NO-GO**）。

**健康判断与资源分配不是同一问题**：

| 关注点 | 例子 |
|---|---|
| 健康判断 | credential auth/quota/error state、probe、circuit、URSM availability。 |
| 资源分配 | concurrent slot、RPM/TPM、fingerprint slot、egress identity、queue pressure。 |

已知边界：RPM 已在 admission 路径；TPM 完整接线待核实；FP slot 与 concurrency 分步 acquire/release（原子联合 lease 为 `TARGET`）；Redis 失效降级进程内状态需显式 fail-open/closed 策略。

**探测代际事实**（`PARALLEL`，见 P-02）：新 worker 族（`credential_selfcheck`+`node_probe`+`system_health`）自 2026-07-14 默认接管，开关唯一权威 `internal/probemode`；legacy 5 worker 需 `LLM_GATEWAY_USE_NEW_PROBE_MODE=false` **且** `LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK=true` 双门才启动（`legacy_selfcheck_gate_test.go`），但对象仍构造并注入 admin（如 main.go:5489）。

---

## 5. 数据、审计和会话

### 5.1 当前事实源

| 对象 | 当前状态 | 说明 |
|---|---|---|
| `request_logs_hot` / usage ledger | `CURRENT` | 请求元数据、用量、审计和派生链路主要 durable 入口。分区体系 + 归档/热区/列存（765 起 bodies 月分区列存化，56.6× 压缩；764 tenant+ts 分区索引）。 |
| request WAL | `CURRENT/PARTIAL` | 同步 INSERT + 异步批量 UPDATE；非完整不可变事件历史。 |
| telemetry queue/fallback | `CURRENT/PARTIAL` | 队列满可同步落库；内存 fallback 不能替代 durable outbox。 |
| `gateway.session_*` / V2 writer | `PARALLEL/SHADOW` | V1 主写（失败即请求失败）、V2 影子写（fail-open），flag `sessions_v2.enabled && shadow_write` 热更（`session_v2_init.go`）。`internal/sessionv2mirror` 负责回填/replay。 |
| `request_logs` legacy session/summary paths | `CURRENT` | 仍是 canonical session/message/body 事实与兼容读取来源。 |
| Gateway -> ASM outbox | `CURRENT/PARTIAL` | 事件构造器现役为 `BuildRequestCompletedEventV3`（GW-1.2 schema，注意位于 `event_builder_v1.go`）；legacy V1/V2 构造器仅测试引用。依赖 endpoint/secret 配置，缺配置不派发。 |

**Outbox 模式现存 4 套实现**（`PARALLEL`，见 P-08）：`durable/pending_outbox.go`+`settlement_outbox.go`（任务存储自有）、`internal/outbox/`（ASM 事件投递）、`internal/sessionv2mirror/outbox.go`（V2 镜像回填）。语义（幂等/重放/签名）各自为政，收敛为债务。

### 5.2 Session V2 的准确边界

```text
schema + writer + cache + shadow persistence: CURRENT/SHADOW
primary read + canonical ownership + old fact retirement: MIGRATION-GATE
```

切换治理：`dual_read_validator.go` 行级对账，**7 天零漂移门禁**通过后才允许翻主读。切换前至少需要：body/turn 完整率、hash/tenant 对账、backfill、dual-read、replay、attachment authorization、真实 RLS、跨仓 E2E、观察期和 rollback drill。

### 5.3 迁移体系事实

- `db.ApplyMigrations` = Go 代码幂等 ensure\*（~89 函数）+ 嵌入式数字 SQL（`db/migrations/`，最新 365_goal_client_signal.sql）。
- 运维 SQL 在 `sql/migrations/`：`startup/`（数字序列 7xx 活跃，最新 **765**_bodies_columnar_storage（request_logs_bodies 月分区列存化），另有预留段 800_provider_endpoint_protocols/801_session_turn_details_duplicate_drain/802_session_turn_details_gw_task_id_index；下一枚规划 766，installer embeddata 有镜像副本需同步）+ `domain/`（93 枚）+ local/manual/operations/test。
- `gateway migrate` 子命令零停机迁移旁路（main.go:189）。

---

## 6. 控制面、worker 与交付

### 6.1 Admin 与管理面

- `admin.Handler`（`admin/handler.go:399,898`）挂 `/api/auth/*` + `/api/admin/*` 大部分；另有独立 RegisterRoutes 的 PromptInjection/HealthCheck/SelfCheck/SystemMonitor/DualReadValidator/RequestJourney 等子 handler。
- Echo 运维子应用（main.go:6520-6698）：licensing/fault/autoupdate/center/vibecoding/tenantops，JWT 中间件注入 user/tenant/role。
- 鉴权三级：`admin()`（JWT/API-key）、`superAdmin()`、`AdminAPIKey`（ops_ 前缀静态 token）。`/api/*` 公开端点必须显式 allowlist，不得依赖"开发者记得包 middleware"。
- admin/control 半迁移（`PARALLEL`，P-07）：routing overrides 的 create 已迁 `control/routing/create.go`（CQRS PoC），delete/extend 留 `admin/routing_overrides.go`，同一资源写命令分居两包。

### 6.2 Background runtime

`bg/`（294 文件）+ 域内 worker 装配于 main.go:3626-5780。worker 族：探测/恢复（新族+legacy）、健康追踪（滑窗/自动恢复/并发自扩容/峰值）、统计聚合（分钟/日月汇总/对账）、生命周期清理（分区/VACUUM/blob/审计/handoff/热区/lite 保留）、autoroute 结算/亲和/特征刷新、FreeDiscovery 扫描、质量采集/画像、apihub 同步、goalrun 调度、会话健康/异常采集、物化视图刷新。重点仍是：统一 owner/Start/Stop/timeout/drain/指标；systemd 停机预算与真实 shutdown budget（23s HTTP→5s worker→telemetry→DB pool）对齐。

### 6.3 Maintain、Installer 与部署

- Maintain 负责 license/distribution/activation/upgrade 控制面；Gateway 保留 compatibility proxy（`/maintain-api/*` 反代）与回退。
- h2c 最终 handler 必须覆盖 Maintain proxy 包装（回归测试保护的 wiring 约束）。
- installer 独立 module；M1-M4 部署模式见 [`../../../../06-deployment/README.md`](../../../../06-deployment/README.md)。

---

## 7. 已知生产门禁与优化优先级

### P0 — 切流或扩面前处理

1. `/api/quality/*`、独立 quality service 的认证、tenant scope、RLS 与 canonical data source。
2. legacy admin 默认密码、DB 不可用时 data-plane auth 降级、长期 query-token JWT 的 fail-closed 边界。
3. Maintain 的 tenant policy、FORCE RLS、least-privilege role 与真实 PostgreSQL negative tests。
4. Session V2 shadow persistence 的 durable compensation、reconciliation、backfill、replay 与 rollback 门禁（7 天零漂移 dual-read）。
5. ASM 候选 migration、context secret、DLQ unique constraint、body fetch failure 可靠性审查。
6. h2c 最终 handler 必须包装包含 Maintain proxy/static 的 final handler。

### P1 — 数据正确性与可运维性

1. tenant GUC 缺失时默认 tenant 回退是否改为 fail-closed。
2. session rotation tenant 归属、multimodal charge、TPM admission、FP/concurrency union lease。
3. 统一 retry budget、Retry-After 优先级和跨层最大尝试数。
4. request fallback 与 onPersisted/V2/outbox 派生链的补偿语义。
5. worker lifecycle、shutdown budget、Compose/systemd/health/env 合约漂移。
6. **并行实现收敛**（本轮新增，见 §8 与统一优化提示词方案）：autoroute 三代退役、legacy 探测 worker 保留期、outbox 内核抽取、死代码删除。

### P2 — 架构演进

1. 逐波次按 ADR-0002 收敛 `cmd/gateway` composition root 与包依赖方向。
2. Provider catalog 导入、active request-aware routing、受保护的 Lite/Caveman/RTK compression stage。
3. MCP、A2A、Fusion 仅在 Gateway 保持唯一 provider executor、tenant/limiter/audit owner 的前提下演进。
4. 清理过期链接、历史数字、secret hygiene 和部署文档漂移。

具体代码落点见 [`optimization-roadmap.md`](optimization-roadmap.md)。

---

## 8. 并行实现与收敛债务（2026-10-01 新增）

全仓确认 **10 组新旧并行实现 + 7 处零引用死代码**，完整证据与状态分类见 [`../parallel-implementations-comparison.md`](../parallel-implementations-comparison.md)，逐项可执行的收敛提示词见 [`../unified-optimization-prompts.md`](../unified-optimization-prompts.md)。速览：

| # | 并行对 | 状态 | 风险 |
|---|---|---|---|
| P-01 | cmd/gateway(v1 主用) vs cmd/gateway-v2 + /v2/* + v1 包装桥（三重 v2 暴露面） | 演示/灰度 | 低（显式 OFF）但认知成本高 |
| P-02 | legacy 探测 5 worker vs 新 3 worker | 新版默认接管 | legacy 对象仍装配注入 |
| P-03 | autoroute decision/classifier/scoring 三代 | **双活**（v1 默认） | 高：v2 内部回退 v1，组合爆炸 |
| P-04 | Session V1 主写 vs V2 影子写 | 迁移中 | 高：门禁未过，schema 漂移风险 |
| P-05 | outbox 事件构造器 V1/V2/V3 | V3 接管 | V1/V2 死构造器待删 |
| P-06 | URSM key 三代 schema | 迁移中 | k2 迁移 NO-GO |
| P-07 | admin vs control CQRS 半迁移 | 迁移中 | 同资源写命令分居两包 |
| P-08 | outbox 模式 4 套实现 | 双活 | 幂等/重放语义不一致 |
| P-09 | centeragent HTTP vs 直连 DB | 新默认 | 低 |
| P-10 | 死代码 7 包（internal/probe、orchestration、capabilityscore、ctxpool、agent/wsclient、adapter/unified、examples） | 待删 | 误当活跃代码"修复" |

退役模板先例：IR 传输层（`docs/adr/2026-09-09-ir-transport-layer-retirement.md`）——以"零生产调用点"为证据整体下线。

---

## 9. 三服务和 OmniRoute 边界

| 能力 | Gateway | ASM | Maintain |
|---|---|---|---|
| Provider 调用、流式执行、路由、限流、成本执行 | Owner | 仅消费事实 | 不实现 |
| Canonical request/session/body | Owner，切换前保持旧事实源 | 投影/分析，不保存完整正文 | 不实现 |
| 会话分析、审批、任务、审计投影 | 产生事件/兼容读取 | Owner | 不实现 |
| License、artifact、activation、upgrade | compatibility/consumer | 插件消费者 | Owner |
| MCP/A2A/Fusion transport/execution | 未来 Owner | 只消费 metadata/task facts | 不实现 |

OmniRoute 的公开能力与本项目 ADOPT / CONSUME / REJECT 边界见 [`omniroute-integration-boundary.md`](omniroute-integration-boundary.md)。任何 provider 数量、工具数量、Token 节省比例都必须标为上游声明或验收目标，不能写成当前 Gateway 事实。

---

## 10. 文档与验证入口

- [运行时请求流](runtime-request-flow.md)
- [路由与状态管理](routing-and-state.md)
- [优化路线图与代码指导](optimization-roadmap.md)
- [OmniRoute 集成边界](omniroute-integration-boundary.md)
- [新旧并行实现对比（重点标注）](../parallel-implementations-comparison.md)
- [统一优化提示词方案](../unified-optimization-prompts.md)
- [系统需求规格](../../../../01-requirements/SYSTEM_REQUIREMENTS.md) · [功能特性目录](../../../../01-requirements/functional/FEATURES_CATALOG.md)
- [仓库布局](REPO_LAYOUT.md) · [包布局 ADR](../../../../../adr/ADR-0002-target-go-package-layout.md)
- [测试矩阵](../../../../../05-testing/01-strategy/test-matrix.md)
- [部署入口](../../../../../06-deployment/README.md)
- [父仓拆分与 ownership 门禁](../../../../../../../docs/拆分/README.md)

---

## 11. 变更纪律

- 代码变更先补对应 regression/integration test，再进入灰度。
- 每个安全、session、RLS、migration、retry、worker 或 routing wave 必须独立可回滚。
- 不以 feature flag、migration 文件、目录存在、HTTP 200 或"页面可打开"作为 ownership/cutover 通过证据。
- 不将未跟踪 checkout、历史 archive 或实验入口当作当前生产事实；**不将死代码/演示入口当作活跃路径修复**（IR 传输层教训）。
- 所有部署和文档示例必须引用 secret，不得出现可用凭据；发现历史泄露时走脱敏、轮换和审计专项。
- 并行对收敛必须走"影子验证→门禁→删旧→登记"流程，删除以零生产调用点 grep 证据为准。

# 02 · 设计 vs 代码对照（Code-vs-Design Deltas）

> **目的**：把 [`docs/03-design/01-architecture/architecture/ARCHITECTURE.md`](../03-design/01-architecture/architecture/ARCHITECTURE.md)、[`optimization-roadmap.md`](../03-design/01-architecture/architecture/optimization-roadmap.md)、[`routing-and-state.md`](../03-design/01-architecture/architecture/routing-and-state.md)、[`omniroute-integration-boundary.md`](../03-design/01-architecture/architecture/omniroute-integration-boundary.md)、[`REPO_LAYOUT.md`](../03-design/01-architecture/architecture/REPO_LAYOUT.md)、[`armor-sdp-feasibility.md`](../03-design/01-architecture/architecture/armor-sdp-feasibility.md)、[`会话优化v4/CONTRACT_FREEZE_2026-08-22.md`](../会话优化v4/CONTRACT_FREEZE_2026-08-22.md) 的承诺 / 设计声明 / 状态标记，与实际代码现状对照，列出 34 处差距（D1–D27 起草与复审；2026-08-25 四向审计增补 D28–D34，见 §8）。
> **行数口径**：文中行数为 2026-08-23 起草快照（2026-08-24 复核有小幅增长，见 D1–D5 括注）；不要把快照数字当作长期事实。
> **标记含义**：
> - ✅ = 代码已实现、文档已声明、回归测试覆盖；
> - ⚠️ = 代码局部实现或文档描述为 `CURRENT/PARTIAL`，仍缺独立可回滚证据；
> - ❌ = 文档承诺但代码未实现，或文档明确标 `SHADOW` / `TARGET` 但被误用为生产路径；
> - ❓ = 设计意图清晰但工作树无任何 wiring（`NOT-WIRED`）。
> **不在范围**：不重写既有设计文档，不修改 SQL 与 Go 代码；只列出差距与回归要求。

---

## 1. 顶层结构与单文件债务

| # | 差距 | 设计声明 | 代码现状 | 标记 | 回归要求 |
|---|---|---|---|---|---|
| D1 | `cmd/gateway/main.go` 单文件超大 | `REPO_LAYOUT.md` 引用 `docs/refactor-plans/main-go-split.md` 指出拆分需求 | 起草快照 6076 行（2026-08-24 复核 6156 行），35+ 注释段（auth / 路由 / 队列 / Admin API / worker 启动等）共存；阅读与重构门槛极高 | ⚠️ | V6-W0：拆分为 `cmd/gateway/init/{auth,telemetry,router,admin,workers}.go`，每文件 < 600 行；`func main()` 仅做 wiring；现有行为零变更；`go test ./...` 与 `golangci-lint run` 双绿 |
| D2 | `domains/streaming/handler.go` 8266 行单文件 | `ARCHITECTURE.md` §3 默认生产 v1 handler | 起草快照 8266 行（复核 8329 行）Chat/Messages/Responses/Embeddings 共存；`anthropic_bridge.go` 1566 行、`responses_bridge.go` 888 行、`stream.go` 1686 行分别承载 | ⚠️ | V6-W1：以"协议入口 → handler adapter → 公共 lifecycle"三段拆；回归 5 协议等价性 |
| D3 | `domains/streaming/executors/executor.go` 3308 行 | `ARCHITECTURE.md` §3 Executor | 起草快照 3308 行（复核 3342 行）attempt 循环 + 协议分支 + 资源 acquire/release + retry policy + outcome 收敛 | ⚠️ | V6-W1：拆为 `executor_attempt.go` / `executor_outcome.go` / `executor_protocol.go`；attempt 循环骨架抽公共函数 |
| D4 | `admin/routing.go` 4980 行 | `ARCHITECTURE.md` §6 Admin 控制面 | 起草快照 4980 行（复核 5292 行）routing 端点 + override + tuning + 候选重排 + 自动模式 + dashboard 统计 | ⚠️ | V6-W0：以"端点族"分组拆分；不改 URL；保留 deprecation 注释 |
| D5 | `db/db.go` 4358 行 | `REPO_LAYOUT.md` 列为平台基础件 | 起草快照 4358 行（复核 4451 行）DB 连接 + pool + 事务工具 + 健康检查 + tracing 注入 | ⚠️ | V6-W0：拆分为 `conn.go` / `pool.go` / `tx.go` / `health.go` / `tracing.go` |
| D6 | `bg/node_probe.go` 2270 行、`bg/credential_recovery.go` 1517 行、`bg/credential_selfcheck.go` 等"巨型 worker" | `optimization-roadmap.md` P1.5 worker lifecycle | 单文件 1000+ 行的 worker 仍然常见；supervisor 未抽离 | ⚠️ | V6-W0：抽出 `bg/supervisor/` 子包（`BackgroundSupervisor` + Start/Stop + ctx + WaitGroup + panic policy）；现 worker 接入；`deploy/llm-gateway-go.service` 与 systemd timeout 对齐 |

---

## 2. 重叠与并行实现

| # | 差距 | 设计声明 | 代码现状 | 标记 | 回归要求 |
|---|---|---|---|---|---|
| D7 | **凭据/健康职责重叠** | `REPO_LAYOUT.md` 把 `domains/credential*` 列为核心、`credentialfpslot/`、`credentialhealth/` 列为控制面 | `domains/credentialstate/`（manager 4000+ 行）↔ 顶层 `credentialfpslot/slot.go` ↔ `credentialhealth/checker.go` 三棵树并存；同一概念在多文件实现 | ⚠️ | V6-W0：基于代码依赖图识别真实所有权；用 `domains/credential/{state,health,quota,limiter,decrypt_cache}` 取代并行树，保留顶层只作为兼容 facade |
| D8 | **路由三套并行实现** | `routing-and-state.md` §1 URSM authoritative / Legacy / DBOnly / Shadow 四种 backend | `domains/routing/`（sticky / round_robin / weighted）↔ `domains/routingstate/`（coordinator / shadow_observer）↔ `autoroute/decision_v2.go`；`autoroute/scoring.go` / `scoring_new.go` / `scoring_simplified.go` 三套并存 | ⚠️ | V6-W1：明确"URSM v2 为唯一 owner"，把 legacy 移到 `internal/_legacy/routing_legacy.go`，scoring 收敛为单实现 + unit test 覆盖 8 维度 |
| D9 | **两套 Migration 系统** | `REPO_LAYOUT.md` §顶层文件同时引用 `db/migrations/` 与 `sql/migrations/` | `db/migrations/` 数字版本（最后 363）+ `sql/migrations/` 分类目录（最后 083 + sub/`startup`/`timeout-optimization`）并存；不同子目录的加载顺序可能不同 | ⚠️ | V6-W0：以"主仓 + 启动种子"明确分层；`db/migrations/` 作为权威（与 Go 内嵌加载器一致），`sql/migrations/` 仅保留 `startup/` 种子；其余历史迁移冻结为 `sql/migrations/archive/2026-Q3/`；不删任何文件，只标记 |
| D10 | **Session V2 双 owner 风险** | `optimization-roadmap.md` P1.1 V2 shadow reconciliation | `domains/session/` + `internal/sessionv2mirror/` + `domains/session/v2/` + `cmd/gateway/session_v2_init.go` 并存；`request_logs` legacy 仍是 canonical | ⚠️ | V6-W1：明确 V2 在 V6 周期内仍为 `SHADOW`，不动 production read-primary；新增 `dual_read_validator.go` 的 7 天 freshness 指标；不在 v6 内做 ownership 切换 |
| D11 | **Plugin runtime 与 Goal orchestration 双 driver** | `docs/04-implementation/plan/2026-08-19-unified-auto-orchestration-plugin-execution-plan.md` W0-A 冻结"GoalRun 不能成为第二个 durable task owner" | `plugin-runtime/` 完整（registry / sandbox / binding / manifest / health_loop），`domains/goalrun/` Wave 2-A 已落库（CAS + lease + 13 状态）；两条 driver 平行存在 | ⚠️ | V6-W2：以"binding surface" 收敛 plugin-runtime 与 goalrun 的暴露面；明确"plugin handle 路由 / 限速 / 鉴权；goalrun handle 步骤账本 / 状态机 / 续约"；不互相 import |

---

## 3. 安全与可观测债务

| # | 差距 | 设计声明 | 代码现状 | 标记 | 回归要求 |
|---|---|---|---|---|---|
| D12 | **PII 脱敏仍处于 Q4 B2 实施清单** | `armor-sdp-feasibility.md` 6.1–6.10 + M1–M4 里程碑 | `security/armor/` judge.go 已有；Presidio sidecar 镜像、Go SDPClient、SDPStreamSanitizer 流式缓冲、k3s Deployment、zh_pii_rules 包均未在 main 工作树落地（仅文档与 NOW-2 commit `ecea00cc` 引用） | ❌ | V6-W3：完整落地 §6 实施清单 6.1–6.10，2 周 observe mode FPR/FNR；这是 v6 唯一允许做"安全模块首次上线"的窗口 |
| D13 | **Maintain/ASM tenant RLS 仅声明** | `optimization-roadmap.md` P0.3 Maintain / ASM 迁移安全 | Maintain `internal/migrations`、`internal/httpapi`、`internal/store` 已有目录，但 `FORCE RLS`、NOBYPASSRLS role、tenant GUC、跨租户 negative test 均未实证 | ❌ | V6-W3：补全 Maintain tenant policy + role + GUC；在隔离 canary 跑 tenant A/B、regular/admin/super-admin 矩阵 negative test；输出矩阵表到 `04-implementation/changes/` |
| D14 | **Tenant GUC 缺省默认回退** | `optimization-roadmap.md` P1.1 第 1 条 | tenant GUC 缺失时默认 tenant 回退（fail-open）；未审计哪些路径会受影响 | ⚠️ | V6-W3：枚举所有 `current_setting('app.tenant_id', true)` 调用点；对高安全接口（`/api/admin/*`、Admin fallback、Maintain proxy）改为 fail-closed；其他保留 fail-open 但打 `WARN` 日志 |
| D15 | **Quality API auth/tenant scope 不闭合** | `optimization-roadmap.md` P0.1 | `/api/quality/*` 与独立 quality service 可能用不同数据源；`internal/quality/api.go` 与 `internal/handlers/quality_handler.go` 共存；未公开 allowlist | ❌ | V6-W3：确定 `provider_profile_daily` 或统一 repository 为 canonical source；quality-service 仅内网或 service auth；与 admin/auth.go 共享 tenant wrapper |
| D16 | **OTel 缺 GenAI semantic conventions** | `ARCHITECTURE.md` §2 OTel collectors 接入 | `internal/observability/` 已存在；当前 OTel span 属性为内部命名（`autoroute.classify.duration_ms` 等），未对齐 `gen_ai.*` 语义约定 | ⚠️ | V6-W5：在 OTel span 上引入 `gen_ai.system` / `gen_ai.request.model` / `gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens` / `gen_ai.response.finish_reasons` 等；保留内部属性做兼容；统一在 `internal/observability/semconv.go` 单点 |

---

## 4. 协议扩展与编排债务

| # | 差距 | 设计声明 | 代码现状 | 标记 | 回归要求 |
|---|---|---|---|---|---|
| D17 | **MCP / A2A / Fusion 仅概念** | `omniroute-integration-boundary.md` §6–§8 明列 ADOPT 顺序 | 目录 `plugin-runtime/`、`apihub/`、`metatools/` 存在；MCP server JSON-RPC transport、A2A AgentCard JSON、`fusion/` coordinator 全部未在生产路径 | ❌ | V6-W4：按"stdio read-only → tools/call → HTTP → SSE"四步落地 MCP；A2A 仅做同步 `message/send` + persisted task 试点；Fusion 仅 Opt-in non-stream + judge-1 模式；每步独立 commit、独立 staging 验证 |
| D18 | **h2c + Maintain proxy final handler 闭环** | `optimization-roadmap.md` P0.2、realtime-flow-fixes audit 2026-08-21 | h2c 包装器存在（`cmd/gateway/main.go`），但 Maintain proxy 与 legacy `/api/*` 是否被 finalHandler 覆盖未独立证据 | ⚠️ | V6-W0：补集成测试覆盖 HTTP/1.1 + h2c 两条路径访问 `/maintain-api/*`、`/v1/*`、`/api/*`；保留旧 fallback 不删，但增加新 smoke 套件 |

---

## 5. 数据库 / SQL 与"事实源"债务

| # | 差距 | 设计声明 | 代码现状 | 标记 | 回归要求 |
|---|---|---|---|---|---|
| D19 | **`request_logs_hot` 与 `request_logs` legacy 双写** | `runtime-request-flow.md` §5 持久化时序 | `request_logs_hot` 为热路径主入口，`request_logs` legacy 仍承担 canonical session/message/body；telemetry fallback 可能不触发 `onPersisted` 派生链 | ⚠️ | V6-W2：定义 `RequestFinalizer` 单一入口；DB/telemetry 失败走 durable outbox；V2/ASM 派生走 `onPersisted` 单一 hook；定义"双写对账"指标 |
| D20 | **统计对账 worker 已有但默认关闭、覆盖不全** | `stats-reconciliation.md` 12 KB | `bg/cost_reconciliation_worker.go` 已存在（2026-08-24 复核）：由 `LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED`（或 settings `provider_profile.cost_reconciliation.enabled`，默认 **false**）开关，interval 默认 86400s（`LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_INTERVAL`）；admin 端点为 `/api/admin/provider-cost-reconciliation`（月度查询 + `/bill` 手动导入账单）；结果落 `provider_cost_reconciliation` 表，差异超阈值写 `provider_events`。未覆盖 `stats-reconciliation.md` 的全量口径，生产默认未启用 | ⚠️（`LOCAL_VERIFIED`：worker 单测存在；生产启用与 dashboard 未实证） | V6-W2：扩到 stats-reconciliation 全量口径；生产灰度启用；对账报告进 admin dashboard |
| D21 | **`urgency_message` / `error_classification_v1.json` 维护策略** | `CONTRACT_FREEZE_2026-08-22.md` §4 "dispatch 子集冻结，其他开放" | frozen subset 11 个；open 部分未治理 | ✅ | 不动 v4 契约；V6-W2 在 `_test.go` 加 frozen-subset 回归（已完成） |
| D22 | **handoff / goal durable at-rest encryption** | `ADR-0001-handoff-goal-state-at-rest-encryption.md` | 文档齐全；`KMS/Vault` 接入与 rotate 流程未明确 | ⚠️ | V6-W3：把 KMS envelope + rotate runbook 写到 `docs/06-deployment/04-runbooks/kms-rotate.md`；不引入新依赖 |

---

## 6. 运维 / 部署 / 文档债务

| # | 差距 | 设计声明 | 代码现状 | 标记 | 回归要求 |
|---|---|---|---|---|---|
| D23 | **零停机部署实测不全** | `deploy/zero-downtime-design.md` | 设计文档齐全；systemd timeout vs Gateway 真实 drain budget 未对齐；compose / K3s 行为可能不同 | ⚠️ | V6-W2：补 drain matrix（systemd + compose + k3s） runbook；定义最长 drain 时间与回滚判定 |
| D24 | **`cmd/gateway-v2` 与 `/v2/*` 与 v1 wrapper 三路径并存** | `runtime-request-flow.md` §2 运行模式表 | v2 仅作为 Pipeline 验证入口；覆盖度统计缺 | ⚠️ | V6-W2：在 `cmd/gateway-v2/main.go` 加 banner 警告"非生产路径"；coverage 报告对 v2 路径单独标 |
| D25 | **文档漂移与"过期数字"** | `optimization-roadmap.md` §P2.4 清理过期链接 | 部分历史 CHANGELOG、ADR、README 数字未刷新 | ⚠️ | V6-W5：跑 `docs-link-checker`（CI）+ 数字审计；过期链接打 `[DEPRECATED]`，数字加 `[STALE 2026-Q2]`，3 个月内删除 |
| D26 | **`tenantops/` 单文件 stub** | `REPO_LAYOUT.md` | `tenantops/handler.go` + 测试一个文件；属 stub | ⚠️ | V6-W4：明确"v6 不投入租户运维扩展"；要么补内容要么删除（ADR 决策） |
| D27 | **`observability` 并入 `deploy/` 已声明但未落实** | `REPO_LAYOUT.md` §顶层目录最后一行 | `observability→已并入 deploy` 的目录仍存在 | ❌ | V6-W4：迁移完成（一次性 commit）或撤回声明 |

---

## 7. 差距汇总（按波次）

| 波次 | 重点差距 | 目标 |
|---|---|---|
| **V6-W0** 结构与门禁 | D1 / D4 / D5 / D6 / D7 / D9 / D18 | 拆 main.go 与超大文件；bg supervisor；Maintain final handler smoke；migration 单仓策略 |
| **V6-W1** 契约与正确性 | D2 / D3 / D8 / D10 | executor / handler 拆分；URSM 单一 owner；V2 shadow validator |
| **V6-W2** 编排与持久化 | D11 / D19 / D20 / D23 / D24 | plugin/goalrun binding surface；`RequestFinalizer`；cost reconciliation worker；drain matrix；v2 banner |
| **V6-W3** 安全与合规 | D12 / D13 / D14 / D15 / D16 / D22 | Presidio sidecar + 中文规则包上线；Maintain RLS negative test；GUC fail-closed；quality canonical source；OTel GenAI semconv；KMS rotate runbook |
| **V6-W4** 协议扩展 | D17 / D26 / D27 | MCP stdio→HTTP→SSE；A2A message/send 试点；Fusion Opt-in；tenantops / observability 治理 |
| **V6-W5** 智能与文档 | D16 / D25 | Cascade router + semantic cache（来自 [`04`](04-hot-ideas-and-divergent-suggestions.md)）；docs-link-checker + 数字审计；SLO 全面落地 |

| **增补**（2026-08-25，映射见 §8） | D28 / D29 / D30 / D31 / D32 / D33 / D34 | hot 8h 存储闭环（W2）；内存精简 LP4（W1）；泄漏清零（W2）；导入版本管理（W2）；位宽重排（W3）；网络 watchdog（W3）；前端统一（W2 前端专项） |

详见 [`03-roadmap-v6-waves.md`](03-roadmap-v6-waves.md)。

---

## 8. 增补差距（2026-08-25 四向审计：存储闭环 / 内存 / 健壮性 / 前端一致性）

> 背景：v6 范围扩展四项要求——流程闭环与数据来去、上传解析与版本管理、多线程竞争、溢出、网络可靠性；观测与 UI 一致性；内存对象精简；**hot+分区（columnar）8 小时存储架构**。本节基于四路子代理审计（证据均到文件:行），并参考 24h 内方案文档：`2026-08-24-session-queue-memory-optimization-plan.md`（LP1–LP3 已完成）、`2026-08-24-lp5-body-storage-schema-baseline.md`（LP1 未 apply）、`2026-08-24-dispatch-race-investigation.md`（已由 `9cde12a79` 修复）、`2026-08-24-session-summary-token-ratio-overflow.md`（已修）、`docs/handoff/20260824-001000-dashboard-queue-audit/`（UI 基线）。

| # | 差距 | 目标架构 | 代码现状（证据） | 标记 | 本轮动作 / 回归要求 |
|---|---|---|---|---|---|
| D28 | **hot 8h 统一与存储闭环缺口** | 所有大数据表 = hot（可 UPDATE/DELETE）+ 月度 columnar 分区；hot 只留 8h；后台批量转移 | `bg/partition_manager.go` 统一调度（promote 1h 周期），但默认保留 24h（已修→8h）；`request_logs_hot` 仍带 3 个 body jsonb 列（LP1 未 apply，双写+TOAST 放大，见 LP5 基线报告）；`request_logs_bodies` 分区已被 startup/562 改回 heap（columnar 不可 UPDATE/DELETE 与 body 覆盖写冲突）；`dashboard_access_events_hot`/`session_module_executions_hot` **无 Go 转移 worker**（仅可选 pg_cron / admin 手动，保留 30d/7d）；`internal/titlestore/store.go:333` 对分区表 `sessions` UPDATE | ⚠️ | 已修：默认保留 24h→8h（`bg/partition_manager.go` + `settings/spec_lifecycle.go`，`lifecycle.hot_retention_hours` 可覆盖）。V6-W2：apply LP1 drop body 列；两表补 Go worker（注册 `promoteSpecs`）；title 写入迁离分区表；重评 562（更新/删除收敛 hot 后分区恢复 columnar） |
| D29 | **同一对象多处存储 / 过度复制** | 一对象一处存储；减少复制与状态更新成本 | 双 waterfall 环：`domains/dispatch/pipeline.go:1057` 写 Pipeline 自有 ring（仅测试读），随即 `buildWaterfallRequest` 二次构建发给 projection ring（生产唯一读者）——每请求 2 次样本构建 + 200 条 × Attempts 深拷贝；`queue_projection.go:353` SnapshotWaterfall 锁内再调 `Snapshot()` 重排序重分配且副作用 `sourceVersion.Add(1)`；V1/V2 会话 L1 缓存并存（`cmd/gateway/main.go:2027/2044`，切换期双份）；streaming 终态全 body 多份（LP3 已决策保留快照拷贝） | ⚠️ | V6-W1（新增 LP4）：删 Pipeline 侧 ring + projection 收样转移所有权免 clone；SnapshotWaterfall 锁内直建 lanes；V1 L1 降容或加开关 |
| D30 | **无淘汰累积（内存泄漏）** | 有界内存 | 已修：`domains/streaming/response_interceptor_helpers.go` follow-up 计数器（原 `cleanup` 从未接线，每 sessionID 永驻）→ 加 lastActive + 惰性清扫 + 回归测试；`admin/logs_summary.go` sessionSummaryCache（key 含 log_count，轮询即新增，过期不删）→ Store 时清扫。残留：`internal/sessionv2mirror/backlog.go:25` cap 10000 但每项含完整 body（V2 长故障可驻 GB 级）；`admin/live_stream_sse.go:447` 4 个 label sync.Map 改名后永陈旧 | ⚠️ | 已修 2 处（本轮提交）。V6-W2：backlog 缩容为 RequestID+错误摘要；label map 加失效 |
| D31 | **上传/导入链路缺版本管理与总量上限** | 解析→存储→版本管理闭环；总量有界 | 附件链路良好（sha256 内容寻址 + 原子写 + safeJoin）；但 pricing CSV 导入（`admin/pricing.go:406`）直接 UPDATE 无 dry-run/快照/回滚/错误清单，错误行静默丢弃；`CleanupOrphanedAttachments`/`CleanupOldAttachments`（`domains/attachments/storage_manager.go:107/138`）**无调用方**，孤儿文件与 `.tmp-*` 永不清理；`GenerateUploadToken` 死代码 | ❌ | 已修：两个导入端点加 `http.MaxBytesReader` 16MiB 总量上限。V6-W2：导入 dry-run + 审计记录 + 失败清单；孤儿清理接线 bg worker；删除死代码 |
| D32 | **打包位宽回绕与溢出边界** | 数据结构与参数无溢出可能 | `domains/nodestatecache/stats.go` `LastUsedUnixSec` int32（2038 回绕，打包布局 word0 约束；读者 `probe_candidates.go:54` 已正确符号扩展）；`resources.go` `minuteWindow` uint16（45 天回绕→分钟窗口误重置，良性）；已修：retry `1<<attempt` 无 cap（`handler.go`，attempt≥31 溢出）→ cap 20；`admin/tool_policy_api.go` calls==0 NaN% → 判零；token ratio 溢出（24h 文档已修） | ⚠️ | 已修 2 处 + 2 处加契约注释。V6-W3：如需 2038 安全，重排 word 布局（LastUsedUnixSec→相对秒或 64 位 word2）+ 全读者同步 |
| D33 | **网络可靠性收尾** | 全链路超时/重试/退避有界 | 基线良好（`upstream/client.go` ResponseHeaderTimeout/TLS/Dial/连接池齐备；内部重试=0 防放大；指数退避+jitter；Retry-After 解析）。缺口：`http.Client` 无整体 Timeout 且 chat/gemini 流读无 stall watchdog（仅 anthropic 有）；`executor_chat.go:135` 非流式 ReadAll 无 read deadline；`internal/outbox/delivery.go:87` 固定 10s 与可配超时不一致；`webcookie/base.go:75` 120s client 流式/非流式混用 | ⚠️ | 已修：Gemini 入口 body 32MiB 上限（对齐 pipeline 入口）。V6-W3：通用流读 idle watchdog；统一 outbox 超时配置；webcookie 流式拆 client |
| D34 | **前端展示统一性 / 菜单 / 交互规范** | 复用同组控件；菜单易用；交互一致 | 9 个手写 Drawer（vs 仅 2 处 el-drawer）；手写分页 ≥10 处（vs el-pagination 5 处）；状态徽章 4 套 + 6 处内联映射；空态 15+19 处混用；`ElMessage.error` 97 次直接调用（`resolveApiError` 仅 7 文件采用）；`toLocaleString` 内联 144 处（vs `useFormat` 11 处）；菜单新旧前缀并存（`/routing-v2` vs `/routing/`）；**性能指标上报缺失**（错误上报已有，`errorReporter.ts` 可复用其通道）；基线：`usePersistedValue` 已统一 6 个 localStorage 入口 | ⚠️ | V6-W2（前端专项，注意与他人 web WIP 协调）：抽 `DetailDrawer.vue` 迁 9 抽屉；`usePagination` + 统一空态/分页；推广 `resolveApiError`；补 web-vitals 上报；菜单前缀收敛 |

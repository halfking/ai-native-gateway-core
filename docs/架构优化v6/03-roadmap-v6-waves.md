# 03 · v6 路线图（V6-W0 → V6-W5）

> **目的**：把 [`optimization-roadmap.md`](../../03-design/01-architecture/architecture/optimization-roadmap.md) 的 P0/P1/P2 与 [`02-code-vs-design-deltas.md`](02-code-vs-design-deltas.md) 的 18 处差距，重映射为 v6 的 6 波次执行路径。
> **执行原则**：
> 1. 代码 / SQL / runtime wiring 是事实源；schema、目录、feature flag 不是完成证据。
> 2. 每个波次先写 regression / integration test，再改代码；独立 commit、独立回滚。
> 3. 安全 / 租户 / RLS / body / session / retry / 计费问题优先于新增 MCP / A2A / Fusion。
> 4. 不动 v4 已冻结契约（5 identities / 4 lanes / 3 lifecycle / 11 error_kinds）。
> 5. 任何波次未达"完成标准"前不得宣布完成。

---

## 0. 波次全景

| 波次 | 主题 | 优先级 | 准入 | 退出 |
|---|---|---|---|---|
| **V6-W0** | 结构 / 门禁 | P0 | 现状盘点完成 | main.go / admin/routing.go / db.go / bg 拆分完成；migration 单仓；Maintain final handler smoke；每个改动有独立可回滚 commit |
| **V6-W1** | 契约 / 正确性 | P0 | V6-W0 完成 | executor / handler 拆分；URSM 单一 owner；V2 shadow validator；retry budget 统一 |
| **V6-W2** | 编排 / 持久化 | P1 | V6-W1 完成 | plugin / goalrun binding surface；`RequestFinalizer`；cost reconciliation worker；drain matrix runbook；v2 banner |
| **V6-W3** | 安全 / 合规 | P0 | V6-W2 完成 | Presidio sidecar + 中文规则包上线；Maintain RLS negative test；GUC fail-closed；quality canonical source；OTel GenAI semconv；KMS rotate runbook |
| **V6-W4** | 协议扩展 | P2 | V6-W3 完成 | MCP stdio → HTTP → SSE；A2A message/send 试点；Fusion Opt-in；tenantops / observability 治理 |
| **V6-W5** | 智能 / 文档 | P2 | V6-W4 完成 | Cascade router + semantic cache；docs-link-checker；SLO 全面落地 |

> v6 整体周期建议：V6-W0 ~ V6-W3 各 2-3 周；V6-W4 4 周；V6-W5 2-3 周；总计 ~16 周（约 4 个月）。所有周次可并行 W2-W3 部分独立子任务（详见各自"并行项"）。

---

## 1. V6-W0 · 结构 / 门禁（2-3 周）

### 1.1 目标

降低阅读与重构门槛，让"打开仓库第一眼能看懂"。

### 1.2 必做

| # | 任务 | 允许文件 | 验收 |
|---|---|---|---|
| W0-1 | `cmd/gateway/main.go` 拆分（6076 行 → < 600 行/文件） | 新增 `cmd/gateway/init/{auth,telemetry,router,admin,workers,storage,middleware,quality,shutdown}.go`；`main.go` 仅留 wiring + signal handling | `go test ./...` + `golangci-lint run` 双绿；行为零变更；冷启动 < 3s 增量 < 5% |
| W0-2 | `admin/routing.go` 拆分（4980 行） | 按端点族分组：override、tuning、reorder、auto-route、dashboard、stats；保留 URL；标记 `@deprecated` 注释 | 同 W0-1 |
| W0-3 | `db/db.go` 拆分（4358 行） | 拆为 `conn.go` / `pool.go` / `tx.go` / `health.go` / `tracing.go` | 同 W0-1；事务包装接口保持兼容 |
| W0-4 | 抽出 `bg/supervisor/`（`BackgroundSupervisor`） | 新增 `bg/supervisor/{supervisor.go,worker.go,signal.go}`；现有 worker 接入（node_probe / credential_recovery / model_quality / partition_manager / quality_collector / freequotacleanup / systemmonitor） | supervisor 控制 goroutine 数 + WaitGroup；SIGTERM 时按 worker 优先级反序停止；新增 `bg_supervisor_*` Prometheus 指标 |
| W0-5 | h2c + Maintain proxy + final handler smoke | 新增 `tests/integration/h2c_final_handler_test.go`；覆盖 `/maintain-api/*`、`/v1/*`、`/api/*` 在 HTTP/1.1 与 HTTP/2 两路径 | integration test PASS；不依赖真实 provider |
| W0-6 | Migration 单仓策略 | `sql/migrations/archive/2026-Q3/` 冻结非 `startup/` 子目录历史迁移；`db/migrations/` 明确为权威加载源；`deploy/sql/schemas/baseline/` 与 `sql/schema/` 双副本说明文档更新（`docs/03-design/04-data-design/migrations/README.md` 增补） | 加载顺序单元测试；启动时序列号输出到 INFO log |
| W0-7 | 凭据/健康职责重叠识别 | 新增 `docs/04-implementation/analysis/credential-health-ownership-map.md`：基于 import graph 列出每个 owner；不重构，仅识别 | 文档落盘；后续 W1-2 收敛 |
| W0-8 | 路由三套并行实现识别 | 新增 `docs/04-implementation/analysis/routing-impl-ownership-map.md`：基于代码依赖图识别 legacy / state / autoroute 三者边界 | 文档落盘 |
| W0-9 | `cmd/gateway-v2` 与 `/v2/*` 与 v1 wrapper 边界 banner | 在 `cmd/gateway-v2/main.go` 加显式 banner："非生产路径（Pipeline 验证入口）"；`/v2/*` 路径返回头加 `X-Gateway-Mode: shadow` | README 标注；不影响 v1 |

### 1.3 禁止

- 不修改任何生产路径行为。
- 不修改 SQL 迁移内容（仅冻结目录）。
- 不引入新外部依赖。

### 1.4 退出条件

- 全部 W0-1 ~ W0-9 完成并通过验收。
- `docs/04-implementation/changes/2026-MM-DD-v6-w0.md` 落盘（含 commit / 拆分前后 line count / 测试结果 / 性能 baseline 对比）。
- 在 staging 环境跑 `load_test.sh` 增量 P99 < 5%。

---

## 2. V6-W1 · 契约 / 正确性（2-3 周）

### 2.1 目标

把"读起来头大、跑起来飘忽"的核心执行器与路由层切成清晰可回归的形态。

### 2.2 必做

| # | 任务 | 允许文件 | 验收 |
|---|---|---|---|
| W1-1 | `domains/streaming/executors/executor.go` 拆分 | 拆为 `executor_attempt.go` / `executor_outcome.go` / `executor_protocol.go` / `executor_lifecycle.go` | 5 协议等价性回归；attempt 循环骨架公共函数化 |
| W1-2 | `domains/streaming/handler.go` 拆分 | 拆为 `{chat,messages,responses,embeddings,gemini}_handler.go` + `lifecycle.go` + `common.go` | 各协议 `_test.go` PASS |
| W1-3 | URSM 单一 owner | `domains/routing/` 与 `domains/routingstate/` 收敛到 `domains/ursm/v2/`；legacy sticky / weighted / round_robin 移到 `internal/_legacy/routing_legacy.go`（仅 LegacyStateBackend 引用） | 单测 + 集成；URSM authoritative 路径回归 |
| W1-4 | `autoroute/scoring*` 三套收敛 | 保留单一 `scoring.go`；`scoring_new.go` 与 `scoring_simplified.go` 单元测试迁入 + 加 `@deprecated`；`autoroute/decision_v2.go` 明确为唯一入口；`decision.go` 走 deprecation | 单测覆盖 8 维度 + 集成 |
| W1-5 | 统一 RetryBudget | 落地 [`routing-and-state.md`](../../03-design/01-architecture/architecture/routing-and-state.md) §6 `RetryBudget` struct；stream retry / dispatch / goal / survival / failover 五层只读写同一 budget；attempts / deadline / first_byte / credential_retries / model_switches / last_reason / retry_at 全字段填齐 | 跨层 attempts 上限不超 MaxAttempts；client cancel / DB 失败正确释放；attempts 对账日志 |
| W1-6 | Session V2 shadow validator | `cmd/gateway/dual_read_validator.go` 已存在；增强为按租户事务读 + 7 天 freshness 指标 + body/turn/token/cost/hash 完整率；不切换 ownership | 7 天滚动 P50/P95 freshness；字段完整率 ≥ 99% |
| W1-7 | Pipeline wrapper 与 v1 handler 等价性测试 | `tests/integration/pipeline_v1_equivalence_test.go`；同入参同出参 | 关键 case 通过 |
| W1-8 | Candidate lease coordinator（联合 FP slot + concurrency + RPM） | 新增 `domains/ursm/v2/lease/coordinator.go`（参考 [`routing-and-state.md` §5 联合 lease](../../03-design/01-architecture/architecture/routing-and-state.md)）；复用 `bg/probe_queue` lease token / heartbeat / zombie rescue | acquire/release 对称；exactly-once release |

### 2.3 禁止

- 不切换 Session V2 owner（仍 SHADOW）。
- 不修改 v4 冻结契约。
- 不新增 protocol-specific executor（除非 W4 范围内）。

### 2.4 退出条件

- 全部 W1-1 ~ W1-8 完成。
- attempts / lease / V2 freshness 三个面板在 Grafana 上线。
- `docs/04-implementation/changes/2026-MM-DD-v6-w1.md` 落盘。

---

## 3. V6-W2 · 编排 / 持久化（2-3 周）

### 3.1 目标

让"写出去的事实"和"读出来的事实"是同一份，并且可重放、可对账。

### 3.2 必做

| # | 任务 | 允许文件 | 验收 |
|---|---|---|---|
| W2-1 | 抽出 `RequestFinalizer` | 新增 `domains/streaming/finalizer.go`；`request_logs_hot` + usage + onPersisted hooks + V2 mirror + ASM outbox 全部走单一入口 | 单一调用链；DB / telemetry fallback 不绕开 finalizer；onPersisted 派生链不断 |
| W2-2 | plugin-runtime / goalrun binding surface 收敛 | 新增 `plugin-runtime/binding.go` 显式声明 5 类 binding（request / response / tool / audit / session）；goalrun 不再 import plugin-runtime，plugin-runtime 不 import goalrun | import graph 检验；双向 import 报错 |
| W2-3 | cost reconciliation worker | 把 [`stats-reconciliation.md`](../../stats-reconciliation.md) 对账脚本搬入 `bg/cost_reconciliation_worker.go`；周期（每日 03:30）+ on-demand（`/api/admin/cost/reconcile`） | 对账报告输出到 `request_logs.cost_reconcile_status` 与 admin dashboard |
| W2-4 | drain matrix runbook | 新增 `docs/06-deployment/04-runbooks/drain-matrix.md`：systemd / compose / k3s 三模式各自最长 drain 时间 + 回滚判定 | runbook 评审通过；staging 复演 |
| W2-5 | `/v2/*` 与 `cmd/gateway-v2` banner | W0-9 后续；增加覆盖率统计与告警：v2 路径异常上升 → page | Prometheus 规则落 `deploy/prometheus/rules/v2-path-alert.yml` |
| W2-6 | 实时流修复回归 | `audit/realtime-flow-fixes-audit-20260821.md` 列出的修复点逐一加 `regression_test.go` | 全绿；fix commit 关联 test commit |
| W2-7 | Dashboard 优化 D5（滑动窗口可点击 → RequestLogDetail） | `web/src/components/QueuePerspectivePanel.vue` 单元格点击事件；与 `RequestLogDrawer.vue` 打通 | vitest + vite build 通过；staging E2E |

### 3.3 禁止

- 不修改 V2 canonical owner。
- 不修改生产路由接线。

### 3.4 退出条件

- finalizer 单元 + 集成测试；持久化对账报告每周自动产出。
- drain matrix 演练完成；systemd timeout 与 supervisor Stop 一致。

---

## 4. V6-W3 · 安全 / 合规（3-4 周）

### 4.1 目标

补上 v6 唯一允许"安全模块首次上线"的窗口：PII、RLS、GUC、quality canonical source、OTel semconv、KMS rotate。

### 4.2 必做（按依赖顺序）

| # | 任务 | 允许文件 / 文档 | 验收 |
|---|---|---|---|
| W3-1 | Presidio sidecar 镜像 | `deploy/shared/docker/presidio-sidecar/Dockerfile` + `zh_pii_rules/` 包；构建脚本加入 `scripts/build-offline-packages.sh` | `docker run` 本地起；`curl /analyze` 返回 spans |
| W3-2 | k3s Deployment + Service（154/245） | `deploy/k8s/apps/presidio-sidecar.yaml` | `kubectl get pod presidio-sidecar` Running |
| W3-3 | Go `armor/sdp.go` + `sdp_stream.go` | `security/armor/sdp*.go` + 测试；与 relay handler 集成 | `go test ./security/armor/...` ≥ 5 PASS；端到端：prompt → 脱敏 → LLM → 脱敏 → 客户端 |
| W3-4 | 中文 PII 规则包（身份证 / 银行卡 / 车牌 / 地址 / 姓名） | `presidio-sidecar/zh_pii_rules/` | 100 条测试集：身份证/手机号/银行卡召回 ≥ 95%；车牌/地址 ≥ 85% |
| W3-5 | 流式脱敏 SSE 缓冲 | `security/armor/sdp_stream.go`（maxBuffer=32 字节） | SSE 跨 chunk PII 测试 PASS |
| W3-6 | per-tenant 策略 + Admin API | DB migration + `admin/sdp_policy.go` + `GET /api/admin/sdp/policy/:tenant` + 前端配置页 | UI 可配置 |
| W3-7 | observe mode 上线 2 周 + FPR/FNR 报告 | `docs/04-implementation/changes/2026-MM-DD-v6-w3-pii-observe.md` | FPR < 5% / FNR < 5% |
| W3-8 | Maintain tenant policy + role + GUC | Maintain `internal/migrations`、`internal/store` | FORCE RLS + NOBYPASSRLS role + tenant GUC 写入事务 |
| W3-9 | Maintain 跨租户 negative test | `tests/integration/maintain_tenant_matrix_test.go` | tenant A/B、regular/admin/super-admin RLS 矩阵通过 |
| W3-10 | Tenant GUC 缺省 fail-closed（关键接口） | `internal/auth/tenant_ctx.go` | 高安全接口（`/api/admin/*`、Maintain proxy、quality）fail-closed；其他保留 fail-open 但打 WARN |
| W3-11 | Quality API canonical source | `internal/quality/api.go` 与 `internal/handlers/quality_handler.go` 共享统一 repository；`provider_profile_daily` 单一权威 | 跨数据源对账；`recalculate` 改名 `refresh`；与 `admin/auth.go` 共享 tenant wrapper |
| W3-12 | OTel GenAI semantic conventions | `internal/observability/semconv.go` | `gen_ai.system` / `gen_ai.request.model` / `gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens` / `gen_ai.response.finish_reasons` 等；内部命名属性保留兼容 |
| W3-13 | KMS rotate runbook | `docs/06-deployment/04-runbooks/kms-rotate.md` | 不引入新依赖；rotate 演练一次 |

### 4.3 禁止

- PII enforce 模式（mask / hash / block）不可在 observe 数据未达标前上线。
- Maintain RLS negative test 必须真实 PG/Redis，不能 mock 替代。

### 4.4 退出条件

- W3-7 报告通过。
- W3-9 矩阵全绿。
- W3-11 quality 跨数据源对账通过。
- `docs/04-implementation/changes/2026-MM-DD-v6-w3.md` 落盘（含 PII FPR/FNR、RLS matrix、OTel sample、quality 对账）。

---

## 5. V6-W4 · 协议扩展（4 周）

### 5.1 目标

按 [`omniroute-integration-boundary.md`](../../03-design/01-architecture/architecture/omniroute-integration-boundary.md) §6–§8 的"ADOPT / CONSUME / REJECT"决策表，落地 MCP stdio→HTTP→SSE 三阶段；A2A 仅做 message/send 试点；Fusion 仅 Opt-in non-stream。

### 5.2 必做

| # | 任务 | 允许文件 | 验收 |
|---|---|---|---|
| W4-1 | MCP stdio read-only server | `plugin-runtime/mcp/server_stdio.go`；`initialize`、`tools/list`；stdout 仅 JSON-RPC | unit test；本地 `mcp-client` 通信通过 |
| W4-2 | MCP `tools/call` | `plugin-runtime/mcp/server_call.go`；绑定 authenticated principal、tenant、scope、policy、timeout、panic isolation、audit | unit + integration；disabled tool 不可 list/call |
| W4-3 | MCP HTTP 单请求 transport | `plugin-runtime/mcp/server_http.go` | curl 测试 PASS |
| W4-4 | MCP SSE / streamable HTTP | `plugin-runtime/mcp/server_sse.go` + session / nonce / replay / connection limits | 集成测试；并发连接压测 |
| W4-5 | A2A message/send 同步试点 | `plugin-runtime/a2a/message_send.go` + persisted task lifecycle + tenant/RLS + cancel 幂等 + TTL + read-only skills | unit + integration |
| W4-6 | Fusion Opt-in non-stream（staged → small panel parallel → judge） | `plugin-runtime/fusion/coordinator.go`；per-source limiter + cancel + audit + replay | unit + integration |
| W4-7 | tenantops / observability 治理 | 决策：补内容 or 删除 | ADR 记录 |
| W4-8 | Provider catalog 导入（参考 §3） | `provider/catalog/`；schema lint + idempotent seed + discovery/contract test + disabled until verify | unit + integration |

### 5.3 禁止

- MCP server 不输出 JSON-RPC 以外的 stdout 日志。
- Fusion 不开启 streaming；A2A 不开启 SSE。

### 5.4 退出条件

- W4-1 ~ W4-4 全部 PASS。
- A2A / Fusion 仅在 Opt-in 模式下可用；默认关闭。
- `docs/04-implementation/changes/2026-MM-DD-v6-w4.md` 落盘。

---

## 6. V6-W5 · 智能 / 文档（2-3 周）

### 6.1 目标

把"基线稳定后能加什么"做成可观测、可灰度、可回滚的尝试。

### 6.2 必做

| # | 任务 | 允许文件 | 验收 |
|---|---|---|---|
| W5-1 | Cascade router（cheap→expensive） | `autoroute/cascade.go`；仅对 tenant Opt-in | unit + shadow 对比；cost ↓ ≥ 10% 且 quality 不退化 |
| W5-2 | Semantic prompt cache | `cache/semantic/`；pgvector / RedisVL；hit/miss 指标 | hit rate ≥ 15%（首月目标） |
| W5-3 | docs-link-checker | `scripts/docs-link-checker.sh` + CI | 过期链接零；数字审计 |
| W5-4 | SLO 全面落地 | `deploy/prometheus/rules/slo-*.yml` + Grafana 看板 | 12 个 SLO 上线（见 [`05`](05-self-check-and-metrics.md)） |
| W5-5 | 历史 CHANGELOG / ADR 数字审计 | 修 `[STALE 2026-Q2]` 标记 | 月度 cron 跟进 |

### 6.3 禁止

- cascade 与 semantic cache 不可在 W3 未完成前启用（依赖 OTel semconv 与 quality 数据）。
- SLO 不可修改既有指标名称（必须新增）。

### 6.4 退出条件

- Cascade 与 semantic cache 在 ≥ 2 租户 opt-in 后 14 天内无 quality 退化事件。
- 12 个 SLO 全部上线；oncall 演练通过。

---

## 7. 跨波次依赖图

```text
V6-W0 ──► V6-W1 ──► V6-W2 ──► V6-W3 ──► V6-W4 ──► V6-W5
   │         │         │         │         │         │
   └─────────┴─────────┴─────────┴─────────┴─────────┘
                          │
                  Contract Freeze T0 (2026-08-22) 不动
                  ADR-0001 / ADR-0002 持续生效
                  PII / RLS 优先于 MCP/A2A/Fusion
```

并行安全：
- W2 与 W3 部分子任务可并行（finalizer / cost reconciliation 与 PII sidecar 镜像构建无依赖）。
- W4 必须等 W3 完成（依赖 OTel semconv 与 quality 数据）。
- W5 必须等 W3 + W4 完成。

---

## 8. 人员与节奏

| 波次 | 主要 Owner | 协办 | 评审 |
|---|---|---|---|
| W0 | 后端架构 | SRE / DBA | 架构组月度 |
| W1 | 后端架构 + 路由 Owner | 测试 / DBA | 架构组 + SRE |
| W2 | 后端架构 + 持久化 Owner | 数据 / DBA | 架构组 |
| W3 | 安全 + 后端架构 | DBA / 法务 / SRE | TL + 安全 |
| W4 | 协议 Owner | 后端架构 / 安全 | 架构组 + TL |
| W5 | 数据 / ML Owner | 后端架构 / 前端 | TL |

每个波次有独立 `branch`、`worktree`、独立 commit 序列、独立 `docs/04-implementation/changes/YYYY-MM-DD-v6-wN.md`、独立 `04-implementation/plan/2026-MM-DD-v6-wN-execution-plan.md`（若需要更细的子任务拆分）。

---

## 9. v6 与 P0/P1/P2 的映射

| 既有 P0/P1/P2 优化项 | v6 波次 |
|---|---|
| P0.1 `/api/quality` 认证 + 租户范围 | V6-W3-W11 |
| P0.2 Gateway fail-closed + Maintain wiring | V6-W0-W5 / W6 / W7 |
| P0.3 Maintain / ASM 迁移安全 | V6-W3-W8 / W9 |
| P1.1 Session V2 shadow reconciliation | V6-W1-W6 |
| P1.2 TPM 与 Candidate lease | V6-W1-W8 |
| P1.3 统一 RetryBudget | V6-W1-W5 |
| P1.4 Billing / body finalization | V6-W2-W1 / W3 |
| P1.5 Worker lifecycle + deployment contract | V6-W0-W4 + V6-W2-W4 |
| P2.1 Composition root 收敛 | V6-W0-W1 |
| P2.2 Provider catalog + active strategy | V6-W4-W8 |
| P2.3 Compression stage | V6-W4（带 Lite/Caveman/RTK 阶段） |
| P2.4 MCP → A2A → Fusion | V6-W4 |

# Gateway Optimization Roadmap — 优化路线与代码指导

> **事实快照：** 2026-08-21  
> **目标：** 把架构审计转成可独立回滚、可测试、可观测的工程波次。本文是执行指导，不表示任何波次已经完成。  
> **v6 后续（2026-08-23）：** 本文件的 P0-P2 已在 [`docs/架构优化v6/`](../../../架构优化v6/) 重新映射为 V6-W0→W5 六波次（[README](../../../架构优化v6/README.md) · [波次路线图](../../../架构优化v6/03-roadmap-v6-waves.md) · [代码-设计偏差表](../../../架构优化v6/02-code-vs-design-deltas.md)），P0/P1 收敛进"门禁+契约"层（V6-W0/W1），P2 拆为架构演进（V6-W4）与智能层（V6-W5）。review 请以 v6 为准。

## 1. 执行原则

1. 代码/SQL/runtime wiring 是事实源；schema、目录和 feature flag 不是完成证据。
2. 每个波次先写 regression/integration test，再改代码；独立 commit，保留 rollback。
3. 安全、tenant、RLS、body/session、retry 和计费问题优先于新增 MCP/A2A/Fusion。
4. 不覆盖当前工作树已有修改；不把未跟踪的 `llm-gateway-go-w3c/` 当主模块事实。
5. 测试 skip 必须记录为 `UNKNOWN` 或 CI 配置失败，不能当作 PASS。

## 2. P0：安全和实际 wiring

### P0.1 `/api/quality` 认证和租户范围

- **问题：** `/api/*` 全局绕过后，质量路由需要显式 Admin/JWT 和 tenant/RLS wrapper；两套 quality API 还可能使用不同数据源。
- **代码落点：** `cmd/gateway/main.go` quality route；`internal/handlers/quality_handler.go`；`internal/quality/api.go`。
- **复用：** `admin/auth.go`、`admin/tenant_ctx.go`、`admin/session_scope.go`、统一 query repository。
- **实现：** 先确定 `provider_profile_daily` 或统一 repository 为 canonical source；provider/model/summary/ranking API 都接受已认证 tenant scope；model filter 不能静默忽略；recalculate 必须真正计算或改名为 refresh。
- **测试：** anonymous 401/403；tenant A 不能读 B；provider/model/summary/ranking SQL 含 scope；rows.Err 传播；quality-service 仅内网或有 service auth。
- **回滚：** 保留旧读路径，仅关闭新 wrapper/feature flag。
- **出口：** auth、tenant、数据源、跨租户 negative tests 全部通过。

### P0.2 Gateway fail-closed 与 Maintain wiring

- **问题：** legacy admin fallback、DB 不可用时 key verifier 未装配、h2c 可能包装旧 handler 而不是 final handler。
- **代码落点：** `admin/auth.go`、`middleware/auth_mw.go`、`cmd/gateway/main.go:5579-5648`、`cmd/gateway/maintain_proxy.go`。
- **实现：** 生产缺 secret/password/DB auth dependency 时 readiness fail；移除默认管理员凭据；显式 public endpoint allowlist；h2c 包装最终 `finalHandler`。
- **测试：** config missing fail-closed；HTTP/1.1+h2c 验证 `/maintain-api/*`、legacy `/api/*`、`/v1/*`；Admin fallback 不创建/返回 API key；新 `/api` 路由必须有 auth。
- **出口：** wiring smoke tests 和部署配置检查通过。

### P0.3 Maintain / ASM 迁移安全

- **问题：** Maintain 的 RLS policy、FORCE RLS、tenant context 和资源 ownership 证据未闭环；ASM 当前未提交迁移/secret/DLQ/body-fetch 变更需先审查。
- **代码落点：** Maintain `internal/migrations`、`internal/httpapi`、`internal/store`；ASM migrations、`internal/gatewayevents`、knowledge worker。
- **实现：** 非 owner/NOBYPASSRLS role；tenant-aware policy；transaction-local context；resource ID 查询必须带 tenant；ASM migrations 幂等且唯一约束一致；body fetch 失败 retryable。
- **测试：** empty/upgraded/down/re-up migration；tenant A/B、regular/admin/super-admin RLS matrix；DLQ replay；secret missing fail; body fetch transient/permanent failure。
- **出口：** 真实 PG/Redis、restore 和 cross-tenant negative tests 均有输出。

## 3. P1：数据、资源和可靠性

### P1.1 Session V2 shadow reconciliation

- **问题：** V2 schema/writer/cache 已存在但默认 shadow；失败可能只 warning，依赖 request_logs persisted。
- **代码落点：** `cmd/gateway/session_v2_init.go`、`domains/session/v2`、`domains/session/dual_writer.go`、`cmd/gateway/dual_read_validator.go`、`internal/outbox`。
- **实现：** shadow failure durable queue/DLQ；validator 使用 tenant transaction；`lastN` 真正限制 SQL；记录 turn/body/token/cost/hash/tenant diff；增加 backfill/replay。
- **测试：** DB/Redis failure、duplicate/out-of-order replay、request cancel、partial stream、retry failover、tenant isolation、old/new body dual-read。
- **出口：** 7 天 freshness、24 小时 shadow、字段/行数/hash 完整率、G4 rollback 和 G5 audit correlation 通过后才允许 read-primary。

### P1.2 TPM 与 Candidate lease

- **问题：** TPM limiter 有实现但主 admission 语义需收口；FP slot/concurrency 分步 acquire。
- **代码落点：** `ratelimit/redis_sliding.go`、`domains/streaming/rate_limit.go`、`domains/streaming/executors/executor_dispatch.go`、`bg/probe_queue.go`。
- **实现：** request token reservation/reconcile；Redis degraded policy；引入 `CandidateLeaseCoordinator`，复用 probe queue lease token/heartbeat/zombie rescue。
- **测试：** multi-instance Redis; estimated vs final tokens; timeout/cancel exactly-once release; lease takeover; TTL/heartbeat; overload/rollback。
- **出口：** quota correctness 和 acquire/release 对称性有指标及回归证据。

### P1.3 统一 RetryBudget

- **问题：** streamretry、dispatch、goal、survival、failover 多层 budget/backoff 可能放大尝试。
- **代码落点：** `domains/dispatch/queued_request.go`、`retry_schedule.go`、`internal/streamretry`、`cmd/gateway/goal_retry_policy.go`。
- **实现：** request-level budget 保存 attempts/deadline/first-byte/provider/model/reason/retry_at；所有层只读写同一预算；定义 Retry-After 优先级。
- **测试：** cross-layer max attempts；首字节前后；client cancel；cost/charge once；model/credential switch；deadline。
- **出口：** 单请求最大尝试、TTFB、成本和终态可解释。

### P1.4 Billing/body finalization

- **问题：** 多模态 ChargeRequest 与 legacy 四 token 维度可能不一致；fallback/onPersisted 派生链可能断裂。
- **代码落点：** `domains/streaming/handler.go` billing finalizer、`maas/service.go`、telemetry client、request WAL/outbox。
- **实现：** 统一 `RequestFinalizer`；固定 token source、charge idempotency、request/attempt/turn linkage；multimodal token、partial/cancel/retry 规则统一。
- **测试：** text/image/audio/video；client cancel；preflight failure no charge；retry one charge；DB failure replay。
- **出口：** usage/ledger/body/audit 对账无重复或漏记。

### P1.5 Worker lifecycle and deployment contract

- **问题：** worker 使用分散 context，部分 collector/profile/session lifecycle wiring 不明确；systemd/compose timeout/env/health 漂移。
- **代码落点：** `cmd/gateway/main.go` shutdown、`bg/` workers、`deploy/llm-gateway-go.service`、各 compose、`config.example.yaml`。
- **实现：** `BackgroundSupervisor`；context-aware Stop、WaitGroup、timeout metrics、panic policy；统一 env schema 和 healthcheck；systemd timeout 覆盖真实 drain budget；专用用户运行。
- **测试：** startup dependency matrix；SIGTERM drain；worker timeout；h2c/HTTP health；compose contract；Redis/PG unavailable。
- **出口：** 关闭无 goroutine leak，deployment contract test 通过。

## 4. P2：架构演进

### P2.1 Composition root 和依赖方向

依据 `docs/adr/ADR-0002-target-go-package-layout.md` 分波次迁移：

1. 无环基础件 → `internal/platform`；
2. telemetry/metrics；
3. control/admin/bg/settings；
4. credential/route，在 B1 解环后实施。

每波次：`git mv`/import 更新 → `go build ./...` → `go vet ./...` → 定向和全量测试 → 独立可回滚 commit。不要把 `domains/` 一次性塞进 `internal/`。

### P2.2 Provider catalog 和 active strategy

- 中间 JSON → schema lint → idempotent seed → discovery/contract test → disabled/灰度。
- active strategy 只能排序已通过 tenant/URSM/health/tier/limiter 的候选。
- request-aware cost/cache/context/headroom 必须有输入和决策解释。

### P2.3 Compression stage

协议转换后、上游发送前接入 Lite/Caveman，补 once guard、hash、savings、reason、limits、fail-open；RTK 只处理结构化 tool result；Stacked 最后且默认关闭。

### P2.4 MCP → A2A → Fusion

- MCP：先 stdio read-only，再 HTTP/SSE；复用 registry/policy/audit。
- A2A：先同步 message/send + persisted task，再 SSE；ASM 只消费 task facts。
- Fusion：仅非流式 opt-in，staged → constrained parallel → judge；每 source 有 limiter、cost、cancel、audit。

## 5. 证据模板

每个波次报告必须包含：

```text
commit / migration version / flags / environment
scenario input / tenant scope / time window
request_id / attempt_id / turn_id / charge_id / event_id
rows/hash/orphan/duplicate/lag/retry/DLQ metrics
P50/P95/P99/TTFB/queue depth/resource usage
negative tests / replay output / rollback action and post-rollback reconciliation
```

HTTP 200、脚本退出码、目录存在、schema 存在或“页面可打开”都不能单独作为 ownership/cutover 证据。

# Phase 2 R1.12 本地完整测试方案

> **目标**：在本地（127.0.0.1）环境下，对 R1.12 新 Pipeline 路径做完整端到端验证，
> 灰度切流前的最后一道质量门。
>
> **R1.12 范围**：`cmd/gateway-v2/main.go` (Pipeline-based entry point) +
> `cmd/gateway/main.go` 的 v2 wiring 旁路
>
> **当前状态**（2026-06-26，本地实测）：
> - `cmd/gateway-v2` build OK，**13/13 E2E tests PASS**（5 top-level + 8 subtests）
> - 全仓库 `go build ./...` OK + `go vet ./...` clean
> - v2 路径与 v1 路径并存，默认走 v1
> - v2 监听 `:8782`，endpoint 为 `/v1/chat` + `/healthz`（**非** `/v2/*`，见 §1.3）

---

## 1. 测试目标

### 1.1 必须验证
- [ ] v2 Pipeline 在本地启动后，`/healthz` 返回 200
- [ ] v2 路径处理真实 LLM 上游请求（含 mock provider，cmd/gateway-v2 预置 `default-openai`）
- [ ] 多租户隔离：tenant_id 正确传递（`X-Tenant-ID` header → `env.TenantID`）
- [ ] Security Hook 阻断危险请求（"jailbreak" 关键字 → 403）
- [ ] Audit sink 收到带 `TenantID` 的事件
- [ ] Metrics counter `requests_total` 累加
- [ ] 各 `LLM_GATEWAY_V2_*` feature flag 组合下，stage 数量符合预期（6/7/8）
- [ ] `lint-llmgw-deploy` 11 项 SSOT 验证不报错
- [ ] `lint-tenant-scope-llmgw` L1=0
- [ ] `lint-pg-rls` L1=0（candidate_failure_logs / request_wal 等 RLS policy 启用）

### 1.2 不在范围（生产前置）
- 71/184 实际部署（需用户授权）
- 真实 LLM provider 调用（mock 替代）
- 灰度切流策略（在 phase 2 后续文档定义）

### 1.3 关键事实（v2 入口与 v1 的差异）
| 维度 | v1 (`cmd/gateway/main.go`) | v2 (`cmd/gateway-v2/main.go`) |
|------|----------------------------|-------------------------------|
| 监听端口 | `:8780` (默认) | `:8782` (默认, env `LLM_GATEWAY_LISTEN`) |
| 协议 | OpenAI `/v1/chat/completions` | `/v1/chat` (mock SSE-friendly) |
| Pipeline | 线性 handler chain | `pipeline.RequestPipeline` 16-stage |
| Healthz | `/healthz` | `/healthz` (相同) |
| 进程模型 | k8s Deployment (184) | `go run` 演示入口 |

> **重要**：`cmd/gateway-v2` 与 `cmd/gateway` 是**独立进程**（独立 `main()`，不互相 import），
> 不存在 `LLM_GATEWAY_V2_ENABLED` 旁路开关。切换方式：上游 nginx 切 upstream。

---

## 2. 测试场景矩阵

### 2.1 基础健康
| 场景 | 端点 | 期望 | 验证方法 |
|------|------|------|----------|
| v2 healthz | GET /healthz | 200 OK `ok` | `curl -i http://localhost:8782/healthz` |
| v2 启动时间 | n/a | <5s | `time go run ./cmd/gateway-v2` |
| v2 优雅关闭 | SIGTERM | exit 0 | `kill -TERM <pid> ; wait` |
| v2 audit close 幂等 | n/a | 2 次 Close 不报错 | `TestE2E_NoMemoryLeak` 已覆盖 |

### 2.2 Pipeline 各阶段（已实现，16 stage）
| Phase | Stage | Hook 类型 | 测试方法 |
|-------|-------|-----------|----------|
| PreRouting | tracing | `observability.NewTracingHook` | `TestE2E_ObservabilityRecords` |
| PreRouting | security | `security.NewSecurityHook` | `TestE2E_PipelineExecutes/dangerous_request_blocked` |
| PreRouting | provider_discovery | `provider.NewProviderDiscoveryHook` | 通过 mock provider 验证 |
| PreRouting | credential_health | `credential.NewHealthCheckHook` | mock InMemoryStore |
| PreRouting | cache_lookup | `cache.NewCacheLookupHook` | `TestE2E_IntegrationWithAllHooks` |
| PreRouting | session_inspect | `session-inspector.NewInspectorHook` | unit test |
| PreRouting | agent_discovery | `agent-ecosystem.NewAgentDiscoveryHook` | `TestE2E_IntegrationWithAllHooks` |
| Routing | routing | `routing.NewStickyRouter(RoundRobin)` | unit test |
| PostRouting | credential_limit | `credential.NewLimiterHook` | 4-layer Limiter |
| Transform | transform | `transformation.NewTransformHook(Sanitizer+Compressor)` | unit test |
| Transform | compression | `compression.NewCompressionHook(LCSCompressor)` | unit test |
| PostTransform | tools | `tools.NewToolInterceptionHook(MetaToolInterceptor)` | unit test |
| PostUpstream | streaming | `streaming.NewStreamHook(SSEStreamer)` | mock 上游 |
| PostResponse | audit | `audit.NewAuditLogHook(BatchWriter)` | `TestE2E_PipelineExecutes/audit_records_events` |
| PostResponse | cache_save | `cache.NewCacheSaveHook` | `TestE2E_IntegrationWithAllHooks` |
| PostResponse | metrics | `observability.NewMetricsHook` | `TestE2E_ObservabilityRecords` |

### 2.3 多租户隔离（必跑，**阻断门**）
| 测试 | 期望 | 阻断条件 |
|------|------|----------|
| tenant-a 看不到 tenant-b 的 request_logs | SQL 返回空 | 看到对方数据 = L1 阻断 |
| admin handler tenant scope (L1 修正) | 不同 tenant admin 看到不同数据 | 看到对方 = L1 阻断 |
| RLS candidate_failure_logs | 不同 tenant SELECT 隔离 | 直连 PG 验证 |
| RLS request_wal | 同上 | 直连 PG 验证 |
| `lint-tenant-scope-llmgw` | L1=0 | 任何 L1 阻断 |
| `lint-pg-rls` | L1=0 | 任何 L1 阻断 |
| `lint-otel-tenant` | L1=0 | 任何 L1 阻断 |

### 2.4 兼容性 / Feature Flag 组合
| `LLM_GATEWAY_V2_CACHE` | `_SECURITY` | `_AUDIT` | `_OBSERV` | `_STREAMING` | 期望 stage 数 | 测试 |
|---|---|---|---|---|---|---|
| true | true | true | true | true | 13 (16 max, 一些条件跳过) | `TestE2E_ConfigFlags/all_enabled` (≥8) |
| false | true | false | false | false | 7 | `TestE2E_ConfigFlags/security_only` (≥7) |
| false | false | true | false | false | 7 | `TestE2E_ConfigFlags/audit_only` (≥7) |
| false | false | false | false | false | 6 | `TestE2E_ConfigFlags/minimal` (≥6) |

> 注：实际 stage 数取决于 buildPipeline 的 `if cfg.Enable*` 守卫；test 断言用 `>=` 防止数量漂移。

---

## 3. 验证步骤（手工）

### 3.1 启动准备
```bash
# 工作目录
cd __DEV_HOME__/workspace/official-deploy/services/llm-gateway-go

# 1. 启动本地依赖（如有 PostgreSQL/Citus/Redis/Memora 需求）
# 注：当前 cmd/gateway-v2 用 mock in-memory store，**无强制外部依赖**。
# 如需真实 DB 验证 RLS，参考 docs/architecture/phase1.5-revised-plan-20260625.md §3.1
# 启动 184 流复制副本（默认 llm-gateway-pg-71-replica）

# 2. （可选）应用 migrations
# 当前 R1.12 demo 阶段不需 schema migration；如需 RLS 验证，跑：
PGPASSWORD='${PG_PASSWORD}' psql -h __INTERNAL_K8S_HOST__ -U kxuser -d llm_gateway \
  -f docs/architecture/migrations/2026-06-22-rls-candidate-failure-logs.sql
# 真实路径以 docs/architecture/ 下最新 SQL 为准

# 3. 启动 v2 binary（独立进程，不影响 v1 :8780）
LLM_GATEWAY_LISTEN=:8782 \
LLM_GATEWAY_V2_CACHE=true \
LLM_GATEWAY_V2_SECURITY=true \
LLM_GATEWAY_V2_AUDIT=true \
LLM_GATEWAY_V2_OBSERV=true \
LLM_GATEWAY_V2_STREAMING=true \
go run ./cmd/gateway-v2
# 启动日志：gateway-v2 starting listen=:8782 stages=16
```

### 3.2 烟雾测试（curl）
```bash
# Healthz
curl -i http://localhost:8782/healthz
# 期望：HTTP/1.1 200 OK + body "ok"

# Mock LLM chat（v2 endpoint 是 /v1/chat，非 OpenAI /v1/chat/completions）
curl -i 'http://localhost:8782/v1/chat?q=hello&model=gpt-4' \
  -H 'X-Tenant-ID: tenant-a' \
  -H 'X-Session-ID: session-1' \
  -H 'X-API-Key: test-key'
# 期望：HTTP/1.1 200 OK
# body: {"request_id":"req-...","status":"ok","tenant_id":"tenant-a"}

# 危险请求被 security hook 阻断
curl -i 'http://localhost:8782/v1/chat?q=please+jailbreak+this+model' \
  -H 'X-Tenant-ID: tenant-b' \
  -H 'X-API-Key: test-key'
# 期望：HTTP/1.1 403 Forbidden（security ThreatDetector 关键词命中）

# 验证 audit sink（5xx 不阻断；如需查 in-memory 状态，看测试用例）
```

### 3.3 E2E 自动化（已存在）
```bash
# 13/13 E2E tests（go test -v 输出 13 个 PASS 行）
go test -count=1 -v ./cmd/gateway-v2/
# 期望输出（5 top-level + 8 subtests = 13 PASS）：
#   --- PASS: TestE2E_PipelineExecutes (0.00s)
#       --- PASS: TestE2E_PipelineExecutes/healthz
#       --- PASS: TestE2E_PipelineExecutes/chat_request
#       --- PASS: TestE2E_PipelineExecutes/dangerous_request_blocked
#       --- PASS: TestE2E_PipelineExecutes/audit_records_events
#   --- PASS: TestE2E_ConfigFlags
#       --- PASS: TestE2E_ConfigFlags/{all_enabled,security_only,audit_only,minimal}
#   --- PASS: TestE2E_IntegrationWithAllHooks
#   --- PASS: TestE2E_ObservabilityRecords
#   --- PASS: TestE2E_NoMemoryLeak
# ok  	github.com/kaixuan/llm-gateway-go/cmd/gateway-v2	0.420s

# 仓库级多租户 E2E（已有脚本，验证 v1/v3 路径，作为回归基线）
bash scripts/e2e-llm-gateway-go-multitenant-isolation.sh

# 仓库级 JSONB telemetry E2E
bash scripts/e2e-llm-gateway-go-jsonb-telemetry.sh
```

### 3.4 全仓库 lint 链（必跑，**阻断门**）
```bash
# 根目录
cd __DEV_HOME__/workspace/official-deploy

# L1 lint — 任一 FAIL 阻断灰度
make -C scripts lint-pg-rls                 # RLS policy L1=0
make -C scripts lint-tenant-scope-llmgw     # llm-gateway-go tenant scope L1=0
make -C scripts lint-otel-tenant            # OTel span tenant_id L1=0
make -C scripts lint-llmgw-deploy           # deploy-*.sh SSOT lib 强制 (R44)
make -C scripts lint-deploy-ssot            # 所有 deploy-*.sh source lib + checkpoint (R45)

# 一键入口
bash scripts/e2e-multitenant-all.sh --skip-e2e
```

---

## 4. 验收标准

### 4.1 必须全部通过（**L1 = 阻断门**）
- [ ] `cd services/llm-gateway-go && go build ./...` exit 0
- [ ] `cd services/llm-gateway-go && go vet ./...` clean
- [ ] `go test -count=1 -v ./cmd/gateway-v2/` **13/13 PASS**（5 top-level + 8 subtests）
- [ ] `make -C scripts lint-pg-rls` L1=0
- [ ] `make -C scripts lint-tenant-scope-llmgw` L1=0
- [ ] `make -C scripts lint-otel-tenant` L1=0
- [ ] `make -C scripts lint-llmgw-deploy` PASS（无 SSOT 漂移）
- [ ] `make -C scripts lint-deploy-ssot` PASS
- [ ] `curl http://localhost:8782/healthz` → 200 OK
- [ ] `curl 'http://localhost:8782/v1/chat?q=hello&model=gpt-4' -H 'X-Tenant-ID: tenant-a' -H 'X-API-Key: test-key'` → 200 + `tenant_id=tenant-a`
- [ ] `curl 'http://localhost:8782/v1/chat?q=please+jailbreak+...'` → 403
- [ ] SIGTERM 优雅退出，exit 0

### 4.2 性能基线（参考，不阻断）
- v2 启动时间 < 5s（实际 ~0.5s 编译 + 启动）
- 单请求 P99 延迟 < 1s (mock upstream，无真实 provider)
- 内存占用 < 200MB（in-memory store + audit buffer 5s flush）

### 4.3 不动 v1 路径
- [ ] `cmd/gateway/main.go` 未被修改
- [ ] `cmd/gateway/main_v3_wiring.go` 未被修改（仍是 v3 智能压缩）
- [ ] 71 systemd unit 文件 `llm-gateway-go.service` 未被触碰

---

## 5. 失败处理

### 5.1 构建失败
- 检查 `domains/hooks/compression/metrics.go` 的 `AlreadyRegisteredError` 兜底
  （实际文件：`domains/hooks/compression/`，需 `grep -r prometheus.AlreadyRegisteredError` 确认）
- 检查 `cmd/gateway-v2/main.go` 与 `cmd/gateway/main.go` 的 import 冲突
  （实际是独立 `main()`，理论上不冲突；如出现，多半是 shared package 重复定义）

### 5.2 Linter L1 失败（**立即阻断**）
- 不可灰度
- 修复后重跑对应 `make -C scripts lint-X`，直到 L1=0
- 重点关注：
  - `lint-tenant-scope-llmgw`：新增 handler 必须带 `tenantLogsClause` 或等效 scope
  - `lint-pg-rls`：新增 tenant-aware 表必须 `ENABLE ROW LEVEL SECURITY` + `CREATE POLICY`
  - `lint-otel-tenant`：span 必带 `tenant.id` attribute

### 5.3 E2E 失败
- 不动 `cmd/gateway-v2/` 实现，先重跑：`go test -count=1 -v ./cmd/gateway-v2/`
- 单次 flaky → 重跑 2 次确认非确定性
- 持续失败 → 写诊断报告（参考 `.agents/skills/diagnose`），不切流
- 常见根因：
  - `audit.InMemorySink.Events()` 顺序依赖 → 加 `t.Skipf` 跳过前置失败
  - `tracer.StartSpan` 返回 nil → 检查 `observability.InMemoryTracer` 实现

### 5.4 性能基线未达
- 4.2 是参考项，不阻断；记录数字供后续对比
- 若 P99 > 1s 且为 mock upstream，问题在 Pipeline 编排 → 用 pprof 抓 CPU profile

---

## 6. 与生产部署的关系

| 步骤 | 命令 | 时机 | 阻断门 |
|------|------|------|--------|
| **本地 R1.12 测试** | 本文档 §3 | 本周（2026-06-26~） | §4.1 全部通过 |
| 184 staging 部署（v1 升级） | `bash scripts/deploy-llm-gateway-go-184.sh` | 本地全绿后 | staging verify_chain 11 项 OK |
| 灰度 1% 流量 | nginx 1% → `:8782` upstream | staging 验证后 | 24h 0 错误率 |
| 灰度 50% | nginx 50% → `:8782` | 1% 稳定后 | 24h 0 错误率 |
| 全量切流 | 100% → `:8782` | 用户授权后 | 7 天 0 critical |
| 删除 v1 | `git rm cmd/gateway/` | 全量稳定后 30 天 | 用户授权 |

> **红线**：v1 (`cmd/gateway/main.go`) 在切流完成前**禁止删除**；71 仍在生产服务 `llmgateway.internal.example.com`。

---

## 7. 引用

### 7.1 内部文档
| 资源 | 路径 | 验证 |
|------|------|------|
| R1.12 原始任务 | `docs/architecture/phase1.5-revised-plan-20260625.md:616` §5.4 | ✅ 存在 |
| 5.4 任务执行 | `cmd/gateway-v2/main.go` (393 lines) | ✅ build OK |
| 5.4 E2E 覆盖 | `cmd/gateway-v2/e2e_test.go` (241 lines, 13 PASS) | ✅ PASS |
| Phase 1.5 审计 | `docs/architecture/phase1-execution-audit-v2-revised-20260625.md` | ✅ 存在 |
| Domain 重构计划 | `docs/architecture/domain-refactoring-plan.md` | ✅ 存在 |
| 实施总计划 | `docs/architecture/implementation-plan.md` | ✅ 存在 |
| 多租户规范 | `../../docs/multi-tenant-standards.md` (仓库根) | ✅ 存在 |

### 7.2 Lint 脚本（已实现，验证过存在）
| Lint | 路径 | 目标 |
|------|------|------|
| `lint-llmgw-deploy` | `scripts/Makefile` → `scripts/_lib/lint-llmgw-deploy.sh` | R44 SSOT 强制 |
| `lint-tenant-scope-llmgw` | `scripts/Makefile` → `scripts/_lib/tenant-scope-lint.py` | L1 tenant scope |
| `lint-pg-rls` | `scripts/Makefile` → `scripts/_lib/pg-rls-lint.py` | L1 RLS policy |
| `lint-otel-tenant` | `scripts/Makefile` → `scripts/_lib/otel-tenant-lint.py` | L1 OTel tenant.id |
| `lint-deploy-ssot` | `scripts/Makefile` → `scripts/_lib/lint-deploy-ssot.sh` | R45 source lib + checkpoint |

### 7.3 部署 SSOT（间接相关）
- `scripts/_lib/llmgw-deploy-lib.sh`（398 行 SSOT lib）
- `scripts/_lib/deploy-ssot-lib.sh`（全服务通用 SSOT）
- `scripts/_lib/check-image-size.sh`（镜像 < 100MB 护栏）

### 7.4 部署脚本
- `scripts/deploy-mobile-h5-184.sh`（**已存在**；SSOT lint 验证目标）
- `scripts/deploy-llm-gateway-go-184.sh`（v1 主部署，本 R1.12 不修改）
- `scripts/deploy-llm-gateway-go-71.sh`（71 systemd 重启，本 R1.12 不修改）

### 7.5 E2E 脚本（回归基线）
- `scripts/e2e-llm-gateway-go-multitenant-isolation.sh`
- `scripts/e2e-llm-gateway-go-jsonb-telemetry.sh`
- `scripts/e2e-llm-gateway-go-model-policy.sh`
- `scripts/e2e-llm-gateway-go-v5-smart-compression.sh`
- `scripts/e2e-multitenant-all.sh`（一键全跑入口）

### 7.6 本地环境（**待编写**）
> ⚠️ 以下文件**当前不存在**，需在 R1.12 上线前补齐：
> - `docs/architecture/phase2-local-env.md`（本地 56/71/184 模拟环境搭建）
> - `services/llm-gateway-go/docker-compose.local-r112.yml`（v2 专用 compose）
> - `scripts/wait-for-services.sh`（PG/Redis/Memora 健康等待）
> - `scripts/apply-migrations.sh`（multi-db migration 编排）
> - `scripts/e2e-r112-multitenant-local.sh`（R1.12 专用本地多租户 E2E）
>
> 当前替代方案：直接用 `psql -h __INTERNAL_K8S_HOST__` 验证 RLS，cmd/gateway-v2 用 mock in-memory store 不需真实 DB。

---

## 8. 验证记录模板

> 执行人填写：

```yaml
date: 2026-06-XX
executor: <name>
branch: main
commit_sha: <git rev-parse HEAD>

build:
  go_build: PASS
  go_vet: PASS

e2e:
  cmd/gateway-v2 (13/13): PASS
  multitenant-isolation: PASS / FAIL
  jsonb-telemetry: PASS / FAIL

linter:
  lint-pg-rls (L1=0): PASS / FAIL
  lint-tenant-scope-llmgw (L1=0): PASS / FAIL
  lint-otel-tenant (L1=0): PASS / FAIL
  lint-llmgw-deploy: PASS / FAIL
  lint-deploy-ssot: PASS / FAIL

smoke:
  healthz: PASS / FAIL
  chat (tenant-a): PASS / FAIL
  dangerous (403): PASS / FAIL
  graceful_shutdown: PASS / FAIL

performance:
  startup_time: <X>s
  p99_latency: <X>ms
  memory_rss: <X>MB

blockers: <list, or "none">
decision: <GREEN to staging / HOLD / ROLLBACK>
```

---

**Last verified**: 2026-06-26
**Maintainer**: Kaixuan DevOps Team
**Status**: R1.12 Local Test Plan — pending first execution

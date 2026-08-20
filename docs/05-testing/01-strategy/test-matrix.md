# Gateway Test Matrix — 当前测试矩阵

> **事实快照：** 2026-08-21  
> **原则：** 测试存在不等于当前生产路径通过；没有外部依赖时的 skip 必须记为 `UNKNOWN`，不能记为 PASS。

## 1. 测试层级

| 层级 | 主要落点 | 关注点 | 外部依赖 | 当前状态 |
|---|---|---|---|---|
| Unit | 各 `domains/*`、`internal/*`、`admin/*` | 状态、评分、转换、错误、边界 | 无/Mock | `CURRENT/PARTIAL`，需按波次执行 |
| Contract | `test/events/`、事件 contract tests | schema/version/event_id/tenant/correlation | 可选 PG | `CURRENT/PARTIAL` |
| Routing | `domains/streaming/executors/*routing*_test.go`、`tests/routing/` | URSM、P2C、Bandit、shadow、tier、sticky | Redis/PG 部分需要 | `CURRENT/PARTIAL` |
| Streaming | `domains/streaming/`、`tests/e2e/` | SSE、首字节、cancel、retry、integrity | Mock/provider | `CURRENT/PARTIAL` |
| PG/RLS | `admin/session_analytics_realdb_test.go`、RLS tests | tenant isolation、owner、bypass role | `TEST_DATABASE_URL`, NOBYPASSRLS | `MIGRATION-GATE` |
| Redis | limiter/session/URSM/queue tests | Lua atomicity、fallback、多实例 | Redis | `CURRENT/PARTIAL` |
| Integration | `tests/integration/`、package integration tests | PG+Redis+runtime wiring | Docker/PG/Redis | `CURRENT/PARTIAL` |
| Provider E2E | Provider-specific scripts/tests | auth、models、stream、429、protocol | 真实凭据 | `UNKNOWN` unless run evidence exists |
| Deployment | `deploy/verify.sh`、compose/systemd/K8s checks | health/readiness/upgrade/rollback | Target environment | `CURRENT/PARTIAL` |
| Security | `tests/security/`, auth/guardian tests | auth, secrets, tenant, injection, SSRF | 可选 services | `CURRENT/PARTIAL` |
| Migration | `db/migrations`、`sql/migrations`、installer embedded seeds | empty/upgraded/down/re-up/checksum | Isolated PG | `MIGRATION-GATE` |

## 2. P0 回归门禁

### 2.1 认证与租户

- `/api/quality/*` anonymous request rejected。
- quality provider/model/summary/ranking cannot cross tenant。
- quality-service has service auth or private-only exposure。
- missing production auth secret/password/DB verifier fails closed。
- `?token=` is not accepted as a long-lived JWT transport。
- missing tenant GUC cannot silently select `default` for protected queries。

### 2.2 Maintain proxy

For both HTTP/1.1 and h2c:

- `/maintain-api/*` reaches configured upstream;
- legacy `/api/downloads/*` has deprecation headers and token stripping;
- `/v1/*` remains Gateway-owned;
- no `MAINTAIN_SERVICE_URL` falls back to legacy handler;
- final server handler is the wrapper containing proxy/static routes。

### 2.3 Session and data integrity

- session rotation uses authenticated/session tenant, never a hardcoded default;
- V2 shadow failure is observable and replayable;
- dual-read validator applies tenant transaction and honors `lastN`;
- request/attempt/turn/charge linkage remains stable through retry/failover;
- body hash, orphan, duplicate, turn gap and tenant reconciliation are measurable。

### 2.4 Limits, billing, lifecycle

- TPM admission is exercised in the actual Chat/Messages/Responses path;
- Redis fallback behavior is explicit for strict vs best-effort quotas;
- FP slot + concurrency acquire/release is exactly-once or idempotent;
- multimodal image/audio/video usage reaches the correct charge API;
- streamretry/dispatch/goal/survival do not exceed one request-level retry budget;
- every started worker stops within the configured shutdown deadline。

## 3. Required test commands

Commands must be run from the target module and recorded with commit/config/fixture context:

```bash
go test ./provider/catalog ./domains/streaming/executors ./domains/hooks/compression ./metatools ./registry

go test ./admin -run 'Test.*(Auth|Tenant|RLS|Quality|Session)' -count=1

go test ./domains/dispatch ./ratelimit ./bg -count=1

go test -race ./domains/dispatch ./ratelimit ./domains/session/...
go test ./... -timeout=300s
```

External suites require explicit evidence:

```bash
TEST_DATABASE_URL='postgres://<low-priv-role>@<isolated-db>' go test ./admin -run TestRLS_ -count=1
# Redis/provider/deployment E2E must state endpoint, fixture, flags and result.
```

Never put real secret values in command logs or reports.

## 4. Migration and cutover evidence

每次 session/Maintain/ownership 波次至少保存：

- commit、migration version、feature flags、环境和租户范围；
- request/attempt/turn/charge/event correlation；
- rows、hash、orphan、duplicate、lag、DLQ、replay 结果；
- RLS negative matrix 和 role attributes；
- P50/P95/P99、TTFB、queue depth、worker shutdown；
- rollback action 以及 rollback 后再次对账。

以下不能单独作为通过证据：HTTP 200、目录存在、schema 存在、测试 skip、页面可打开、单元测试绿。

## 5. 结果状态

| 状态 | 含义 |
|---|---|
| `PASS` | 测试在声明环境和版本中实际执行并通过。 |
| `FAIL` | 实际执行失败，必须记录输出和回滚。 |
| `UNKNOWN` | 因缺少真实环境/凭据/外部服务未执行。 |
| `SKIPPED-CONFIG` | 测试因环境配置缺失被跳过；发布门禁视为未满足。 |
| `SHADOW-PASS` | 只验证旁路/观测路径，不能作为 primary/cutover 通过。 |

## 6. 测试资产维护

- 生产路径变化时同步更新本矩阵和架构 `runtime-request-flow.md`。
- 历史报告必须写明快照日期和适用 commit。
- 任何 real PG/RLS 测试禁止使用 superuser 或 bypassrls role。
- 外部 Provider 测试只保存脱敏 metadata、错误分类和耗时，不保存 API key 或完整敏感正文。

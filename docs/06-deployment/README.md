# Gateway Deployment — 部署入口

> **事实快照：** 2026-08-21
> **本页定位：** 部署文档唯一入口；可执行脚本和 manifest 位于 `deploy/`，数据库/初始化/runbook 详见本目录子目录。

## 1. 部署模式

| 模式 | 物料 | 适用场景 | 当前状态 |
|---|---|---|---|
| M1 | 二进制 + systemd + installer | 私有/离线单机 | `CURRENT/PARTIAL` |
| M2 | Docker Compose | 单机或测试环境 | `CURRENT/PARTIAL`，需核对 env/health/Redis/PG |
| M3 | K8s deployment/sidecar/cron | 多副本和生产 | `CURRENT/PARTIAL`，需按目标集群验证 |
| M4 | Operator/CRD | 声明式运维 | `TARGET`，除非有当前代码和集群证据 |

不要仅以 README、Compose 文件或目标模板宣称部署已通过；必须记录目标环境、commit、migration、flags、health/readiness 和回滚输出。

## 2. 文档入口

- 环境规则与构建：`01-environments/`
- 数据库初始化和迁移：`02-database/`、`03-initialization/`
- 运维 runbook：`04-runbooks/`
- 可执行 systemd/K8s/Nginx/Prometheus：`../../deploy/`
- Installer：`../../installer/`
- 当前架构：`../03-design/01-architecture/architecture/ARCHITECTURE.md`
- 运行时请求流：`../03-design/01-architecture/architecture/runtime-request-flow.md`
- 测试矩阵：`../05-testing/01-strategy/test-matrix.md`

## 3. 依赖契约

生产启动前必须明确并验证：

```text
PostgreSQL URL / role / migration source
Redis URL / availability policy
credential encryption key
API/admin/JWT/cursor secrets
Maintain service URL and static distribution
ASM endpoint/event secret (if enabled)
OTel endpoint and headers (if enabled)
Prometheus scrape/auth configuration
artifact/storage/license authority configuration
```

缺少生产认证密钥、数据库关键依赖或无法建立租户安全上下文时，应 fail-closed 或保持 readiness failed；不得静默切到无认证、默认 tenant 或不一致的内存事实源。

## 4. Handler 和健康检查

- Gateway 默认监听端口和 `/healthz` 以当前 `docker-compose.yml`、`config` 和主入口为准。
- Compose、persistent Compose、systemd、K8s 的端口、环境变量、health path 必须通过 deployment contract test 对齐。
- HTTP/1.1 与 h2c 都必须使用包含 Maintain proxy/static 的最终 handler；不要只测试 handler 单体而忽略 `http.Server.Handler` 的最终装配。
- `/metrics`、Admin、质量服务和 live stream 的认证/暴露范围必须单独列入边界测试。

## 5. 数据库与迁移

- 生产 migration 只能使用声明的权威来源；`db/migrations`、`sql/migrations`、`deploy/sql` 和 installer embedded seed 的同步关系必须在变更中记录。
- migration 必须在隔离 PG 中验证 empty-up、upgrade-up、down/re-up、checksum/dirty、RLS/role 和 restore。
- 会话/正文 owner 切换前必须完成 body/turn backfill、dual-read、hash/orphan/duplicate 对账、replay 和 rollback drill。
- RLS 测试使用 `NOSUPERUSER` + `NOBYPASSRLS` 角色；缺 `TEST_DATABASE_URL` 或角色不合规时门禁为未通过，不是成功。

## 6. Redis、降级和持久化

Redis 承载 URSM、限流、session/cache、队列和锁等热状态；它不是长期事实源。每一项必须写明：

- Redis 不可用是 fail-open、fail-closed 还是 degraded；
- 多副本是否允许进程内 fallback；
- pending/fallback 是否 durable、如何 replay/DLQ；
- shutdown 如何 drain；
- 恢复后如何 reconciliation。

严格 TPM/付费额度不能无条件依赖各实例独立内存计数。

## 7. 升级与回滚

标准流程：

```text
preflight -> backup/snapshot -> migration compatibility check
  -> deploy artifact by platform/arch
  -> health/readiness -> smoke -> observation
  -> promote or rollback
```

Installer 的 artifact 选择、SHA256、backup、launcher 和 rollback 需与 Maintain distribution API 保持契约一致。任何 session/body ownership 或旧 reader/trigger 删除都必须有单独的 MIGRATION-GATE 批准；不得用二进制回滚替代数据回滚。

## 8. 安全与 secret hygiene

- 本文和部署样例不得包含真实 secret、API key、JWT、飞书/云存储凭据或完整生产 URL token。
- 历史部署文档如发现明文凭证，应执行脱敏、凭证轮换和审计；不要在新文档复制秘密。
- systemd 建议使用专用非 root 用户、`NoNewPrivileges`、受限文件系统和明确的 `TimeoutStopSec`；该值必须覆盖 HTTP drain + worker drain 的真实预算。
- `MAINTAIN_SERVICE_URL`、ASM endpoint 和 event secret 缺失时必须明确回退行为，并由 readiness/metrics 观测。

## 9. 最小验收清单

```text
[ ] clean build and go vet
[ ] targeted unit/contract tests
[ ] real PG migration replay and RLS negative matrix
[ ] Redis/queue/fallback recovery
[ ] HTTP/1.1 + h2c health and proxy smoke
[ ] v1 request/stream/cancel/retry smoke
[ ] admin/quality/metrics exposure audit
[ ] artifact/license/installer check
[ ] SIGTERM drain and timeout evidence
[ ] backup/restore or rollback drill
[ ] tenant/body/session reconciliation
```

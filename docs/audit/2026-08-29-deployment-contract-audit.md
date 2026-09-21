# 部署切换与数据闭环审计（2026-08-29）

**范围**：本地版本化部署、245/154 `deploy-seamless.sh`、Kubernetes manifest、candidate-failure 聚合与 RequestJourney 相关迁移。
**依据**：

- `docs/03-design/01-architecture/deploy-zero-downtime/zero-downtime-design.md`
- `docs/2026-08-19-p0-1-host-restart-safe-drain-design.md`
- `docs/06-deployment/02-database/local-pg-sync-from-252.md`
- `docs/audit/2026-08-18-deploy-gate-and-canary-design.md`
- `docs/adr/2026-08-28-requestjourney-journal-snapshot.md`

## 需求矩阵

| 需求 | 审计结论 | 本次处理 |
| --- | --- | --- |
| 发布必须在依赖可用后才接收流量/标记 verified | 原流程只检查 `/healthz`，不检查严格 `/readyz` | `host_wait_readyz` 进入 seamless 与本地发布门禁；verified 在其后才写入 |
| K8s 不得将依赖不可用 Pod 标为 Ready | readiness 错误复用 `/healthz` | readiness 改为 `/readyz`；保留 liveness `/healthz` |
| K8s 版本必须可追溯 | `latest + IfNotPresent` 可继续运行缓存旧镜像 | 固定 build tag `2.5.0-1805`，每次发布须与 `version.json` 同步更新 |
| 154 rollback 行为须与 target contract 一致 | contract 声称 runbook，seamless 实际 versioned rollback | contract 统一为 versioned |
| 元数据更新不可破坏 rollback 候选 | `deployment.json` 原地修改可能留下半写内容 | 使用临时文件、JSON 校验和同目录 rename 替换 |
| 本地单端口发布失败不得留下无服务 | 启动失败仅 warning，已切 current 且旧 PID 被杀 | 记录上一个 release；启动或 ready 失败时回切并重启旧 release |
| 2 秒零失败切换 | 当前仍是单端口 `stop → wait → start`，维护页会接管请求 | **未完成**：必须实施 Phase 2 双端口 canary、nginx active-upstream 原子切流、后台 worker 角色隔离和 SSE drain 测试后才能宣称达标 |

## 已发现并修复的问题

### P1：readiness 没有成为发布门禁

`/healthz` 只说明进程存活，DB/Redis 不可用时仍可能是 200。现在发布必须依序通过 liveness、`/readyz`、nginx 链路、业务 DB 门禁和 release identity，才写 `verified=true`。

### P1：Kubernetes 可以将未就绪实例导入 Service

Kubernetes liveness/readiness 均使用 `/healthz`。已把 readiness 改为 `/readyz`，并增加 35 秒终止宽限以匹配 gateway 的 graceful shutdown 预算。

### P1：Kubernetes 使用不可证明的镜像版本

已移除 `latest + IfNotPresent`。manifest 使用明确 release tag；发布脚本或变更者必须同步 tag、`version.json` 与运行时 `/version` 断言。

### P1：154 rollback policy 语义冲突

154 的 seamless 自动/手动回滚实际已依赖 verified release bundle，故 target contract 改为 `versioned`，避免不同入口产生相反的运维结论。

### P1：本地部署在启动失败时没有补偿

本地单端口 runner 不能在同端口提前 warm-up。修复采用可逆的 Phase 1 行为：在切换前保存旧 release；新版本启动或 strict readiness 失败则停止失败进程、恢复旧 `current` 并重启旧 release。

## 未修复风险与后续门禁

1. **P0 — 2 秒零中断仍未实现。** 当前 245/154 仍为 stop/start + upgrade page，典型窗口 5–8 秒。不能将本次修改描述为蓝绿完成。
2. **P1 — schema forward migration 与 binary rollback 不对称。** 迁移先于切换执行；自动回滚 binary 前必须在每个 migration 中保证旧 binary 可读新 schema，或采用明确的 data rollback runbook。
3. **P1 — 154/252 旧 nginx active 配置的 server 级 `error_page 503` 可能掩盖探针失败。** 该配置必须与 245 synthetic-599 策略对齐后才可用 nginx 状态作为 readiness 证据。
4. **P1 — K8s wrapper 声称支持的 252/kaixuan targets 与 `targets.sh` contract 不一致。** 在实际 K8s backend 接入前，应改为 fail-closed 的明确提示，而不是透传到必然拒绝的 host CLI。
5. **P1 — candidate failure → request journey → 最终成功缺 request 级 E2E。** 当前 writer、迁移、dispatch/journey 各自有覆盖，但需补第一个 candidate 失败、第二个成功、terminal 唯一成功的内存 E2E。

## 验证证据

- `bash tests/deploy_host_test.sh`：35 passed
- `bash tests/deploy_network_test.sh`：9 passed
- `bash tests/deploy_readiness_contract_test.sh`：通过
- `go test ./admin ./bg ./cmd/gateway ./domains/streaming/executors ./domains/requestjourney`：通过

审计结论：**可合并本次 Phase 1 发布安全收敛；不可据此发布或宣传 2 秒零中断能力。**

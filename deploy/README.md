# Deploy 物料导航

> 最后核对：2026-08-29
> 本页只列出现役入口、适用环境和证据要求；历史专项资料不等于发布流程。

## 1. 现役路径

| 路径 | 目录/入口 | 状态 | 适用环境 | 发布前必须有 |
| --- | --- | --- | --- | --- |
| 客户 host | `scripts/user/client-deploy.sh --install-mode host`、`scripts/lifecycle/` | `PARTIAL` | customer-host | artifact/checksum、init、三段 preflight、升级回滚 |
| 客户 Docker | `scripts/user/client-deploy.sh --install-mode docker` | `PARTIAL` | customer-docker | Compose/PG/Redis 契约、镜像 checksum、回滚证据 |
| local/dev | `scripts/local-host-deploy*.sh`、`docker-compose.dev-research.yml` | `CURRENT/PARTIAL` | local/dev | 本地测试；不得代替生产验证 |
| 245/154 | `scripts/deploy*.sh`、`scripts/deploy-lib/` | `CURRENT/PARTIAL` | staging/production target | 目标 contract、migration、readiness、canary、回滚和监控 |
| K8s test | `deploy/k8s/` | `CURRENT/PARTIAL` | `k8s-test` / `pms-test` | 集群、Secret/PVC、Service、readiness 和 migration 顺序 |
| 数据库迁移 | `sql/migrations/`、`db/migrations/`、`deploy/sql/` | `CURRENT/PARTIAL` | 按目标授权 | unique/down/re-up、RLS、兼容性和备份恢复 |
| 观测 | `deploy/prometheus/`、`deploy/grafana/` | `PARTIAL` | staging/production | 规则实际加载、指标命名、dashboard、告警路由 |

详细环境命名、端口和支持矩阵见 [环境总览](../docs/06-deployment/01-environments/README.md)。

## 2. 目录边界

- `deploy/`：仓库内 manifest、init 模板、Nginx/Prometheus 物料和部署参考。
- `scripts/deploy/`：客户/服务安装所需的 systemd、launchd、Windows 模板。
- `scripts/user/`：客户级安装、升级、回滚编排 CLI。
- `scripts/lifecycle/`：host 服务注册、启动、停止和 preflight。
- `docs/06-deployment/`：面向操作者的环境、数据库和 runbook 说明。

修改 service identity 时必须同步对应脚本、manifest、文档和 contract test；不要把同名脚本当成互相独立的实现。

## 3. K8s 当前边界

`deploy/k8s/llm-gateway-go-deployment.yaml` 当前绑定 `pms-test`/测试目标的假设，不能直接作为 production manifest。当前缺少或未统一验证的生产资源包括 Ingress、NetworkPolicy、PDB、HPA、ServiceAccount/RBAC、migration Job、TLS、备份恢复和 canary/blue-green 配置。

在这些资源和真实集群证据完成前，K8s 状态保持 `PARTIAL`，不得标记 `RELEASE_READY`。

## 4. Migration 与回滚门禁

标准顺序：

```text
备份/快照 -> migration compatibility check -> artifact deploy
  -> /healthz -> /readyz -> /version -> smoke -> observation
  -> promote 或 rollback
```

migration 先于 binary 切换时，必须证明旧 binary 能读取新 schema；binary rollback 不能代替 data rollback。Session V2、session bodies/turns、provider error 和 RequestJourney 相关迁移必须有独立的 upgrade/down/re-up、RLS 和对账证据。

## 5. Legacy 物料

以下路径保留用于兼容/历史追溯，不是新部署推荐入口：

- `install.sh` 与 `installer/`：旧版 one-click/full-stack installer，使用前先核对当前环境总览。
- `deploy/one-click/`：历史一键部署文档，端口/仓库/目录语义可能过期。
- `deploy/DEPLOYMENT_GUIDE.md`：审批通知专项 runbook，不是 Gateway 总部署指南；其中不得保留真实凭据。
- `QUICK-START.md`：当前内容是 GLM-5.2 事故 runbook，不是通用安装指南。

历史资料如需更新，先标注 `legacy`/`archive` 和最后核对日期，不要复制旧 secret 或旧绝对路径。

## 6. 最小验收

```bash
go build ./...
go vet ./...
git diff --check
bash -n scripts/user/*.sh scripts/lifecycle/*.sh scripts/deploy-lib/*.sh
```

此外必须按目标执行受影响测试、K8s 多文档 YAML 解析、migration contract、真实依赖验证和回滚演练。缺少真实环境或授权时输出 `manual_required`。

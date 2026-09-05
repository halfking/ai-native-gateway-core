# 环境总览

> 状态：CURRENT / DOCUMENTATION BASELINE
> 最后核对：2026-08-29
> 本页是 `docs/06-deployment/01-environments/` 的唯一环境入口。

## 1. 证据等级

| 等级 | 含义 |
| --- | --- |
| `IMPLEMENTED` | 代码、脚本或 manifest 已存在，并通过静态检查或单元测试。 |
| `LOCAL_VERIFIED` | 在本地或隔离测试依赖中执行过可重复验证。 |
| `REAL_DEPENDENCY_VERIFIED` | 在指定 PostgreSQL、Redis、provider、Prometheus、浏览器或集群中取得证据。 |
| `RELEASE_READY` | 真实依赖验证、回滚演练、监控门禁和责任人记录均完成。 |

代码完成不等于 staging 或生产完成。当前大多数部署能力最高只能标为 `IMPLEMENTED` 或 `LOCAL_VERIFIED`。

## 2. 规范环境和目标映射

| 规范环境 | 目标/别名 | 用途 | 数据与凭据 | 当前状态 |
| --- | --- | --- | --- | --- |
| `local` | 本机 | 开发、调试、契约测试 | 合成/脱敏数据；禁止生产 secret | `LOCAL_VERIFIED`（按脚本/测试） |
| `dev` | dev research | 功能开发和实验 | 独立 DB/Redis；禁止客户真实数据 | `PARTIAL` |
| `test` | `pms-test`、252 测试库 | 集成测试和 migration replay | 测试 DB；真实 provider 需授权 | `PARTIAL` |
| `staging` | 245、154 staging | 发布前回归、canary、回滚演练 | staging DB/Redis；最小权限 | `NOT_RELEASE_READY` |
| `production` | 154 production / RDS production | 生产流量 | 生产 DB/Redis/secret；仅授权窗口 | `NOT_RELEASE_READY` |
| `customer-host` | 客户 Linux/macOS/Windows 裸机 | 客户离线或不使用 Docker | 客户自有依赖和 secret | Linux/macOS `PARTIAL`；Windows `NOT_RELEASE_READY` |
| `customer-docker` | 客户单机 Compose | 客户单机全栈或外部依赖 | Compose/外部 DB/Redis 需明确选择 | `PARTIAL` |
| `k8s-test` | namespace `pms-test` | K8s manifest 验证 | 测试 Secret/PVC | `IMPLEMENTED/PARTIAL`，不可直接生产 |

245/154/252 是目标标识，不是环境名；执行记录必须同时写规范环境名。`staging`、`production`、`customer-*` 不能互相替代。

## 3. 端口和健康检查契约

| 模式 | Gateway 进程/容器监听 | 宿主暴露 | 反向代理 |
| --- | ---: | ---: | --- |
| local host | `8781` | `8781` | 无 |
| customer host | 由 release/config 决定，推荐 `8781` | 由 init/代理决定 | 可选 |
| customer Docker | 容器内 `8781` | `8080 -> 8781`（`client-deploy.sh` 契约） | 可选 |
| 245/154 | 由 target contract 确定 | 通常由 Nginx/LB 暴露 | 必须记录实际 upstream |
| k8s-test | Service targetPort `8781` | 当前 manifest 为测试 NodePort | 不提供通用生产 Ingress |

| 端点 | 语义 | 期望 |
| --- | --- | --- |
| `GET /healthz` | L1 liveness，只证明进程存活 | 进程正常时 `200` |
| `GET /readyz` | L2 readiness，严格检查 DB + Redis | 依赖都可用 `2xx`，否则 `503` |
| `GET /version` | L3 release identity | version、git sha、build seq/date |

没有 Redis 的 Compose 组合不得声称 `/readyz` 完整通过；应明确 degraded、外部 Redis 或未验证状态。

## 4. 部署入口

- 客户 host：`scripts/user/client-deploy.sh --install-mode host` 或 Maintain 分发的 host 脚本；底层服务注册在 `scripts/lifecycle/`。
- 客户 Docker：推荐 `scripts/user/client-deploy.sh --install-mode docker`；`install-docker.sh` 是底层/轻量脚本。
- 本机开发：`scripts/local-host-deploy.sh`、`scripts/local-host-deploy-test.sh`，不等同客户生产安装。
- 中心 245/154：使用 target/deploy pipeline，必须记录目标、版本、迁移、readiness、回滚和监控证据。
- K8s 测试：`deploy/k8s/llm-gateway-go-deployment.yaml` 等 manifest，仅适用于声明的测试集群。

## 5. 数据库、Redis 和权限边界

每次发布必须记录 PostgreSQL role/迁移来源、Redis 可用性策略、secret 变量名、schema 向前兼容性、binary rollback 与 data rollback 关系，以及 session/body/turn 回填和对账证据。没有授权、脱敏方案或回滚路径时只能标记 `manual_required`，不得执行迁移、重启或生产流量操作。

## 6. 平台支持矩阵

| 平台/架构 | Host | Docker | Artifact 要求 | 状态 |
| --- | --- | --- | --- | --- |
| Linux amd64 | 支持路径 | 支持路径 | `linux-amd64` 包/镜像 | `PARTIAL` |
| Linux arm64 | 支持路径 | 支持路径 | `linux-arm64` 包/镜像 | `PARTIAL` |
| macOS arm64/amd64 | launchd 路径 | Docker Desktop Linux 容器 | host 包或 Linux 镜像 | `PARTIAL` |
| Linux loong64 | 需 `LOONG64_OK=1` | 需对应镜像 | 发布物料必须存在 | `NOT_DEFAULT` |
| Windows host | 当前脚本契约未闭合 | — | Gateway Windows artifact | `NOT_RELEASE_READY` |
| Windows Docker | 无统一正式入口 | 未承诺 | Windows Docker/WSL 契约 | `NOT_RELEASE_READY` |
| sw_64 / riscv64 | 不支持 | 不支持 | 不得绕过架构门禁 | `UNSUPPORTED` |

“脚本能运行”不等于“平台已验收”；必须有 artifact、安装、启动、三段健康检查、升级和回滚证据。

## 7. 当前发布边界

当前可以描述为：**Phase 1 发布安全收敛**，包括 readiness 门禁、release identity、失败回切和回滚路径的代码/脚本改进。

当前不能描述为“2 秒零中断”、生产已验收、所有客户平台已支持或 Session V2 staging 已完成。2 秒零中断仍需要双端口 candidate/active、Nginx 原子切流、worker 角色隔离、SSE drain 和真实演练证据。

## 8. 相关入口

- [客户安装](customer-install/README.md)
- [部署总入口](../README.md)
- [本机部署](deployment/LOCAL-HOST-DEPLOY.md)
- [Session V2 staging 验证](../../staging-validation-progress-20260829.md)
- [部署切换审计](../../audit/2026-08-29-deployment-contract-audit.md)
- [下一阶段主代理提示词](../../next-phase-master-prompt-20260829.md)

# 客户级部署

> 状态：PARTIAL / 客户安装契约
> 最后核对：2026-08-29

本目录描述客户机器上的 Gateway 安装方式。它不等同于 local 开发、245/154 中心部署或 `pms-test` K8s manifest。所有“支持”都必须以实际 artifact、安装、启动、健康检查、升级和回滚证据为准。

## 1. 先选择安装方式

| 方式 | 推荐入口 | 适用场景 | 当前状态 |
| --- | --- | --- | --- |
| 客户 host | `scripts/user/client-deploy.sh --install-mode host`；Linux/macOS 底层为 `install-host.sh` | 不允许 Docker、离线单机、客户自管 init | Linux/macOS `PARTIAL` |
| 客户 Docker | `scripts/user/client-deploy.sh --install-mode docker` | 单机 Compose 或客户已有外部依赖 | `PARTIAL`，需确认 PG/Redis 契约 |
| local host | `scripts/local-host-deploy.sh` | 开发/调试 | 不是客户生产路径 |
| K8s | `deploy/k8s/` | 当前仅测试集群 | `NOT_RELEASE_READY` for production |

`install-docker.sh` 是底层/轻量脚本，适合已明确 `COMPOSE_DIR`、镜像和依赖的维护场景；不要把它和完整 `client-deploy.sh` 的编排能力混写。

## 2. OS × 安装模式矩阵

| OS | 默认服务根/安装根 | 模式 | init/runtime | 状态 |
| --- | --- | --- | --- | --- |
| Linux amd64/arm64 | `/opt/llm-gateway`（不可写时使用 `~/.local/llm-gateway`） | host | systemd | `PARTIAL` |
| macOS amd64/arm64 | 下载/解压根由 `INSTALL_ROOT` 决定；服务配置可能位于 `/usr/local/etc/llm-gateway` | host | launchd | `PARTIAL`，需 clean-machine 验证 |
| Linux | `/opt/llm-gateway` 或显式 `INSTALL_ROOT` | docker | Docker Engine + Compose V2 | `PARTIAL` |
| macOS | 显式 `COMPOSE_DIR`/`INSTALL_ROOT` | docker | Docker Desktop Linux containers | `PARTIAL` |
| Windows host | `D:\kaixuan\llm-gateway` 或 `C:\llm-gateway` | host | PowerShell service script | `NOT_RELEASE_READY`，脚本契约待修复 |
| Windows Docker | 需客户自备 WSL/Docker 契约 | docker | 无统一官方入口 | `NOT_RELEASE_READY` |

## 3. 架构支持

| 架构 | Host | Docker | 约束 |
| --- | --- | --- | --- |
| `amd64` / `x86_64` | 支持候选 | 支持候选 | 必须有对应 checksum artifact |
| `arm64` / `aarch64` | 支持候选 | 支持候选 | 必须有对应 checksum artifact |
| `loong64` / `loongarch64` | 默认阻断 | 默认阻断 | 显式设置 `LOONG64_OK=1` 且 artifact 必须存在 |
| `sw_64` | 不支持 | 不支持 | 脚本应 fail-closed |
| `riscv64` | 不支持 | 不支持 | 脚本应 fail-closed |

“国产 OS/CPU 支持”必须拆成具体 OS、架构、artifact 和 Docker runtime 证据；旧文档的笼统声明不构成支持承诺。

## 4. 端口与健康检查

| 模式 | 进程/容器监听 | 客户端访问 |
| --- | ---: | ---: |
| local host | `8781` | `http://127.0.0.1:8781` |
| customer host | 推荐 `8781`，以 release/config 为准 | 由客户代理或本机端口决定 |
| customer Docker | 容器内固定 `8781` | `8080 -> 8781`（`client-deploy.sh` 默认契约） |
| K8s test | Service targetPort `8781` | 按测试 manifest 的 Service/NodePort |

三段检查：

```text
/healthz  = 进程存活（liveness）
/readyz   = DB + Redis 严格就绪（readiness；失败为 503）
/version  = version/git_sha/build_seq/build_date（release identity）
```

没有 Redis 的安装组合不得声称 `/readyz` 完整通过；应记录 degraded 或外部 Redis 依赖。

## 5. 推荐命令

```bash
# 客户 host：实际支持 --install-mode 的是 client-deploy.sh
bash scripts/user/client-deploy.sh --install-mode host deploy

# 客户 Docker：推荐使用完整编排入口
INSTALL_ROOT=/opt/llm-gateway \
  bash scripts/user/client-deploy.sh --install-mode docker deploy

# 底层 Docker 脚本：必须显式理解 COMPOSE_DIR 语义
COMPOSE_DIR=/opt/llm-gateway-docker \
  bash scripts/user/install-docker.sh

# 预检（端口按实际模式调整）
bash scripts/lifecycle/preflight.sh \
  --base-url http://127.0.0.1:8781 \
  --expected-version <bundle-version>
```

`install-host.sh` 本身不解析 `--install-mode`；不要使用 `bash install-host.sh --install-mode host`。

## 6. Secret、目录和升级边界

- 命令示例只能使用 `<...>` 占位符，不写 DSN、token、密码或 App Secret。
- `INSTALL_ROOT` 是服务/版本数据根；`COMPOSE_DIR` 是底层 Compose 文件目录，只有脚本明确读取时才会生效。
- macOS 的下载缓存目录与 launchd 服务根可能不同，必须以 release 中的 service installer 和生成的 plist 为准。
- Windows host 当前脚本仍包含 Maintain 残留，不得按本页宣称为可发布 Gateway 服务；修复脚本并完成 clean-machine 验证前保持 `NOT_RELEASE_READY`。
- 升级前必须有 bundle checksum、配置备份、schema 向前兼容性检查和可执行回滚路径。

## 7. 子文档

- [Linux host](linux-host.md)
- [Linux Docker](linux-docker.md)
- [macOS host](macos-host.md)
- [macOS Docker](macos-docker.md)
- [Windows host](windows-host.md)

## 8. 相关入口

- [环境总览](../README.md)
- [部署总入口](../../README.md)
- [客户脚本](../../../../scripts/user/)
- [生命周期脚本](../../../../scripts/lifecycle/)
- [下一阶段主代理提示词](../../../next-phase-master-prompt-20260829.md)

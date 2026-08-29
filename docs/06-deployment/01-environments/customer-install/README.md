# 客户级部署 — llm-gateway-go

面向客户机器 / 服务器的端到端部署脚本与文档。复用 `ai-native-maintain` 已实现的客户级架构（catalog / ticket / sha256 / install-scripts），在 llm-gateway-go 仓库建立镜像对应关系。

## OS × install-mode 矩阵

| OS | 默认 INSTALL_ROOT | install-mode | init 系统 | 服务注册脚本 |
| --- | --- | --- | --- | --- |
| macOS (Darwin) | `~/Downloads/llm-gateway-files` | host | launchd | `lifecycle/install-service.sh` + `deploy/launchd/com.kaixuan.llm-gateway-go.plist` |
| macOS | 同上 | docker | docker compose | — |
| Linux | `/opt/llm-gateway` | host | systemd | `lifecycle/install-service.sh` + `deploy/systemd/llm-gateway-go.service` |
| Linux | 同上 | docker | docker compose | — |
| Windows (D: 可写) | `D:\kaixuan\llm-gateway` | host | NSSM / sc.exe | `deploy/windows/install-service.ps1` |
| Windows (无 D: / 不可写) | `C:\llm-gateway` | host | NSSM / sc.exe | 同上 |
| Windows | `C:\llm-gateway` | docker | docker compose | — |

INSTALL_ROOT 可被 `INSTALL_ROOT` 环境变量覆盖；`--install-mode host|docker|auto` 强制选择模式（`auto` 默认看 `INSTALL_ROOT/docker-compose.yml` 是否存在）。

## 国产芯片 / 架构支持

| uname -m | catalog ARCH | 国产芯片覆盖 | 备注 |
| --- | --- | --- | --- |
| `x86_64` / `amd64` | `amd64` | 海光、兆芯 (x86) | CI 默认发 |
| `aarch64` / `arm64` | `arm64` | 鲲鹏、飞腾 | CI 默认发 |
| `loongarch64` / `loong64` | `loong64` | 龙芯 | **默认严格**：需显式 `LOONG64_OK=1` 才放行；CI 默认不发 loong64 artifact（`package.sh --include-loong64` 才发） |

未支持：`sw_64` (申威)、`riscv64` —— 见 `scripts/user/install-host.sh` ARCH 检测；执行会失败并给出清晰错误。

## 入口

### 1. Maintain API 一行式安装（生产）

```bash
# host 路径（裸机 + systemd/launchd）
curl -fsSL https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/llm-gateway-host | bash

# docker 路径
curl -fsSL https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/llm-gateway-docker | bash

# 升级（host+compose 双模式自动检测）
curl -fsSL https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/llm-gateway-upgrade | bash
```

> **注意**：上述 endpoint path 由 maintain command-builder 生成（见 maintain 仓库 `commandBuilder.ts`）；本仓库只提供脚本。脚本同时也可以离线 `bash install-host.sh` 直接跑。

### 2. 客户本地直装（开发机、预发布）

```bash
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git
cd llm-gateway-go/scripts/user
bash install-host.sh                      # 自动探测 OS + ARCH + INSTALL_ROOT
bash install-host.sh --install-mode host  # 强制 host 模式
INSTALL_ROOT=/custom/path bash install-docker.sh
```

## 子文档

- [macOS host](macos-host.md) — launchd 路径
- [macOS docker](macos-docker.md) — Docker Desktop 路径
- [Linux host](linux-host.md) — systemd 路径
- [Linux docker](linux-docker.md) — Docker Engine 路径
- [Windows host](windows-host.md) — NSSM / sc.exe 路径

## 验证 / 回退

每个 install-mode 都有对应 smoke：

| 路径 | smoke 命令 |
| --- | --- |
| 任何 | `bash scripts/lifecycle/preflight.sh --base-url http://127.0.0.1:8781 --expected-version <bundle>` |
| docker | `docker compose -f $INSTALL_ROOT/docker-compose.yml ps` |
| 升级 | `bash scripts/user/upgrade.sh list` 查最新；`bash scripts/user/upgrade.sh run` 升级；失败自动回退 |

三段健康检查（preflight.sh）：

| 段 | 端点 | 期望 |
| --- | --- | --- |
| L1 进程存活 | `GET /healthz` | 200 + `status:ok` + `version`/`git_sha`/`build_seq`/`build_date` |
| L2 依赖就绪 | `GET /readyz` | 200（DB+Redis 都通） / 503（任一不通） |
| L3 版本对齐 | `GET /version` | 200 + `version` 与切完后的 bundle version.json 匹配 |

## 客户级 vs 服务级（245）部署差异

| 维度 | 服务级（245 部署） | 客户级（本目录） |
| --- | --- | --- |
| 位置 | `/opt/ai-native-maintain/` 等 | macOS `~/Downloads/llm-gateway-files` / Linux `/opt/llm-gateway` / Windows `D:\kaixuan\llm-gateway` |
| 启动 | systemd unit | systemd (Linux) / launchd (macOS) / NSSM (Windows) |
| 端口 | 8000（nginx → upstream） | 8080（直接 host listen；docker 路径也是 8080） |
| 版本保留 | 5 | 3（保守磁盘） |
| 数据 | 服务级 host bind-mount | 客户级 host 直装 / docker compose |
| Prune 时机 | 每次 deploy 后 | 每次 upgrade 后 |
| 健康检查 | systemd + curl | preflight.sh 三段 |

## 故障排查

| 现象 | 原因 | 修复 |
| --- | --- | --- |
| install-host.sh 报 "checksum MISMATCH" | catalog sha256 与下载包不符 | 重跑；如持续失败设 `ALLOW_UNVERIFIED_PACKAGE=1` 临时绕过 |
| install-docker.sh 报 "no published sha256" | catalog 没该 version 的 docker image artifact | 看 maintain 发布是否成功；重发 / 跳过 docker 走 host |
| preflight.sh L2 /readyz 503 | DB 或 Redis ping 失败 | `docker exec llm-gateway-pg pg_isready` / `redis-cli -h ... ping` |
| preflight.sh L3 /version mismatch | 切完后的 binary 与预期版本不符（rsync 没覆盖到、或旧进程没退） | `systemctl status llm-gateway-go` / `journalctl -u llm-gateway-go -n 50` |
| loong64 exit 2 | CI 默认不发 loong64 artifact | 设 `LOONG64_OK=1`；或让 CI 用 `package.sh --include-loong64` 重发 |

## 相关文件

- `scripts/user/{install-host,install-docker,upgrade,client-deploy}.sh` — 客户级 CLI
- `scripts/user/lib/common.sh` — 共享 utils（detect_platform / detect_arch / detect_install_root / sha256 / catalog / ticket）
- `scripts/lifecycle/{lib,install-service,service-runner,preflight}.sh` — 服务生命周期（host 路径用）
- `scripts/deploy/{systemd,launchd,windows}/*` — init 系统单元模板
- [LOCAL-HOST-DEPLOY.md](../LOCAL-HOST-DEPLOY.md) — 开发机本机部署（不走 systemd/launchd）

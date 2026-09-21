# 一键部署指南（PG + Redis + LLM Gateway）

> 整合 `install.sh`、`deploy/one-click/` 与 `llm-gw-installer`，在目标机一次性拉起
> **PostgreSQL（Citus 镜像）+ Redis + llm-gateway-go** 全栈。

## 前置条件

| 系统 | 要求 |
|------|------|
| Linux | Docker 或 Podman；无则安装器尝试自动安装 Docker |
| macOS | Docker Desktop / OrbStack |
| Windows | Docker Desktop + WSL2 |

端口默认：`8781`（网关）、`5432`（PG）、`6379`（Redis）。

## 方式 A — 从 Git 源码一键部署（开发/内网）

```bash
git clone https://github.com/halfking/ai-native-gateway-core.git
cd ai-native-gateway-core   # 或本仓库 llm-gateway-go-cursor

# Linux / macOS — 交互向导
bash deploy/one-click/install.sh

# 静默（CI / 脚本）
bash deploy/one-click/install.sh --non-interactive --dir ~/llm-gateway

# 等价快捷命令
bash deploy/one-click-install.sh --non-interactive
```

```powershell
# Windows（PowerShell 管理员）
powershell -ExecutionPolicy Bypass -File deploy\one-click\install.ps1
powershell -ExecutionPolicy Bypass -File deploy\one-click\install.ps1 -NonInteractive
```

安装器将自动：

1. 检测 OS / Docker / 端口 / 磁盘
2. 生成 `.env`（数据库密码、Redis 密码、JWT 等）
3. 拉取或加载 `kx-citus` / `kx-redis` / `kx-llm-gateway-go` 镜像
4. 应用内嵌 SQL（`00-prereqs` / `01-schema` / `02-seed`）
5. `docker compose up -d` 启动全栈
6. 输出部署报告

## 方式 B — 从官方离线包部署（生产推荐）

### 1. 下载对应平台包

访问 https://llmgo.kxpms.cn/download 或：

```bash
# Linux x86_64 示例
curl -fLO https://download.kxpms.cn/llm-gateway-go/latest/llm-gateway-go-2.4.6-linux-amd64-offline.tar.gz
tar xzf llm-gateway-go-2.4.6-linux-amd64-offline.tar.gz
cd llm-gateway-go-2.4.6-linux-amd64-offline
./install.sh install
```

### 2. 脚本自动拉包（需外网）

```bash
bash deploy/one-click/install.sh --download v2.4.6 --non-interactive
```

```powershell
powershell -ExecutionPolicy Bypass -File deploy\one-click\install.ps1 -DownloadVersion v2.4.6 -NonInteractive
```

## 方式 C — 离线包内直接安装

解压后的目录已包含：

- `gateway` — 网关二进制
- `llm-gw-installer-<os>-<arch>` — 安装器
- `web/` — 前端静态资源
- `sql/baseline/` — 数据库基线脚本副本
- `install.sh` — 与仓库根目录相同入口

```bash
./install.sh install --skip-prompt
# 或
./llm-gw-installer-linux-amd64 install --dir ~/llm-gateway --skip-prompt
```

## 激活

安装完成后：

- 在线：https://llmgo.kxpms.cn/activate
- 离线：https://llmgo.kxpms.cn/offline-activation
- CLI：`llm-gw-installer activate --mode trial --email you@company.com --agree`

## 运维发布（245 中心机）

```bash
# 1. 部署网关前端+二进制到 245
bash scripts/deploy-seamless.sh deploy 245

# 2. 构建多平台离线包并写入 DB + 下载目录
bash scripts/build-offline-packages.sh v2.4.6 --out /var/www/download/llm-gateway-go/v2.4.6
bash scripts/publish-download-release.sh v2.4.6

# 3. API 冒烟
bash scripts/test-public-download-api.sh https://llmgo.kxpms.cn
```

## 相关文档

- [分发与激活 — 部署架构](../../docs/分发与激活/03-部署架构.md)
- [installer README](../../installer/README.md)
- [无缝部署指南](../../docs/分发与激活/seamless-deployment-guide.md)

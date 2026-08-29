# macOS Docker 安装（Docker Desktop）

## 快速上手

```bash
# 1. 一行式（生产）
curl -fsSL https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/llm-gateway-docker | bash

# 2. 本地脚本（开发）
cd llm-gateway-go/scripts/user
bash install-docker.sh
```

macOS Docker 路径与 Linux 同源码——`install-docker.sh` 与 `client-deploy.sh` 都强制 `PLATFORM=linux`（容器内跑 Linux 镜像）。

## 前置条件

- Docker Desktop for Mac 已安装并运行（脚本会自动检测；缺失会打印安装链接）
- Apple Silicon（M1/M2/M3）默认 `linux/arm64`；Intel Mac 默认 `linux/amd64`

## 路径布局

```
~/Downloads/llm-gateway-files/
├── docker-compose.yml          # gateway + pg 容器定义
├── .env                        # mode 0600，PG 密码 + gateway secrets
├── .env.bak/                   # 升级前自动备份
├── versions/
│   └── <v>/docker/linux-arm64/   # 镜像 tar + SHA256SUMS
├── releases/
│   └── <v>-<ts>/              # 升级前快照（rollback 用）
├── data/                       # bind-mount 给 llm-gateway-pg
└── CHANGELOG.log
```

## 升级

```bash
# 检测到 docker-compose.yml → INSTALL_MODE=compose → 走 compose 升级路径
bash scripts/user/upgrade.sh run

# 查看镜像缓存
ls versions/

# 回退到上一个 verified bundle
bash scripts/user/upgrade.sh rollback
```

Compose 升级三段：
1. `docker compose stop`（gate 流量）
2. `docker load -i versions/<new>/docker/linux-<arch>/gateway.tar` + sha256 校验
3. `docker compose up -d --force-recreate`
4. preflight.sh 三段

失败自动回退（从 `releases/<v>-<ts>/.env.snapshot + docker-compose.yml.snapshot` 恢复）。

## 回退（人工）

```bash
# 看快照
ls releases/

# 恢复 .env.snapshot + compose.snapshot + 旧 docker-compose.yml
cp releases/<v>-<ts>/.env.snapshot .env
cp releases/<v>-<ts>/docker-compose.yml.snapshot docker-compose.yml
docker compose up -d
```

## 故障排查

```bash
# 容器状态
docker compose ps
docker compose logs -f gateway
docker compose logs -f llm-gateway-pg

# 健康检查
curl http://127.0.0.1:8080/healthz | jq
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:8080/version | jq

# PG 密码对齐（.env vs 容器）
docker exec llm-gateway-pg pg_isready -U llm_user
docker exec -e PGPASSWORD=$(grep POSTGRES_PASSWORD .env | cut -d= -f2) llm-gateway-pg \
  psql -U llm_user -d llm_gateway -tAc 'SELECT 1'
```

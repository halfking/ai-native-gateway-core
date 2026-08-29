# Linux Docker 安装（Docker Engine）

## 快速上手

```bash
# 1. 一行式（生产）
curl -fsSL https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/llm-gateway-docker | bash

# 2. 本地脚本（开发）
cd llm-gateway-go/scripts/user
sudo bash install-docker.sh
```

`install-docker.sh` 内部会自动安装 Docker（如缺失），写入 docker-compose.yml，下载 + load 镜像，启动。

## 前置条件

- Linux kernel ≥ 3.10
- 联网（首次安装需要从 maintain API 拉 docker image tar）
- root 或 sudo 权限（创建 `/var/run/docker.sock` 权限 + `/opt/llm-gateway`）

## 支持 ARCH

| 架构 | catalog ARCH | 国产芯片 |
| --- | --- | --- |
| x86_64 | amd64 | 海光、兆芯 |
| aarch64 | arm64 | 鲲鹏、飞腾 |
| loongarch64 | loong64（需 `LOONG64_OK=1`） | 龙芯 |

## 路径布局

```
/opt/llm-gateway/
├── docker-compose.yml
├── .env                              # mode 0600
├── .env.bak/                         # 升级前自动备份
├── versions/<v>/docker/linux-<arch>/
│   ├── gateway.tar                   # 容器镜像 tarball
│   ├── pg17-circus.tar               # pg image（可选）
│   └── SHA256SUMS
├── releases/<v>-<ts>/                # 升级前快照
├── data/                             # bind-mount 给 llm-gateway-pg
├── logs/  backups/
└── CHANGELOG.log
```

## 升级

```bash
bash scripts/user/upgrade.sh run         # auto 模式
INSTALL_MODE=compose bash scripts/user/upgrade.sh run   # 强制 compose
```

Compose 升级三段：
1. `docker compose stop` 流量隔离
2. `docker load -i versions/<new>/docker/linux-<arch>/gateway.tar` + `sha256 -c SHA256SUMS` 校验
3. `docker compose up -d --force-recreate`
4. preflight.sh 三段

失败自动 `releases/<v>-<ts>/.env.snapshot + docker-compose.yml.snapshot` 回退。

## 回退（人工）

```bash
ls /opt/llm-gateway/releases/
cp /opt/llm-gateway/releases/<v>-<ts>/docker-compose.yml.snapshot /opt/llm-gateway/docker-compose.yml
cp /opt/llm-gateway/releases/<v>-<ts>/.env.snapshot /opt/llm-gateway/.env
docker compose up -d
```

## 故障排查

```bash
docker compose -f /opt/llm-gateway/docker-compose.yml ps
docker compose logs -f gateway
docker compose logs -f llm-gateway-pg

# PG 密码对齐
docker exec -e PGPASSWORD=$(grep POSTGRES_PASSWORD /opt/llm-gateway/.env | cut -d= -f2) llm-gateway-pg \
  pg_isready -U llm_user

# 健康检查
curl http://127.0.0.1:8080/healthz | jq
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:8080/version | jq
```

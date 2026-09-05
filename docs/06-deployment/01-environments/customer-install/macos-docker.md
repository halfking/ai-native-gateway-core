# macOS Docker 安装（Docker Desktop）

> 状态：PARTIAL；需要 Docker Desktop、真实 DB/Redis 和 clean-machine 验收。

## 推荐入口

```bash
INSTALL_ROOT="$HOME/Downloads/llm-gateway-files" \
  bash scripts/user/client-deploy.sh --install-mode docker deploy
```

macOS 使用 Linux 容器。`client-deploy.sh` 是客户编排入口；`install-docker.sh` 是底层脚本，使用时应显式设置 `COMPOSE_DIR`，不能假定 `INSTALL_ROOT` 会被它读取。

## 前置条件

- Docker Desktop 已安装并运行。
- Apple Silicon 使用 `linux/arm64`，Intel 使用 `linux/amd64`。
- 对应 release 必须提供镜像 tar、SHA256SUMS、Gateway 配置模板和版本 metadata。
- PostgreSQL 与 Redis 必须明确是 Compose 服务还是外部服务；未提供 Redis 时不能声称 `/readyz` 完整通过。

## 目录和端口

- `INSTALL_ROOT`：客户编排的服务/版本根，默认建议 `~/Downloads/llm-gateway-files`，也可显式改为稳定路径。
- `COMPOSE_DIR`：仅底层 `install-docker.sh` 的 compose 输出目录。
- 容器内 Gateway 固定监听 `8781`；推荐宿主 `8080 -> 8781`。
- `.env` 必须为 `0600`，secret 只通过环境变量或客户密钥管理提供。

## 安装后验证

```bash
docker compose -f "$HOME/Downloads/llm-gateway-files/docker-compose.yml" ps
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/version
bash scripts/lifecycle/preflight.sh \
  --base-url http://127.0.0.1:8080 \
  --expected-version <bundle-version>
```

## 升级与回滚

```bash
bash scripts/user/upgrade.sh run
bash scripts/user/upgrade.sh list
bash scripts/user/upgrade.sh rollback
```

升级必须先备份 `.env`/compose，校验新镜像 checksum，再启动并执行三段 preflight。失败时恢复最近 release snapshot；若 migration 已执行，必须使用兼容性 runbook，不得只恢复旧 binary。

## 故障排查

```bash
docker compose ps
docker compose logs --tail=100 gateway
docker compose exec <db-service> pg_isready
docker compose exec <redis-service> redis-cli ping
```

Windows Docker 当前没有统一官方入口；不能将本页 macOS Docker 流程直接移植到 Windows。

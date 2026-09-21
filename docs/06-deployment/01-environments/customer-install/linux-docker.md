# Linux Docker 安装（Docker Engine + Compose V2）

> 状态：PARTIAL；必须先确认 PostgreSQL/Redis 是 Compose 内置还是外部依赖。

## 推荐入口

```bash
INSTALL_ROOT=/opt/llm-gateway \
  bash scripts/user/client-deploy.sh --install-mode docker deploy
```

`install-docker.sh` 是底层/轻量脚本，默认写入 `COMPOSE_DIR="$PWD/llm-gateway-docker"`，不会因为设置 `INSTALL_ROOT` 自动改变目录。使用它时必须显式设置 `COMPOSE_DIR` 并自行确认依赖契约：

```bash
COMPOSE_DIR=/opt/llm-gateway-docker \
  bash scripts/user/install-docker.sh
```

## 前置条件

- Linux amd64/arm64；loong64 需 `LOONG64_OK=1` 和实际镜像 artifact。
- Docker Engine + Compose V2；首次安装需要镜像下载或本地 tar 包。
- Gateway 容器内部监听 `8781`。
- `/readyz` 完整通过要求 PostgreSQL 和 Redis 都可用；若 compose 只包含 Gateway/PG，必须标记 Redis 为外部依赖或 readiness 未验证。

## 路径和端口

- 完整客户编排默认安装根：`/opt/llm-gateway`，可由 `INSTALL_ROOT` 覆盖。
- `COMPOSE_DIR` 只控制底层 `install-docker.sh` 的输出目录。
- 推荐宿主映射：`8080 -> 8781`；local host 直接访问 `8781`，二者不要混淆。
- `.env` 必须为 `0600`，其中只存环境变量引用对应的 secret 值，不写入仓库。

## 安装后验证

```bash
docker compose -f /opt/llm-gateway/docker-compose.yml ps
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

升级前备份 `.env` 和 compose 文件，校验镜像 checksum，启动后依次检查 liveness、readiness、release identity。失败时恢复最近 release snapshot 并重新执行 preflight。不要在未确认 schema 向前兼容性时只回滚 binary。

## 故障排查

```bash
docker compose -f /opt/llm-gateway/docker-compose.yml logs --tail=100 gateway
docker compose -f /opt/llm-gateway/docker-compose.yml ps
# 按实际 compose 服务名检查 DB/Redis
docker compose -f /opt/llm-gateway/docker-compose.yml exec <db-service> pg_isready
docker compose -f /opt/llm-gateway/docker-compose.yml exec <redis-service> redis-cli ping
```

当前 Docker 客户链路仍需 clean-machine、真实 DB/Redis 和升级回滚证据，不能标记 `RELEASE_READY`。

# Linux Host 安装（systemd）

> 状态：PARTIAL；完成 clean-machine 和真实依赖验收前，不标记 `RELEASE_READY`。

## 入口

```bash
# 推荐：客户编排入口，支持 --install-mode
bash scripts/user/client-deploy.sh --install-mode host deploy

# 底层 host 安装脚本（不接受 --install-mode）
sudo bash scripts/user/install-host.sh
```

生产/离线场景应使用 Maintain 分发的、已校验 checksum 的 release 脚本，不要直接把源码仓库当作发布包。

## 前置条件

- Linux amd64/arm64；loong64 需显式 `LOONG64_OK=1` 且有对应 artifact。
- systemd、root/sudo、可写安装根；真实 PostgreSQL/Redis 地址和 secret 由环境注入。
- 必须确认 release 内包含 Gateway binary、`version.json`、web assets、`config/gateway.env.example` 和 checksum。

## 默认路径和端口

- 安装/版本根：`/opt/llm-gateway`；不可写时使用 `~/.local/llm-gateway`。
- 服务配置：`/etc/llm-gateway/gateway.env`，权限 `0600`。
- 服务单元：`/etc/systemd/system/llm-gateway-go.service`。
- 服务默认监听：`8781`；如 release/config 覆盖，必须在部署记录中写明。
- state/log 目录以 systemd unit 和 release 为准，不要假定与下载缓存目录相同。

## 安装后验证

```bash
sudo systemctl status llm-gateway-go --no-pager
curl -fsS http://127.0.0.1:8781/healthz
curl -fsS http://127.0.0.1:8781/readyz
curl -fsS http://127.0.0.1:8781/version
bash scripts/lifecycle/preflight.sh \
  --base-url http://127.0.0.1:8781 \
  --expected-version <bundle-version>
```

`/healthz` 是 liveness；`/readyz` 只有 DB 和 Redis 都可用才返回 2xx；`/version` 用于 release identity 对齐。

## 升级与回滚

```bash
bash scripts/user/upgrade.sh run
bash scripts/user/upgrade.sh list
bash scripts/user/upgrade.sh rollback
```

升级前必须保留配置和当前 verified release。失败时先停止候选服务、恢复旧 `current`/release，再启动旧版本并重新执行三段 preflight。二进制回滚不能替代不兼容 migration 的数据回滚。

## 故障排查

```bash
sudo journalctl -u llm-gateway-go -n 100 --no-pager
sudo systemctl cat llm-gateway-go
sudo ss -ltnp | grep 8781
```

真实 DB/Redis、生产凭据、迁移和重启操作必须在授权窗口执行并留下证据。

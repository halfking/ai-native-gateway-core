# macOS Host 安装（launchd）

> 状态：PARTIAL；需在干净 macOS 主机完成安装、启动、升级和回滚验收。

## 入口

```bash
# 推荐：客户编排入口
bash scripts/user/client-deploy.sh --install-mode host deploy

# 底层 host 脚本（不接受 --install-mode）
bash scripts/user/install-host.sh
```

生产或离线安装应使用 Maintain 分发的已校验 release，不要直接执行未验证的源码脚本。

## 前置条件

- macOS amd64 或 arm64；Docker Desktop 场景见 [macOS Docker](macos-docker.md)。
- 可写的 `INSTALL_ROOT`、launchd 权限以及客户自有 PostgreSQL/Redis。
- release 必须包含 Gateway binary、`version.json`、web assets、`config/gateway.env.example` 和 checksum。

## 目录、服务和端口

- 下载/解压根由 `INSTALL_ROOT` 决定，默认倾向 `~/Downloads/llm-gateway-files`，不可写时使用 `~/.local/llm-gateway`。
- launchd 服务配置可能位于 `/usr/local/etc/llm-gateway/gateway.env`；服务运行根可能是 `/usr/local/llm-gateway`。这与下载缓存目录不是同一概念，必须以 release 生成的 plist 为准。
- 服务默认监听 `8781`；如果客户配置覆盖，必须同步修改 preflight 的 base URL。
- plist 模板：`scripts/deploy/launchd/com.kaixuan.llm-gateway-go.plist`。

## 安装后验证

```bash
sudo launchctl list | grep llm-gateway
curl -fsS http://127.0.0.1:8781/healthz
curl -fsS http://127.0.0.1:8781/readyz
curl -fsS http://127.0.0.1:8781/version
bash scripts/lifecycle/preflight.sh \
  --base-url http://127.0.0.1:8781 \
  --expected-version <bundle-version>
```

`readyz` 不是单纯进程检查；没有可用 DB 或 Redis 时应为 503，不能把失败记录为安装成功。

## 升级与回滚

```bash
bash scripts/user/upgrade.sh run
bash scripts/user/upgrade.sh list
bash scripts/user/upgrade.sh rollback
```

升级前保留配置、当前 verified release 和 checksum。失败时停止候选服务、恢复旧 release/链接、重新 bootstrap launchd，再执行三段 preflight。需要真实数据回滚时必须使用单独 migration runbook。

## 故障排查

```bash
sudo launchctl print system/com.kaixuan.llm-gateway-go
log show --last 10m --predicate 'process == "gateway"'
plutil -lint /Library/LaunchDaemons/com.kaixuan.llm-gateway-go.plist
lsof -nP -iTCP:8781 -sTCP:LISTEN
```

macOS clean-machine 验收尚未完成前，状态保持 `PARTIAL`。

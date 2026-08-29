# Linux Host 安装（systemd）

## 快速上手

```bash
# 1. 一行式（生产）
curl -fsSL https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/llm-gateway-host | bash

# 2. 本地脚本（开发）
cd llm-gateway-go/scripts/user
sudo bash install-host.sh
```

需要 root（systemd unit 创建 + useradd/groupadd）。

## 默认路径

- `INSTALL_ROOT=/opt/llm-gateway`（与 maintain 服务一致；`/opt` 不可写时回退 `~/.local/llm-gateway`）
- `/etc/llm-gateway/gateway.env`（mode 0600，由 `service-runner.sh` 注入到 gateway 进程环境）
- `/var/lib/llm-gateway`（StateDirectory）/ `/var/log/llm-gateway`（LogsDirectory）
- systemd unit: `/etc/systemd/system/llm-gateway-go.service`

## 服务注册

`install-host.sh` 调包内 `install.sh` → `scripts/lifecycle/install-service.sh`：

1. `groupadd --system llm-gateway` + `useradd --system --home-dir /var/lib/llm-gateway --shell /usr/sbin/nologin llm-gateway`
2. 写 `/etc/llm-gateway/gateway.env`（首次拷 `gateway.env.example`，提醒填 secrets）
3. 写 `/etc/systemd/system/llm-gateway-go.service`（来自 `scripts/deploy/systemd/llm-gateway-go.service` 模板，sed 替换占位符）
4. `systemctl daemon-reload && systemctl enable --now llm-gateway-go`

## 升级

```bash
bash scripts/user/upgrade.sh run            # 自动检测 host 模式
INSTALL_MODE=host bash scripts/user/upgrade.sh run
```

Host 升级流程：
1. `systemctl stop llm-gateway-go`
2. `rsync -a --delete --exclude '.upgrade-*' staging/ → INSTALL_ROOT/`
3. `systemctl start llm-gateway-go`
4. preflight.sh 三段

失败自动从 `.upgrade-backup-*` 恢复。

## 回退（人工）

```bash
# 看历史
ls /opt/llm-gateway/.upgrade-backup-*/
ls /opt/llm-gateway/releases/

# 切到旧 bundle（沿用 install-host.sh 的 releases/ 布局）
ln -sfn /opt/llm-gateway/releases/2.4.7.1794 /opt/llm-gateway/current
sudo systemctl restart llm-gateway-go
```

## 故障排查

```bash
# 服务状态
sudo systemctl status llm-gateway-go
sudo journalctl -u llm-gateway-go -n 100 --no-pager

# 健康检查
curl http://127.0.0.1:8080/healthz | jq
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:8080/version | jq

# 服务降级排查
sudo systemctl edit llm-gateway-go    # 加 Environment="LLM_GATEWAY_LOG_LEVEL=debug"
sudo systemctl restart llm-gateway-go
```

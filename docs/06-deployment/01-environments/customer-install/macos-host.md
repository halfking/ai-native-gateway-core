# macOS Host 安装（launchd）

## 快速上手

```bash
# 1. 一行式（生产）
curl -fsSL https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/llm-gateway-host | bash

# 2. 本地脚本（开发）
cd llm-gateway-go/scripts/user
bash install-host.sh
```

默认 `INSTALL_ROOT=~/Downloads/llm-gateway-files`（与开发机 local-host-deploy 一致；`~/Downloads` 不可写时回退 `~/.local/llm-gateway`）。

## 路径布局

```
~/Downloads/llm-gateway-files/
├── bin/
│   ├── <version>/              # bundle: gateway binary + web + version.json
│   ├── current → <version>     # atomic symlink
│   ├── start.sh stop.sh        # bundle 内置 wrapper（scripts 也用）
│   └── env.sh
├── logs/  run/  backups/
└── (你的 db / data 也在此)
```

## 服务注册

`install-host.sh` 调包内 `install.sh` → 调 `scripts/lifecycle/install-service.sh`：

1. 创建 `llm-gateway` user / group（macOS 上 user/group 概念较轻，launchd 默认以 root 跑）
2. 写 `/etc/llm-gateway/gateway.env`（mode 0600）—— 首次会拷 `gateway.env.example` 让用户填 secrets
3. 写 `/Library/LaunchDaemons/com.kaixuan.llm-gateway-go.plist`（来自 `scripts/deploy/launchd/com.kaixuan.llm-gateway-go.plist` 模板，sed 替换占位符）
4. `launchctl bootstrap system <plist>`

## 升级

```bash
# 自动检测 current + 切到 latest stable
bash scripts/user/upgrade.sh run

# 仅下载不切
bash scripts/user/upgrade.sh download

# 查看可用版本
bash scripts/user/upgrade.sh list

# 回退（从 .upgrade-backup-* 最近备份）
bash scripts/user/upgrade.sh rollback
```

升级三段健康检查（自动）：
1. `service_stop` → `launchctl bootout system/com.kaixuan.llm-gateway-go`
2. `rsync -a --delete --exclude '.upgrade-*' staging/ → INSTALL_ROOT/`
3. `service_start` → `launchctl bootstrap system <plist>`
4. `preflight.sh` 三段（healthz + readyz + version 匹配）

失败自动回退（`upgrade.sh:818` restore_from_backup）。

## 回退（人工）

```bash
# 看 releases/
ls -la ~/Downloads/llm-gateway-files/bin/

# 切到旧 bundle
ln -sfn ~/Downloads/llm-gateway-files/bin/2.4.7.1794 ~/Downloads/llm-gateway-files/bin/current
~/Downloads/llm-gateway-files/bin/current/start.sh
```

## 故障排查

```bash
# 服务状态
sudo launchctl list | grep kaixuan

# 实时日志
tail -f ~/Downloads/llm-gateway-files/logs/gateway.stdout.log

# plist 路径错误？
plutil -lint /Library/LaunchDaemons/com.kaixuan.llm-gateway-go.plist

# 健康检查
curl http://127.0.0.1:8781/healthz | jq
curl http://127.0.0.1:8781/readyz   # 期望 200
curl http://127.0.0.1:8781/version | jq
```

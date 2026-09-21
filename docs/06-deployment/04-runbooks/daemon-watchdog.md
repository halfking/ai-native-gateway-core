# 守护进程与自动重启（2026-09-02）

> 适用版本：2.4.7+

## 一、设计原则

**复用现有 OS 守护，**不另建独立 supervisor 进程**。** Docker / launchd / systemd 都已自带 `restart: unless-stopped` / `KeepAlive` / `Restart=always`，足以保证「非人工停止」时服务持续运行。

## 二、按部署方式的守护矩阵

| 部署方式 | 守护进程 | 自重启机制 | 关键参数 |
|---|---|---|---|
| **Docker Compose（推荐，本地新部署）** | Docker Engine | `restart: unless-stopped` | `max-file: 3`, `max-size: 10m`（日志） |
| **macOS launchd（兼容老路径 / 原生部署）** | launchd | `KeepAlive=true` + `ThrottleInterval=10`（**2026-09-02 新增**） | `SoftResourceLimits/NumberOfFiles=65536` |
| **Linux systemd (154/245 canary)** | systemd | `Restart=on-failure` / `RestartSec=5s` | 蓝绿无缝切流 |
| **Linux systemd (154 非 canary)** | systemd | `Restart=always` / `RestartSec=5` | 旧 unit，保留兼容 |

## 三、关键文件位置

| 文件 | 用途 |
|---|---|
| `scripts/deploy/launchd/com.kaixuan.llm-gateway-go.plist` | macOS launchd 模板（ThrottleInterval=10、PATH 含 /opt/homebrew/bin、LLM_GATEWAY_HOME 注入） |
| `scripts/lifecycle/install-service.sh` | 把 plist 写入 `/Library/LaunchDaemons/`；darwin 分支 prefix 走 `detect_install_root` |
| `deploy/llm-gateway-go.service` / `deploy/llmgo-245.service` | 154/245 systemd unit（路径 `/opt/kaixuan/llm-gateway`） |
| `deploy/llm-gateway-go-canary@.service` / `deploy/llmgo-245-canary@.service` | 154/245 canary unit，蓝绿切换用 |
| `deploy/logrotate-llm-gateway-go` | `/var/log/kaixuan/llm-gateway/gateway.{stdout,stderr}.log` 100M rotate |
| `scripts/local-docker-up.sh` | 本机一键 Docker 部署（自带 restart: unless-stopped） |

## 四、运维命令速查

### macOS launchd
```bash
# 状态
launchctl list | grep kaixuan.llm-gateway-go

# 重启
launchctl kickstart -k gui/$(id -u)/com.kaixuan.llm-gateway-go

# 查日志
tail -F $LLM_GATEWAY_HOME/logs/gateway.log
tail -F $LLM_GATEWAY_HOME/logs/gateway.error.log
```

### Linux systemd
```bash
# 状态
systemctl status llm-gateway-go-canary@8781
systemctl status llm-gateway-go-canary@8782

# 重启（蓝绿切换由 deploy-154 / deploy-245 seamless 流程管理，**不要**手动 restart）
systemctl restart llm-gateway-go-canary@<port>

# 查日志
journalctl -u llm-gateway-go-canary@8781 -f
```

### Docker
```bash
docker compose -f $LLM_GATEWAY_HOME/compose.yml ps
docker compose -f $LLM_GATEWAY_HOME/compose.yml restart
docker compose -f $LLM_GATEWAY_HOME/compose.yml logs -f gateway
```

## 五、故障排查

### 1. 启动后立即 crash loop

**症状**：launchd / systemd 不断重启 gateway，healthz 一直 503。

**排查**：
```bash
# 看最近一次启动日志
journalctl -u llm-gateway-go-canary@<port> -n 200 --no-pager
# 或
tail -200 $LLM_GATEWAY_HOME/logs/gateway.error.log
```

**常见原因**：
- DB 连接失败（`closed pool` 错）— 看 `internal/postgres` 配置 / PG 容器状态
- 端口被占 — `lsof -Pi :8781 -sTCP:LISTEN`
- ENV 没注入 — 看 `run/install-info.json` 是否被 gateway 写入

### 2. 系统资源耗尽 / OOM

**症状**：进程被 SIGKILL，`dmesg` 看到 OOM kill。

**排查**：
```bash
dmesg | grep -i 'oom\|killed' | tail
# macOS: 
log show --predicate 'eventMessage CONTAINS "Jetsam" AND eventMessage CONTAINS "llm-gateway"' --info --last 1h
```

**缓解**：
- 调整 systemd `MemoryHigh/MemoryMax` drop-in（参考 `deploy/phase0/` 阶段示例）
- macOS 启用 `GOMEMLIMIT` 环境变量限制 heap
- 检查是否有未关闭的 SSE 连接积累（`outbound_body_bytes` 监控）

### 3. ThrottleInterval 触发后服务仍挂

**症状**：macOS launchd 5+ 分钟没拉起。

**排查**：
```bash
launchctl print gui/$(id -u)/com.kaixuan.llm-gateway-go | head -50
# 看 last exit code + throttle interval 是否被 reset
```

**恢复**：
```bash
launchctl kickstart -k gui/$(id -u)/com.kaixuan.llm-gateway-go
```

## 六、不要做的事

- ❌ 不要在应用层写 watchdog goroutine 自杀重启 — OS 守护已足够
- ❌ 不要手动 `kill -9` 然后期待自动恢复 — launchd ThrottleInterval 会限制重启频率
- ❌ 不要修改 `bin/current` 软链绕过 deploy 流程 — 会导致 metadata 与实际不一致
- ❌ 不要直接编辑 `/var/log/kaixuan/llm-gateway/gateway.{stdout,stderr}.log` — logrotate 的 `copytruncate` 会处理 rotate
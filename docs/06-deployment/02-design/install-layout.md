# Install Layout 设计规范（2026-09-02）

> 适用版本：2.4.7+
> 适用服务：llm-gateway-go 全栈（gateway + postgres + redis + nginx + 守护）

## 一、设计原则

1. **复用 > 重构** — 不另起炉灶；能用 Docker 走 Docker，能复用现有 launchd/systemd 模板就用。
2. **新建按 OS 规范** — 新安装按 OS 默认路径；老实例保留。
3. **不破坏现状** — 现存的 `~/Downloads/llm-gateway-files` 等历史路径**不被强制迁移**；脚本探测顺序保证它们继续被识别。
4. **守护只补缺口** — Docker 自带 restart: unless-stopped；launchd 加 ThrottleInterval；systemd 沿用 canary。**不再额外建独立 supervisor**。

## 二、安装根目录矩阵

| OS | 默认路径 | 用户级回退 | Env 覆盖 |
|---|---|---|---|
| **macOS** | `~/kaixuan/llm-gateway` | `~/.local/llm-gateway`（不可写时） | `INSTALL_ROOT` / `LLM_GATEWAY_HOME` |
| **Linux** | `/opt/kaixuan/llm-gateway` | `~/.local/llm-gateway`（/opt/kaixuan 不可写时） | `INSTALL_ROOT` / `LLM_GATEWAY_HOME` |
| **Windows** | `D:/kaixuan/llm-gateway` | `C:/kaixuan/llm-gateway`（D 不可写时） | `INSTALL_ROOT` / `LLM_GATEWAY_HOME` |

## 三、INSTALL_ROOT 探测优先级（统一）

```bash
detect_install_root() {
  # 1) 显式 env 最高优先
  if [[ -n "${LLM_GATEWAY_HOME:-}" ]]; then echo "$LLM_GATEWAY_HOME"; return; fi
  if [[ -n "${INSTALL_ROOT:-}" ]]; then echo "$INSTALL_ROOT"; return; fi
  if [[ -n "${LLM_GATEWAY_FILES_ROOT:-}" ]]; then echo "$LLM_GATEWAY_FILES_ROOT"; return; fi

  # 2) 现存实例识别（保护历史部署）
  for cand in \
    "$HOME/Downloads/llm-gateway-files" \
    "$HOME/kaixuan/llm-gateway" \
    "$HOME/.local/llm-gateway"
  do
    [[ -d "$cand/bin" || -f "$cand/VERSION" || -L "$cand/bin/current" ]] \
      && { echo "$cand"; return; }
  done

  # 3) OS 默认
  case "$(uname -s)" in
    Darwin*)   echo "$HOME/kaixuan/llm-gateway" ;;
    Linux*)    [[ -w /opt/kaixuan ]] && echo "/opt/kaixuan/llm-gateway" || echo "$HOME/.local/llm-gateway" ;;
    MINGW*|MSYS*|CYGWIN*)
      [[ -d /d && -w /d ]] && echo "/d/kaixuan/llm-gateway" || echo "/c/kaixuan/llm-gateway" ;;
  esac
}
```

实现：见 `scripts/local-host-layout-helper.sh:lh_root`、`scripts/user/lib/common.sh:detect_install_root`、`scripts/user/install-host.sh:detect_install_root`。

## 四、目录布局

```
{INSTALL_ROOT}/
├── bin/                             版本化可执行（蓝绿切换）
│   ├── {version}-{build}/           e.g. 2.4.7-1882
│   │   ├── gateway                 主二进制（统一改名为 gateway）
│   │   ├── env.sh                  0600
│   │   ├── start.sh / stop.sh / status.sh   0755
│   │   ├── version.json / VERSION
│   │   └── web/ (可选, 实际由 bin/{ver}/web/ 软链到 ../web)
│   ├── current → bin/{version}-{build}/   原子软链
│   └── releases/                   历史归档（软链）
├── web/                            共享前端 dist（不放入 bin/{ver}，节省空间）
├── sql/                            共享 SQL migrations（deploy 时核对 version）
├── postgres/                       本机原生 PG 数据
│   └── data/
├── redis/                          本机原生 Redis 数据
│   └── data/
├── attachments/                    附件存储
├── logs/
│   ├── gateway.stdout.log
│   ├── gateway.stderr.log
│   └── archive/                    应用层 lumberjack rotate 后的归档
├── raw-logs/                       raw_data_logger 输出
├── backups/
├── run/                            运行时状态
│   ├── gateway.pid / gateway.port
│   ├── active-upstream.conf
│   ├── install-info.json           启动时由 cmd/gateway 写入
│   └── crash.log                  （预留，OS 守护接管后通常不需要）
└── compose.yml                    Docker 部署主入口（脚本生成）
```

**老目录 `~/Downloads/llm-gateway-files`** 维持原 8 个子目录不变；新脚本通过探测顺序把它识别为「现存实例」复用之。

## 五、bin/{version}.{build} 版本目录

- 目录名格式：`{git_tag}-{build_seq}`（例：`2.4.7-1882`）
- `bin/current` 原子切换 → 旧版本软链归档到 `bin/releases/`
- 蓝绿部署沿用 `local-host-layout-helper.sh` 的端口机制：`18781`(blue) / `18782`(green)，nginx upstream `: `: 8781`
- nginx 通过 `include run/active-upstream.conf` 实现毫秒级切流

## 六、守护进程（按部署方式）

| 部署方式 | 守护 | 自重启参数 | 备注 |
|---|---|---|---|
| Docker（推荐，本地新部署） | Docker Engine + Compose | `restart: unless-stopped` | 不另建进程；非人工 stop 不会停 |
| 本机原生（macOS） | launchd plist | `KeepAlive=true` + `ThrottleInterval=10`（**2026-09-02 新增**） | 见 `scripts/deploy/launchd/com.kaixuan.llm-gateway-go.plist` |
| Linux systemd（154/245） | canary unit | `Restart=on-failure` / `RestartSec=5s` | 蓝绿无缝切流复用现有 canary |
| Linux systemd（非 canary，154 旧） | service unit | `Restart=always` / `RestartSec=5` | 保留以兼容过渡期 |

**不**新增应用层 watchdog / supervisor 进程 — OS 守护已足够。

## 七、本机 Docker 一键部署（新增）

```bash
bash scripts/local-docker-up.sh
# 或显式指定 INSTALL_ROOT:
INSTALL_ROOT=~/kaixuan/llm-gateway bash scripts/local-docker-up.sh

bash scripts/local-docker-down.sh           # 仅停容器
bash scripts/local-docker-down.sh --purge   # 停 +删数据卷
```

- 复用 `installer/templates/compose.yml`（含 PG/Redis/Gateway 全栈）
- bind-mount: `${INSTALL_ROOT}/{attachments,backups,logs,raw-logs}`
- logs 走 Docker json-file driver（`max-size: 10m, max-file: 3`）— 已有
- 守护由 Docker Engine 自带 — 无需额外配置

## 八、154/245 中心机迁移（蓝绿无缝）

```bash
HOST_154_ROOT=/opt/kaixuan/llm-gateway bash scripts/deploy-154.sh
HOST_245_ROOT=/opt/kaixuan/llm-gateway bash scripts/deploy-245.sh
```

seamless 流程自动：
1. 远端 `mkdir -p /opt/kaixuan/llm-gateway/{bin,logs,run,...}`
2. 上传新版本到 `releases/{ver}/`
3. 软链 `current → releases/{ver}`
4. canary 单元热重启 `llmgo-245-canary@8782`
6. nginx upstream 切到 8782
7. drain 旧 active

**老 `/opt/llm-gateway-go` 保留 30 天用于回滚。**

## 九、回滚

| 场景 | 步骤 |
|---|---|
| 154/245 蓝绿回滚 | `bash scripts/deploy-154.sh rollback <old_ver>`（seamless 支持） |
| 154/245 路径回退 | `ln -sfn /opt/llm-gateway-go /opt/kaixuan/llm-gateway` 软链兼容；重启 canary |
| macOS 本地 Docker | `bash scripts/local-docker-down.sh --purge && bash scripts/local-docker-up.sh` |
| macOS launchd | `launchctl bootout gui/$(id -u)/com.kaixuan.llm-gateway-go` + 装回老 plist |

## 十、参考

- [daemon-watchdog.md](./../04-runbooks/daemon-watchdog.md) — 守护与运维手册
- [154 生产部署 SOP](../../deployment/154-production-deployment-sop.md) — 中心机部署
- [BLUEGREEN_QUICKSTART.md](../../deployment/bluegreen-quickstart.md) — 本地蓝绿
- [LOCAL_DEPLOY_OPTIMIZATION.md](../../deployment/local-deploy-optimization.md) — 本地蓝绿优化
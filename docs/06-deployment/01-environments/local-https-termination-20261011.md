# 本地 HTTPS 终结方案（Caddy + 自签名，2026-10-11）

> 适用：部署完 `deploy-local.sh` 之后，**对外提供 HTTPS 终结** 时使用。
> 场景：远端 peer 通过 NetBird mesh（`http://llm.itestu.cn:8782`）能直连网关，
> 但浏览器走 `https://llm.itestu.cn` 时需要 HTTPS 终结；公网 CA 暂未申请，
> 故走 **Caddy 自动颁发自签名**。
> 关联：[本地 host-first 部署](./local-host-first-deploy-20261011.md) ·
> [域名指向（§5）](./local-host-first-deploy-20261011.md#5-netbird-域名接入llmitestucn)

## 1. 目标与边界

- 在本机起 Caddy，监听 `:443`，对 `llm.itestu.cn` 自动签发自签 CA 证书，
  反代到 `127.0.0.1:8782`（host-first 网关活跃端口）
- 浏览器首次访问需手动信任本地 CA（或一次性 `trust-local-ca.sh`，本脚本
  不做 —— 属用户侧决策，避免越权写系统 keychain）
- 自动安装 Caddy 二进制（若缺失）、生成 Caddyfile + 本地 PKI、自动 openrc
  / launchd 保活
- **不**做：公网 CA 申请（acme.sh / Let's Encrypt）、HTTP→HTTPS 全局跳转
  （mesh 入站仍走明文 HTTP，避免与 LAN 代理栈冲突）

## 2. 决策点

| 项 | 决策 | 理由 |
|---|---|---|
| 终结器 | Caddy | Caddyfile 极简（3 行有效负载），内置自动 HTTPS + 自签 CA |
| 证书 | 本地自签 CA（`pki/authorities/local`）| 公网 CA 暂未申请；自签可零依赖落地 |
| 反代上游 | `127.0.0.1:8782` | host-first 网关本轮切到 8782；走 loopback 不经 NetBird |
| 监听 | `:443`（全接口） | 浏览器走 mesh IP / LAN IP / 域名均需可达 |
| 保活 | macOS launchd / Linux systemd | 与 deploy-local 同形态 |
| 信任本地 CA | 留给用户 | 写系统 keychain 是越权动作；本脚本只生成 + 提示 |

## 3. 拓扑

```
浏览器 ─HTTPS──▶ :443 (Caddy) ────HTTP──▶ 127.0.0.1:8782 (llm-gateway)
                       │
                       └─ 自签 CA（pki/authorities/local）

mesh peer ─HTTP─▶ 100.106.169.63:8782 ─HTTP─▶ 127.0.0.1:8782
                （绕开 Caddy，明文直连，走 NetBird 默认策略放行）
```

> **mesh 与 HTTPS 不冲突**：mesh peer 走明文 HTTP（`http://llm.itestu.cn:8782`），
> 浏览器走 `https://llm.itestu.cn`。两套同时生效。

## 4. 一键脚本

`scripts/setup-caddy.sh`（仓内 SSOT，与 `deploy-local.sh` 同模块化风格）：

```bash
# dry-run：只打印将创建/修改的路径与命令，不实际写
bash scripts/setup-caddy.sh

# 实际安装
bash scripts/setup-caddy.sh apply

# 状态
bash scripts/setup-caddy.sh status

# 卸载（停保活 + 删二进制与配置，PKI 备份到 backups/）
bash scripts/setup-caddy.sh remove
```

dry-run 输出约束（已落 `tests/setup_caddy_contract_test.sh`，verify.sh 已接）：

- 仅打印 `+ <命令>` 一行一行，没有任何写入动作
- 二进制路径 / Caddyfile 路径 / PKI 路径必须落在
  `~/kaixuan/caddy/{bin,etc,var,pki,backups}` 下
- launchd plist（macOS）必须落在 `~/Library/LaunchAgents/com.kaixuan.caddy.plist`

## 5. Caddyfile（生效形态）

Caddyfile 由脚本写入 `~/kaixuan/caddy/etc/Caddyfile`：

```
# llm-gateway 本地 HTTPS 终结（自签 CA，仅本机信任）
{
    admin localhost:2019
    log {
        output file ~/kaixuan/caddy/var/caddy.log {
            roll_size 50MiB
            roll_keep 5
        }
    }
}

llm.itestu.cn:443 {
    tls internal                       # 用本地 CA（pki/authorities/local）签发
    encode zstd gzip
    reverse_proxy 127.0.0.1:8782 {
        header_up Host {host}
        header_up X-Real-IP {remote}
        header_up X-Forwarded-For {remote}
        header_up X-Forwarded-Proto https
    }
}
```

`tls internal` 是 Caddy 对本地自签 PKI 的官方开关；首次启动会触发
`caddy trust` 等价的动作，把 CA 装到 Caddy 自己的 trust store
（**不影响系统 keychain**）。

## 6. 验证矩阵

| 层 | 命令 | 预期 |
|---|---|---|
| 端口监听 | `lsof -nP -iTCP:443 -sTCP:LISTEN` | Caddy 进程在听 |
| 协议握手 | `openssl s_client -connect 127.0.0.1:443 -servername llm.itestu.cn </dev/null 2>&1 \| openssl x509 -noout -subject -issuer` | subject `CN=llm.itestu.cn`，issuer `O=Acme Co Ltd`（Caddy 本地 CA）|
| HTTPS 直连 | `curl -kfsS https://llm.itestu.cn/healthz` | `{"status":"ready",...}` |
| 信任本地 CA 后直连 | `curl -fsS https://llm.itestu.cn/healthz` | 同上（无 `-k`）|
| 上游透传 | `curl -kfsS https://llm.itestu.cn/readyz` | `database=connected, redis=connected` |
| 浏览器 | `https://llm.itestu.cn` | 首次提示"证书不受信任"，按用户决策信任本地 CA |
| 日志 | `tail -F ~/kaixuan/caddy/var/caddy.log` | 应能看到 200/401 |

## 7. 运维速查

```bash
# 启停（macOS launchd）
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/com.kaixuan.caddy.plist 2>/dev/null
launchctl kickstart -k gui/$UID/com.kaixuan.caddy                          # 重启
launchctl bootout gui/$UID/com.kaixuan.caddy                                # 停

# 启停（Linux systemd）
systemctl --user daemon-reload
systemctl --user enable --now caddy-llmgw

# 改完 Caddyfile 重载
caddy reload --config ~/kaixuan/caddy/etc/Caddyfile --adapter caddyfile

# 查看本地 CA（系统安装的是 macOS keychain；Caddy 内部 trust 在 var/caddy/pki）
ls -la ~/kaixuan/caddy/pki/authorities/local/                               # 证书与私钥

# 吊销本地 CA（极端场景：私钥泄露）
rm -rf ~/kaixuan/caddy/pki/authorities/local
caddy trust --config ~/kaixuan/caddy/etc/Caddyfile                         # 重生
```

## 8. 已知坑（落地经验）

- **`launchd` 与 `nohup` 不能并存**：`deploy-local.sh` 已停用 launchd 代理，
  Caddy 的 launchd plist 用 `RunAtLoad=false + KeepAlive`，避免与手动调试互踩。
- **`tls internal` 不写系统 trust**：Caddy 仅把 CA 装到自身 `var/caddy/pki`。
  浏览器仍需用户手动信任 `~/kaixuan/caddy/pki/authorities/local/root.crt`。
- **443 端口冲突**：macOS 控制面板的"隔空播放接收器"会监听 7000 / 5000 系
  端口，不抢 443；APF（应用防火墙）首次启动 Caddy 会弹权限框（允许入站）。
- **Caddy 二进制下载**：`github.com/caddyserver/caddy` 官方 release 走 GitHub，
  国内下载偶发慢；脚本默认走 `https://github.com/caddyserver/caddy/releases/download/vX.Y.Z/caddy_X.Y.Z_mac_arm64.tar.gz`，
  如失败会自动 fallback 到 `https://caddyserver.com/api/download?os=darwin&arch=arm64`。
- **LAKE 双 Caddy 实例**：若用户已装 Caddy（brew / 官方包），本脚本的
  launchd plist 名 `com.kaixuan.caddy` 不与 `com.caddyserver.caddy` 冲突，
  但会抢 443。dry-run 会探测并 warn（不阻断）。
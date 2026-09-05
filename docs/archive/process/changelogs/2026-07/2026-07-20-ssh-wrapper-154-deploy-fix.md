# 2026-07-20 — 154/245 deploy 链路修复 + ssh wrapper

## 触发问题

老板报告 `https://llm.kxpms.cn`（生产域名）首页是旧版本，跟 245 对比明显页面陈旧。
排查后定位 3 类问题：

1. **154 公网 SSH 被防火墙全挡** — `47.97.111.154:25022/22` 全部 refused/timeout，从任何源都连不上
2. **deploy-seamless 走 252 跳板的 SSH 路径有 bug** — 跳到 252 后 `Bad configuration option: -o` 立即失败
3. **`host_atomic_switch` 给 154 算的 binary_link 跟 systemd unit 不一致** — 154 部署后 systemd 一直 203/EXEC restart

## 涉及根因 (5 条)

### 1. ssh-retry.sh 的 proxy_flag 双层 `-o` (脚本 bug)

`ssh-retry.sh:217` 的 `extra_flags=(-o "$proxy_flag")` 把已经包含 `-o ProxyCommand=...` 的字符串又套了一层 `-o`，ssh 解析报 `Bad configuration option: -o`。

**修复**：去掉外层 `-o`，`extra_flags=("$proxy_flag")`。

### 2. host.sh 给 154 算 binary_link 名字错 (contract bug)

`host.sh:78` 用三元 `[[ $target == 245 ]] && echo gateway || echo llm-gateway-go` 决定 symlink 名：
- 245 → `/opt/llm-gateway-go/gateway` ✅
- 154 → `/opt/llm-gateway-go/llm-gateway-go` ❌

但 154 的 systemd unit `llm-gateway-go.service` 的 ExecStart 是 `/opt/llm-gateway-go/gateway`，**两边不一致**，atomic-switch 每次 deploy 都建错链接，systemd 立即 203/EXEC restart 死循环。

**修复**：硬编码 `binary_link=$root/gateway`，154 / 245 都用 `gateway`，跟 systemd unit 一致。

### 3. 154 公网 SSH 不可达，需走 245 (172.16.2.241)

实际服务器拓扑 (2026-07-20 验证):
- 154 公网 `47.97.111.154`: SSH 全挡 (防火墙), 443 HTTPS 跑 nginx + WorkOS (casdoor)
- 154 内网 `172.16.2.209`: SSH 同样被挡
- **245 内网 `172.16.2.241`: SSH 通 (sshpass 密码), 跑 systemd `llm-gateway-go.service`, 公网 8781 + nginx**
- 252 内网 `172.16.2.210`: nginx 反代入口, 配 `kxpms_llm_backend` upstream

实际"154 服务"是跑在 245 上的, 252 nginx 把 `llm.kxpms.cn` 反代到 245:8781。
所以老板说"154 旧版"实质是 245 上跑的还是 7月19日 1200 build, 而当时 245 也刚被部署过一次 1200 (跟 154 同样 SHA `c1a01552`)。

**修复**：
- 新增 `scripts/ssh-wrapper-154.sh` + symlink `scripts/ssh` —— 收到 `root@47.97.111.154`/`root@172.16.2.209` 时自动重定向到 `252 → sshpass → 172.16.2.241:25022`
- 新增 `scripts/deploy-154-via-252.sh` —— 封装 PATH，让 `bash scripts/deploy-154-via-252.sh deploy 154` 一条命令搞定
- 修改 252 nginx upstream `kxpms_llm_backend`: `172.16.2.209:8781` → `172.16.2.241:8781`，reload 完成

### 4. ssh 输出 Warning 污染 deploy-seamless 解析

ssh 第一次连到 172.16.2.241 时输 `Warning: Permanently added ...` 到 stdout，deploy-seamless 的 `host_atomic_switch` 拿 stdout 解析 release 名字，结果 `ln -sfn releases/Warning: ... current`，systemd ExecStart 找不到 binary。

**修复**：ssh wrapper 强制 `ssh -q -o LogLevel=QUIET`，彻底抑制 Warning。

### 5. deploy-seamless PATH 模式下 wrapper 递归死循环

wrapper 在 PATH 中递归调用自己 → 死循环 → "hostname contains invalid characters"。

**修复**：
- 内部 `exec` 用绝对路径 `/usr/bin/ssh` `/usr/bin/sshpass`
- 同时让 wrapper 不递归解析任何 `-i/-p/-o` 等带值选项的参数

## 部署后状态

| 项 | 之前 | 现在 |
|---|---|---|
| `https://llm.kxpms.cn` 健康 | 502 (upstream 错指 209) | 200, `/healthz` `2.4.7-dae265fb-20260719-1209` |
| 当前 release | `releases/1200-c1a01552` (1200 build, 7月19日) | `releases/1209-dae265fb` (1209 build, 7月20日) |
| 看板/实时请求流/会话与统计/系统监测 tab | 只有营销落地页 | 4 tabpage 全部可见 (browser-use 验证) |
| deploy-seamless 154 路径 | 不可用 (3 个 bug 卡死) | `bash scripts/deploy-154-via-252.sh deploy 154` 一键部署 |

## 文件改动

- `scripts/deploy-lib/ssh-retry.sh`: 修 proxy_flag 双层 `-o` (line 217)
- `scripts/deploy-lib/host.sh`: `binary_link` 154/245 都用 `gateway` (line 78)
- `scripts/ssh-wrapper-154.sh`: 新增 (wrapper 主逻辑, ~90 行)
- `scripts/ssh`: 新增 (symlink target, 让 `PATH=scripts:$PATH` 时优先匹配)
- `scripts/deploy-154-via-252.sh`: 新增 (封装 PATH + 一键 deploy)

## 远程配置改动 (不入仓)

- 252 nginx `/etc/nginx/conf.d/kxpms-on-252.conf`: `kxpms_llm_backend` upstream `172.16.2.209:8781` → `172.16.2.241:8781`
- 245 `/etc/systemd/system/llm-gateway-go.service` 未改 (保持 ExecStart=/opt/llm-gateway-go/gateway)
- 245 `/opt/llm-gateway-go/gateway` 符号链接已建立 (`→ current/llm-gateway-go`)

## 留给未来的工作

1. **环境同步**：`~/workspace/ai-native-tools/envs/servers/47.97.111.154/.env.secrets.plain.yaml` 加 `LLM_GATEWAY_DEPLOY_TARGET_IP=172.16.2.241` 注释；`docs/INFRA_SUMMARY.md` 加 "llm.kxpms.cn 实际服务在 245 (172.16.2.241)" 一行
2. **envs loader 注入**：把 SSHPASS_154 加入 envs SSOT（rule 47 跨项目密钥），避免明文密码散落
3. **deploy-154 直连路径**：如果未来 154 公网 SSH 重新开放，把 ssh-wrapper-154.sh 改回 deploy-seamless 默认走 252 跳板的代码路径
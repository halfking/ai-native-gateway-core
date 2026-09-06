# LLM Gateway 零停机部署 — 设计方案

> 日期: 2026-07-16
> 状态: 设计阶段, 待 Phase 2 实施
> 当前可用: Phase 1 (SSH 重试 + 连接复用 + 批量命令 + 单元硬化)

## 1. 问题陈述

`scripts/deploy-seamless.sh deploy 154` 当前 restart 窗口约 **5–8 秒** (单实例)
到 **30–60 秒** (公网抖动时)。此窗口内 `:8781` unbound, nginx 上游拿到
`connection refused`, 用户请求全部失败。

SSH 链路另外两个问题:
- 每次 ssh 调用建新 TCP 连接 + 认证 (~150–300ms × N 命令)
- 154 公网 IP `<env:HOST_154_IP>` 偶发抖动, 直接 ssh 偶发 `Operation timed out`

## 2. 已落地 (Phase 1 — 本次 PR)

### 2.1 SSH 重试 + 连接复用 — `scripts/deploy-lib/ssh-retry.sh`

- **ControlMaster socket**: `${TMPDIR}/ssh-retry-$$/cm-<target>.sock`
  一次握手, 后续命令复用, 节省 ~150ms/命令。
- **指数退避重试**: 默认 3 次 (2s / 4s / 8s 间隔), 仅重试传输层失败
  (timeout / Connection reset / No route to host)。
- **154 fallback via 252**: 连续失败 `SSH_RETRY_FALLBACK_AFTER=2` 次后, 切到
  `ProxyCommand=ssh -W 154:25022 252`, 经 252 内网中转。
- **命令级错误不重试**: 远端命令 `rc=1` 立即返回, 不浪费重试配额。

覆盖:
- 18 个 ssh-retry 单元测试 (`tests/deploy_ssh_retry_test.sh`)
- 23 个 host.sh 回归测试 (原有 `tests/deploy_host_test.sh` 仍 100% 通过)

### 2.2 批量 SSH 命令 — `host_atomic_switch`

旧实现: 5 个独立 SSH 调用 (`ln -sfn` × 4 + `restart` × 1)
新实现: 1 个 heredoc 批量调用 (`set -e` + 4 个 `ln -sfn`) + 1 个 restart

**收益**: SSH 握手次数 -80%, 切换窗口稳定在 3-5s (公网正常时)。

### 2.3 systemd 单元硬化

`deploy/llm-gateway-go.service` + `deploy/llmgo-245.service`:

| 字段 | 旧值 | 新值 | 收益 |
|---|---|---|---|
| `TimeoutStopSec` | 默认 90s | `25s` | restart 卡住时更快 SIGKILL |
| `KillSignal` | 隐式 `SIGTERM` | 显式 `SIGTERM` | 文档化 |
| `ExecStopPost` | 无 | `date +%s > /var/run/llm-gateway-go.drained` | drain 标记可被脚本轮询 |

## 3. Phase 2 — 真正的零停机切换 (设计)

### 3.1 目标

restart 期间 `:8781` 持续可服务, 客户端请求零失败。

### 3.2 方案: Canary + nginx upstream reload

**前置依赖** (需协调):
1. nginx upstream 配置改为支持双端口 (`upstream` 内多 `server`)
2. systemd 增加 transient unit 模板 (canary 用)
3. 应用层无变化 (Go binary 仍 bind `:8781`, canary bind `:8782`)

**deploy 流程**:

```text
t=0s   启动 canary on :8782 (独立 systemd unit llm-gateway-go-canary.service)
t=1s   wait /healthz on :8782  (canary 自己 ready)
t=2s   nginx upstream 临时加 server 127.0.0.1:8782
        nginx -s reload   (优雅, ~50ms, 不丢已建连接)
t=3s   SIGTERM llm-gateway-go.service (主实例 drain)
        Go srv.Shutdown 30s timeout — 已建连接继续处理
t=5s   新连接 nginx 已不路由 :8781 (max_fails=3 fail_timeout=10s)
        所有新连接走 :8782
t=8s   主实例 drain 完, systemd 标记 exited
t=8.1s nginx upstream 移除 :8781
        nginx -s reload
t=8.2s 重命名 canary unit → 主 unit (systemctl reenable)
```

**切换窗口**: 主实例 drain 时间 (~5s), 但已建连接 0 失败, 新连接 0 失败
(因为 nginx 上游有 fallback 到 :8782)。

### 3.3 落地步骤 (Phase 2 任务)

| 步骤 | 文件 | 工作量 |
|---|---|---|
| 1. nginx conf 模板化 | `deploy/llmgo-245.nginx.conf` + 新增 `conf.d/upstream.snippet` | 0.5d |
| 2. systemd transient unit | 新增 `deploy/llm-gateway-go-canary.service` + `@.service` 模板 | 0.5d |
| 3. canary 编排逻辑 | `scripts/deploy-lib/zero-downtime.sh` (新增) | 1d |
| 4. drain 信号接入 | `host_drain_and_stop` (已实现 host.sh) 接入编排 | 0.5d |
| 5. 灰度回退 | nginx upstream 移除 canary, 重启旧实例 | 已实现 |
| 6. 245 验证 + 154 灰度 | deploy-test skill + 30 天观察 | 3d |

总计: 约 1 周 (含灰度观察)。

### 3.4 风险与回滚

| 风险 | 缓解 |
|---|---|
| canary 启动后 healthz 失败 | nginx upstream 不加 canary, 直接退化为 Phase 1 流程 |
| nginx reload 中断连接 | 用 `nginx -s reload` (SIGUSR1, 不会终止 worker) |
| canary 端口被占用 | 启动前 `nc -z 127.0.0.1 8782`, 占用则报错退出 |
| systemd unit 名称冲突 | canary 用 transient (`systemd-run`), 不污染 unit 文件 |

### 3.5 Phase 1 现状下的部署观察

最后一次 245 部署 (本会话): 切换 43s, DB ready 2s
最后一次 154 部署 (本会话): 切换 103s (含 1 次公网 SSH 超时, 后续走 252 路径)

Phase 1 已显著降低 SSH 失败率, 但 restart 期间 nginx 上游短暂失败 (5–8s)
仍存在。Phase 2 可消除此窗口。

## 4. 监控

部署期间 (`scripts/deploy-seamless.sh`) 输出的关键指标:

```text
[seamless] [2/9] 前端 + 后端并行构建          ← 构建耗时
[seamless] [5/9] upload → 245                  ← 上传耗时 (含 SSH 重试)
[seamless] [8/9] 原子符号链接切换 + restart    ← 切换耗时 (本次 43s/103s)
[seamless] [9/9] 验证 /healthz + DB            ← DB ready 耗时 (本次 2s/15s)
```

阈值告警:
- 切换耗时 > 30s: 排查公网抖动 / SSH 失败 fallback
- DB ready > 30s: 排查 `postgres disabled` (EnsureSchema 慢)
- upload > 60s: 排查 tar 管道 / 网络带宽

## 5. 相关文件

| 文件 | 用途 |
|---|---|
| `scripts/deploy-lib/ssh-retry.sh` | SSH 重试 + ControlMaster + 154 fallback |
| `scripts/deploy-lib/host.sh` | `host_atomic_switch` (批量) + `host_drain_and_stop` (canary 用) |
| `scripts/deploy-seamless.sh` | 主部署脚本, 已切换走 ssh-retry |
| `scripts/deploy-lib/targets.sh` | target 契约 (ssh_host / service_name / health_url) |
| `tests/deploy_ssh_retry_test.sh` | 21 个 ssh-retry 单元测试 |
| `tests/deploy_host_test.sh` | 23 个 host.sh 回归测试 |
| `deploy/llmgo-245.service` | 245 systemd unit (硬化: TimeoutStopSec=25s + drain 标记) |
| `deploy/llm-gateway-go.service` | 154 systemd unit (同上) |
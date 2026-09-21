# Local-Host Versioned Deployment

本机 Mac 上的版本化、可回滚、与 245 部署同模型的 LLM Gateway 部署方案。

学习自 245 服务的 `releases/<v>/{gateway, web/, version.json, ...}` + atomic symlink 切换模型，但跑在本地 macOS 上 —— 直接 host 启动 gateway 二进制，不走 systemd。

## 1. 目录布局

```
~/Downloads/llm-gateway-files/
├── bin/
│   ├── <git_tag>.<build_seq>/              # 独立 bundle (如 2.4.7.1795)
│   │   ├── gateway (0755)                  # Go 编译产物
│   │   ├── web/                            # 前端 dist
│   │   ├── version.json                    # 版本 SSOT
│   │   ├── VERSION                         # bundle 名
│   │   ├── env.sh  start.sh  stop.sh       # bundle 内置运行脚本
│   │   │   status.sh  logs.sh
│   │   ├── configs/  SHA256SUMS  deployment.json
│   ├── current → 2.4.7.1795                # atomic ln -sfn
│   ├── start.sh  stop.sh  status.sh        # 顶层符号链接
│   ├── env.sh  logs.sh
├── postgres/data/                          # bind-mount /var/lib/postgresql/data
├── redis/data/
├── logs/  attachments/  raw-logs/  backups/  run/
```

**关键不变量**：
- `bin/current` 始终指向当前 active bundle（atomic symlink）
- `bin/{start,stop,status,env,logs}.sh` 是符号链接到 `current/<script>`，调用时永远跟随 active bundle
- 每个 bundle 内 `env.sh` 携带完整环境（含 PG 密码、admin token、Redis 地址等）
- 已通过 healthz 验证的 bundle 标记 `deployment.json: verified=true`
- 自动 prune：保留 active + 最新 N 个 verified，删除更老的 verified bundle

## 2. 快速上手

### 首次部署

```bash
# 1. 启动依赖容器（PG + Redis），如果还没起
docker start llm-gateway-pg nbjl-redis

# 2. 同步 252 数据库到本地（一次性，按需）
bash scripts/local-host-sync-db.sh

# 3. 注入运行时凭据（必须，否则 deploy 拿不到 PG 密码）
source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go --server <env:HOST_252_IP>

# 4. 编译并部署当前 HEAD
bash scripts/local-host-deploy.sh deploy

# 5. 验证 L1-L4
bash scripts/local-host-deploy-test.sh --verify
```

### 日常迭代

```bash
# 改完代码，deploy + L1-L4 全跑
bash scripts/local-host-deploy-test.sh --keep 3

# 看当前服务
bash scripts/local-host-deploy.sh status

# 看所有 bundle（含已 prune 候选）
bash scripts/local-host-deploy.sh list

# 跟随日志
bash scripts/local-host-deploy.sh logs 100    # tail -n 100 -f
```

### 回退

```bash
# 1. 看哪些版本可回退（仅 verified=true 的才能 rollback）
bash scripts/local-host-deploy.sh list

# 2. 切到目标版本（自动 stop → atomic switch → start → 重新 mark_verified）
bash scripts/local-host-deploy.sh rollback 2.4.7.1793
```

## 3. 命令参考

### `local-host-deploy.sh`

| 命令 | 作用 |
| --- | --- |
| `deploy [--keep N]` | 编译并部署当前 HEAD；默认保留 N=3 个 verified bundle |
| `start` | 启动当前 active（如果 PID 文件存在则先 kill 旧进程） |
| `stop` | 优雅停机（SIGTERM → 30s 超时 → SIGKILL） |
| `restart` | 等价于 `stop` + `start` |
| `status` | 输出 PID、healthz、版本号 |
| `list` | 列出所有 bundle，标注 active/inactive + verified/unverified |
| `rollback <ver>` | 切到指定 verified bundle 并重启 |
| `logs [N]` | `tail -n N -f` 当前日志（默认 50） |
| `verify` | 重跑 L1 healthz（不重启） |
| `cleanup` | 交互式停止并删除 active bundle |
| `env` | 输出当前 active 的 env（供 `source` 使用） |

### `local-host-deploy-test.sh`

| 参数 | 作用 |
| --- | --- |
| `(无)` | full 模式：deploy + L1-L4 验证 |
| `--quick` | 跳过 deploy，仅跑当前服务的 L1-L4 |
| `--verify` | 同 `--quick`，但保留默认 keep 提示 |
| `--clean` | 跑完后 stop + 删除 active bundle |
| `--keep N` | 指定保留 N 个 verified（默认 3） |
| `--port PORT` | 覆盖默认监听端口（默认 8781） |
| `--admin-token TOK` | 覆盖 admin token（默认从 env-injector 拿） |

测试报告：`/tmp/local-host-deploy-test-report.md`，日志：`/tmp/local-host-deploy-test.log`。

## 4. L1-L4 验证层级

| 层级 | 名称 | 检查项 |
| --- | --- | --- |
| L0 | 依赖 | PG 容器 + llm_gateway 表数 + Redis PONG |
| L1 | HTTP 存活 | `/healthz` HTTP 200 + JSON body 字段 + SPA `/` |
| L2 | 依赖连通 | `pg_isready` + PG version |
| L3 | 功能链路 | `/v1/models` 200 + `/v1/chat/completions` 401 (预期) |
| L4 | 业务真实 | `/metrics` (admin Bearer) 200 + 含 prometheus `# TYPE` |

**注意**：
- `/v1/models` 走全局 `AuthMiddleware`，需 `Authorization: Bearer $LLM_GATEWAY_API_KEY`（env-injector 注入）
- `/metrics` 走 `AdminTokenMiddleware`，需 `Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY`
- `/v1/chat/completions` 在 mock upstream（18080）未启 llm-mock 时返回 401/403 是预期的，测试脚本视为通过

## 5. 回退策略

### 自动 prune（每次 deploy 后）

1. `deploy` 完成后，`mark_verified` 把当前 bundle 标 `verified=true`
2. `prune_releases 3` 计算 keep_set = active + 最新 3 个 verified
3. 不在 keep_set 的 verified bundle 被 `rm -rf`

**典型场景**：连续 deploy 5 次后，磁盘上只剩 active + 前 3 次 verified。

### 手动 rollback

```bash
# 看候选
bash scripts/local-host-deploy.sh list
# 2.4.7.1792                 inactive+verified       2026-08-28T14:00:00Z
# 2.4.7.1793                 inactive+verified       2026-08-28T16:00:00Z
# 2.4.7.1794                 inactive+verified       2026-08-28T18:00:00Z
# 2.4.7.1795                 active+verified         2026-08-28T21:08:31Z

# 切到 1793
bash scripts/local-host-deploy.sh rollback 2.4.7.1793
# 内部：lh_atomic_switch → stop → start → mark_verified
```

**安全约束**：
- 只能 rollback 到 `verified=true` 的 bundle（未验证的不可信）
- rollback 后再次 mark_verified，避免下次 prune 把它当候选

## 6. 关键事实

### PG 容器

- 容器名：`llm-gateway-pg`，PG user：`llm_gateway`（**不是** r112 compose 的 `kxuser`）
- 密码从 env-injector 的 `COMMON_PG_SUPERUSER_PASS` 拿，**不要**硬编码 `kxpass`
- bind-mount 到 `~/Downloads/llm-gateway-files/postgres/data/`，容器外

### Redis

- 容器名：`nbjl-redis`，端口：`127.0.0.1:16379`（**不是** 6379）
- deploy.sh 默认 `LLM_GATEWAY_REDIS_ADDR=127.0.0.1:16379`

### System Monitor

- `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true` 是新版本必备
- 否则 `ursm.v2 authoritative mode` 启动失败

### Active 守卫

即使 prune 的 keep_set 算错（active 丢失），`lh_prune_releases` 也有 fallback：
```bash
if [[ "$v" == "$active" && -n "$active" ]]; then
  continue   # 永远不删 active
fi
```

## 7. 故障排查

### deploy 报 `healthz did not respond in 60s`

```bash
# 1. 看 stderr
tail -50 ~/Downloads/llm-gateway-files/logs/gateway.stderr.log

# 2. 常见原因：PG 连接失败（密码不对）
docker exec llm-gateway-pg pg_isready -U llm_gateway -d llm_gateway

# 3. 常见原因：Redis 端口不对（16379 vs 6379）
redis-cli -h 127.0.0.1 -p 16379 ping

# 4. 重启一次试试
bash scripts/local-host-deploy.sh restart
```

### 测试 L3/L4 401

```bash
# 看是不是 env-injector 没注入
echo "API_KEY=${LLM_GATEWAY_API_KEY:0:8}..."
echo "ADMIN_KEY=${LLM_GATEWAY_ADMIN_API_KEY:0:8}..."

# 重新 source
source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go --server <env:HOST_252_IP>
```

### 孤儿 gateway 进程

多次 deploy 可能留下未通过 PID file 跟踪的孤儿：
```bash
ps -ef | grep "\./gateway" | grep -v grep
# 保留最新那个（端口 8781 的 listen PID），kill 其他
kill <orphan_pid>
```

### Prune 把不该删的删了

prune 有 3 重防护，按顺序：
1. `lh_list_verified_releases` 跳过 active
2. keep_set 显式包含 active
3. 删除前再 `[[ "$v" == "$active" ]]` 兜底

如果发现误删，看 `~/Downloads/llm-gateway-files/bin/` 是否还能 restore。

## 8. 与 245 部署的差异

| 维度 | 245 部署 | 本机 (local-host) |
| --- | --- | --- |
| 位置 | `/opt/llm-gateway-go/releases/` | `~/Downloads/llm-gateway-files/bin/` |
| 启动 | systemd unit | `nohup ./gateway &` + PID file |
| 端口 | 8000（nginx → upstream） | 8781（直接 host listen） |
| Verified 保留 | 5 | 3（保守磁盘） |
| 数据 | `/opt/llm-gateway-go/data/` (host) | `~/Downloads/llm-gateway-files/postgres/` (host bind-mount) |
| 前端 | nginx serve | `LLM_GATEWAY_STATIC_DIR=./web` 内嵌 |
| Prune 时机 | 每次 deploy 后 | 每次 deploy 后（同一逻辑） |
| 健康检查 | systemd + curl | start.sh 等 healthz 60s |

## 9. 相关文件

- `scripts/local-host-layout-helper.sh` — stage/verify/atomic_switch/list/prune/mark_verified/rollback 原语
- `scripts/local-host-deploy.sh` — 部署 CLI 入口
- `scripts/local-host-deploy-test.sh` — L1-L4 验证
- `scripts/local-host-sync-db.sh` — 252 → 本地 PG 同步
- 参考（不变更）：`scripts/deploy-lib/host.sh` (245 模型)、`scripts/deploy-seamless.sh`、`docs/06-deployment/02-database/local-pg-sync-from-252.md`

# 245 pre-prod gateway runbook

> **Audience**: 245 SRE / on-call
> **Date**: 2026-07-20
> **Build under test**: `v2.4.7-cdb185f86-20260720-1215-7e721f86` (seq 1215)
> **角色**: pre-prod（llmgo.kxpms.cn），新版本必须**先 245 后 154**（详见 §10）

## 1. 245 主机基础

```
hostname:        iZbp1ipiir49tmm01urycqZ
公网 IP:         <env:HOST_245>
私网 IP:         <env:HOST_245_PRIVATE>
SSH 端口:        <env:SSH_PORT>
SSH key:         <env:SSH_KEY_245>
DB 接入:         <env:COMMON_PG_HOST_252>:<env:COMMON_PG_PORT_252>/llm_gateway
Redis:           <env:COMMON_REDIS_HOST_252>:<env:COMMON_REDIS_PORT_252>（252 shared，**245 本地无 redis**）
services:        llmgo-245.service (systemd, port 8781, pre-prod vendor unit)
                  nginx (systemd, ports 80/443)
                  llmgo-prometheus.service (systemd, :9090, host-resources.rules.yml)
                  llmgo-alertmanager.service (systemd, :9093/:9094)
                  quality-service.service (systemd, :8081, 5m scheduler)
                  collector-service.service (systemd, 1m/1h interval)
                  aegis.service (阿里云安全 agent)
                  ai-native-maintain.service (lifecycle / ops)
                  aliyun.service / cloudmonitor.service (阿里云监控)
                  chronyd.service (NTP) / crond.service (cron jobs)
```

## 2. 启动顺序 (cold start)

1. 252 先起（网关 DB / Redis / NPS / cert relay 都在 252）
2. 245 自动起来 (`systemd default target`)
3. `systemctl is-active llmgo-245 nginx` 都应 `active`
4. `curl -sS http://127.0.0.1:8781/healthz` 应返回 200 + `2.4.7-...`
5. 252 nginx 上 `kxpms-on-252.conf` 应当把 `kxpms_llm_backend` 指向 `172.16.2.241:8781` (=245)，**不是** 154 (172.16.2.209)。如果发现还是 209，跑：
   ```bash
   ssh 252 'sed -i "s|server 172.16.2.209:8781 max_fails=2 fail_timeout=5s;|server 172.16.2.241:8781 max_fails=2 fail_timeout=5s;|" /etc/nginx/conf.d/kxpms-on-252.conf'
   ssh 252 'nginx -t && nginx -s reload'
   ```

## 3. 部署 (deploy-245)

245 部署走 `deploy-245.sh`（或对应 skill）：

```bash
# 1. 准备 (本地 main 分支)
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
git status -uno    # 确认 working tree 干净
git log -1         # 看 HEAD commit
git fetch origin

# 2. bump version
bash scripts/bump-version.sh --seq 1216

# 3. 部署
bash scripts/deploy-245.sh --seq 1216
#  或通过 skill:
#  /deploy-245

# 4. 部署过程会做:
#   - 前端 npm build + 后端 CGO_ENABLED=0 go build (本地 Mac 不需要 cross-compile)
#   - tar pipe 把 release/{seq}-sha 推到 245 releases/
#   - 远端 sha256 校验
#   - DB pending migration 跑 + db-changelog
#   - 原子符号链接 current → releases/{seq}-sha
#   - systemd restart llmgo-245
#   - healthz + DB ready 验证 (失败自动 rollback)

# 5. 部署完必看
ssh 245 'systemctl status llmgo-245 --no-pager -l | head -10'
ssh 245 'curl -sS http://127.0.0.1:8781/healthz'
ssh 245 'journalctl -u llmgo-245 --since "3 min ago" --no-pager | grep -E "WARN|ERROR|panic" | head -10'
```

## 4. 回滚 (rollback)

```bash
# 方式 1: deploy-seamless 自动选择 verified+active 之前的版本
bash scripts/deploy-seamless.sh rollback 245

# 方式 2: 手动回滚
ssh 245 '
  cd /opt/llm-gateway-go
  CURRENT=$(readlink current)
  # 列已有 releases:
  ls -lt releases/ | head -10
  # 选老版本:
  OLD=1201-c1a01552  # 改为你想回滚的版本
  ln -sfn releases/$OLD current
  ln -sfn current/llm-gateway-go llm-gateway-go
  systemctl restart llmgo-245
'
```

## 5. 监控 (observability checklist)

| signal | how to check | threshold |
|---|---|---|
| healthz | `curl -sS http://127.0.0.1:8781/healthz` | 200 |
| gateway 上游超时 | `journalctl -u llmgo-245 --since "10m ago" \| grep "upstream_status" \| grep -E "(50[0-9]\|40[0-9])"` | 0 个 5xx 4xx |
| 长 latency | `journalctl -u llmgo-245 --since "10m ago" \| grep "latency_ms" \| awk -F, '{print $1}' \| sort -t: -k2 -n` | max < 30s |
| commit/rollback | `journalctl -u llmgo-245 --since "1h ago" \| grep -i "rollback"` | 0 |
| panic | `journalctl -u llmgo-245 --since "30d ago" \| grep -i panic` | 0 |
| OOM killed | `dmesg --since "30d ago" \| grep -iE "oom\|killed process"` | 0 |
| MemoryMax 触发 | `journalctl -u llmgo-245 --since "10m ago" \| grep -i "memory" \| grep -i "kill"` | 0 |
| requestdetail Clear residue | `curl -s http://127.0.0.1:9090/metrics \| grep requestdetail_store_clear_failures_total` | rate == 0 |
| requestdetail eviction residue | `curl -s http://127.0.0.1:9090/metrics \| grep requestdetail_store_eviction_failures_total` | rate == 0 |
| requestdetail oversize drop | `curl -s http://127.0.0.1:9090/metrics \| grep requestdetail_forwarder_dropped_oversize_total` | rate == 0；持续 > 0 提示客户端发送了异常大体量 body |
| requestdetail malformed snapshot | `curl -s http://127.0.0.1:9090/metrics \| grep requestdetail_malformed_snapshot_total` | rate == 0；持续 > 0 提示 /tmp 写入或 JSON 序列化有 bug |
| requestdetail forwarder stop timeout | `journalctl -u llmgo-245 --since "10m ago" \| grep "capture forwarder stop timed out"` | 0；> 0 提示 /tmp 极慢或磁盘 I/O 拥塞 |
| cert 剩余 | `ssh 245 'certbot certificates 2>&1 \| grep "Expiry Date"'` | ≥ 30 days |
| DB cache hit | `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT ROUND(100.0*blks_hit/(blks_hit+blks_read),1) FROM pg_stat_database WHERE datname='llm_gateway';"` | ≥ 99 % |
| DB rollback 率 | `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT ROUND(100.0*xact_rollback::numeric/(xact_commit+xact_rollback),1) FROM pg_stat_database WHERE datname='llm_gateway';"` | ≤ 5 % |
| 慢查询 top-5 | `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT substring(query,1,80), ROUND(mean_exec_time::numeric,1) FROM pg_stat_statements ORDER BY mean_exec_time DESC LIMIT 5;"` | mean < 1000 ms |
| 连接池 | `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT count(*) FROM pg_stat_activity WHERE datname='llm_gateway';"` | ≤ 50 |
| upstream candidates | 打开 dashboard swim lane "供应商" 维度 | open_conns/inflight 接近 1.0 是健康 |

## 6. 已知警告 / 已知问题 / 风险

- **MemoryHigh=1500M 在 cgroup v1 + systemd v239 上不生效**。
  systemd v239 起 `MemoryHigh=` 仅 cgroup v2 支持；cgroup v1 下只有 `MemoryMax=2G` 硬上限起作用。
  实际 OOM 触发后 systemd 直接 SIGKILL 进程（无 graceful throttle）。需监控 `dmesg | grep -i oom`。
  缓解：长期方案升级 systemd 或迁移 cgroup v2；短期只看 MemoryMax 边界，不依赖 soft throttle。
- **llmgo-245.service 是 pre-prod vendor unit**，不是通用 `llm-gateway-go.service`。
  deploy-245 / deploy-seamless / journalctl / systemctl 一律用 `llmgo-245`，不要用 unit 名 `llm-gateway-go`。
- **certbot-renew.timer** 启用并独立于 154。`systemctl list-timers certbot-renew.timer` 显示 `Thu 2026-08-20 08:09:19 CST 9h left`（下次运行时间）。当前 245 上 certbot 管理的证书：**`download.kxpms.cn`**（2026-10-14 到期，VALID 55 天）。`llmgo.kxpms.cn` / `llm.kxpms.cn` 的证书是**手工放置**到 `/etc/letsencrypt/live/kxpms.cn/`（245 上由 nginx 持有，certbot **不管理**这两个证书，续签走手工流程）。
- **252 nginx kxpms-on-252.conf 现在 upstream 指向 172.16.2.241:8781 (245)**。
  245 是 pre-prod，154 production upstream = 172.16.2.209:8781。两个后端 IP 不混用，分别承载 llmgo.kxpms.cn / llm.kxpms.cn。
- **`/etc/nginx/conf.d/llm-kxpms-cn.conf`** 重复 listen 警告（80 + 443 + IPv4/IPv6）：已确认。`nginx -T | grep "^listen"` 在 245 上输出 `listen 80; × 6`、`listen 443 ssl http2; × 3`、`listen [::]:80; × 1`、`listen [::]:443 ssl; × 1`，属于 nginx 多 vhost + IPv4/IPv6 双栈声明。**nginx 启动时只 WARN 不 FAIL**，245 当前 active 无问题；与 154 模式相同。

## 7. cert 应急 / 失败时

```bash
# 1. 看 cert 状态
ssh 245 'certbot certificates'

# 2. 如果 cert 真的过期 (X days negative), 手动跑 certonly
ssh 245 'certbot certonly --nginx -d <DOMAIN> --non-interactive --agree-tos -m ops@kaixuan.ai'

# 3. 确认 timer 仍然 enabled
ssh 245 'systemctl list-timers | grep certbot'

# 4. dry-run 一次
ssh 245 'certbot renew --dry-run'
```

## 8. Redis 容量 / cleanup

```bash
# 245 上没有本地 redis（已 SSH 验证：`ps -ef | grep redis` 空，`ss -tlnp | grep 6379` 空）
# 仅使用 252 shared redis（`<env:COMMON_REDIS_HOST_252>:<env:COMMON_REDIS_PORT_252>`）
# 验证:
ssh 245 'redis-cli -h "$COMMON_REDIS_HOST_252" -p "$COMMON_REDIS_PORT_252" dbsize'
# 异常排查走 §8.1
```

## 9. 常见 on-call 场景

### 9.1 "minimax 调用全 5xx"
- `journalctl -u llmgo-245 --since "10m ago" | grep "upstream_status\":5`
- 确认 provider 14 (MiniMax api.minimaxi.com) 在 245 网关 curl 直连：
  ```bash
  curl -sS --max-time 5 -o /dev/null -w "HTTP=%{http_code} time=%{time_total}s\n" \
    -X POST 'https://api.minimaxi.com/v1/chat/completions' \
    -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer __API_KEY_1__' \
    -d '{"model":"MiniMax-M3","messages":[{"role":"user","content":"ping"}],"max_tokens":3,"stream":false}'
  ```
- 若 api.minimaxi.com 自己挂 (e.g. HTTP 5xx)，则走路由自动 fallback (next plan)。
  查 `request_logs_hot` 看 `transform_summary='egress=openai-completions' AND provider=14 AND success=false AND ts > NOW() - INTERVAL '10 min'`
- 若 245 也挂, 看 245 / 154 之间的 healthz 区别

### 9.2 "healthz 失败 / 5xx"
- 立即 `systemctl status llmgo-245 --no-pager -l | head -20`
- 若 OOM, 看 `dmesg | tail -20` (kernel 杀进程记录) + `journalctl -u llmgo-245 | grep -i "memory"`
- 若 panic, 看最近 200 行 gateway log
- `kill -9` + `systemctl reset-failed llmgo-245 && systemctl start llmgo-245`
- 验证 build 仍 1215 (没被 deploy 覆盖) — `readlink /opt/llm-gateway-go/llm-gateway-go`

### 9.3 "deploy 失败 (verify/atomic 不通过)"
- `deploy-seamless.sh` 会自动 rollback 到前一个 verified 版本
- 看 `~/.acc/logs/deploys/<service>-<date>.log` (deploy script 写日志)
- 手动回滚参见 §4
- 常见失败原因:
  - 校验 sha256 mismatch (本地编译和远端 binary 不一致)
  - atomic switch 时 nginx 同时 reload 卡住 (systemd `Requires` 缺)
  - DB migration 失败 (schema 漂移)
  - 远端 healthz 30s 没回 (新代码起不来)

### 9.4 "deploy 一启动就报 `local lock held`"
- 报错形如: `ERROR: local lock held at /var/folders/.../T/kx-llm-gateway-deploy-245.lock`，下面打印持有者元数据 (target/source_user/source_host/pid/started_at/commit/version)。
- `deploy-245.sh --force`（`--force-unlock` 兼容别名）会按顺序恢复 245 目标本地锁、245 远端锁、共享构建锁，然后重新获取全部锁；它不是跳过锁。仅在确认旧部署不应继续运行时使用。
- 先确认是不是真有另一个 245 deploy 在跑: 看目标锁元数据里的 `pid` 和 `started_at`。
  - **pid 还活着** → 那个 deploy 还在跑，别解锁，等它自然完成。
  - **pid 已死 / 是另一个 repo checkout 的陈旧锁** → 用 `--force-unlock`:
    ```bash
    bash scripts/deploy-245.sh --force-unlock
    # 或单独用：
    bash scripts/deploy-lib/unlock-local.sh --target 245 --force
    ```
  - 默认模式 (无 `--force`) 只是报告，不删；只有加了 `--force` 才会真的 `rm -rf`，并在删之前对仍活着的 holder 发 SIGTERM → SIGKILL。
- 目标锁按 `${TMPDIR}` 和目标派生：245 使用 `kx-llm-gateway-deploy-245.lock`，154 使用 `kx-llm-gateway-deploy-154.lock`，不同目标互不阻塞。
- 由于 bump/version、`web/dist` 和本地 stage 共用 checkout，部署还会短暂持有 `${TMPDIR}/kx-llm-gateway-build.lock`；它只保护构建阶段，不能用目标锁的 force-unlock 命令清理。

#### 9.4.1 远端锁残留 (`remote lock held`)
- 远端锁路径固定为 `/var/lib/llm-gateway-go/deploy.lock` (154/245 同机同路径)。
- 正常流程：`deploy` / `rollback` 获取后由 `deploy-seamless.sh` 的 EXIT trap 释放。仅当 **SSH 断开 / 主机重启 / 进程被 `kill -9`** 等极端情况下才会残留，导致下一次部署 fail-fast。
- 先 SSH 进 245 确认是否还有 deploy 进程在跑:
  ```bash
  ssh -p 25022 root@8.136.114.245 'cat /var/lib/llm-gateway-go/deploy.lock/metadata; ps -p $(awk -F= "/^pid=/{sub(\"pid=\",\"\");print}" /var/lib/llm-gateway-go/deploy.lock/metadata)'
  ```
  - **metadata 里的 PID 还活着** → 那个 deploy 还在跑，别解锁，等它自然完成。
  - **PID 已死 / 主机重启过** → 用 `unlock-remote.sh` 清远端锁 (与本地 `unlock-local.sh` 平行设计):
    ```bash
    bash scripts/deploy-lib/unlock-remote.sh 245            # 只报告，不删
    bash scripts/deploy-lib/unlock-remote.sh 245 --force    # 确认无误后删
    ```
  - 默认模式 (无 `--force`) 仅读 metadata 报告持有者，**不删**；加了 `--force` 才 `rm -rf` 远端锁目录。
  - 与本地 helper 不同：远端 metadata 中的 PID 是发起部署进程的本机 PID，不是 245 上的 PID；`--force` 只清锁、**不杀远端进程**。执行前必须从发起机确认该部署进程已结束。
  - sanity check：metadata 里的 `target` 必须与传入的 `<target>` 一致 (154 vs 245)，不一致直接拒绝 (防止 SSH 连错主机误删别人的锁)。
  - 校验入口：`scripts/deploy-lib/test/test-unlock-remote.sh` (14 路 stub e2e，可在无真实 SSH 的情况下回归)。

### 9.5 "DB 太慢 / connection 池满"
- `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT count(*), state FROM pg_stat_activity WHERE datname='llm_gateway' GROUP BY state;"`
- 若 active 持续 > 50，看 `pg_stat_activity` 中长 query
- 看 `pg_stat_statements` top 5 slow query, 找 idempotent retry / N+1 pattern
- 若 commit 等待 > 1s，看 checkpoint / bgwriter 状态

## 10. 升级 checklist (245 pre-prod → 154 production)

245 是 pre-prod，新版本必须**先 245 后 154**（245 验收 ≥ 1 周稳定再升 154）：

```
[ ] git log upstream main..HEAD 看新提交 (改了什么)
[ ] deploy-seamless dry-run: bash scripts/deploy-seamless.sh status 245
[ ] bump version 1216+ (按 deploy 顺序)
[ ] deploy 到 245 (本文档 §3)
[ ] 部署后必做 (245 acceptance, 至少跑 1 周无 P0/P1):
    [ ] curl healthz
    [ ] curl minimax-m3 一次 (live, 用 hermes api key)
    [ ] curl claude-sonnet-5 一次
    [ ] journalctl 最近 5 min 无 panic / OOM / MemoryMax kill
    [ ] gateway pprof 看 goroutine 数 (稳定 100±10)
    [ ] §5 所有监控指标 1 周稳态无漂移
[ ] 通知 stakeholder (飞书 / slack)
[ ] 245 跑 1 周稳定后, 按 154-runbook §10 流程升 154
```

## 11. Reference

- 154 production runbook: `docs/06-deployment/04-runbooks/ops/154-runbook.md`
- 245 stability report: **不存在**（项目内目前只有 154 stability report：`docs/archive/process/process/2026-07/2026-07-20-154-stability-report.md`；245 计划在 pre-prod 验收 1 周后产出 `docs/design/2026-08-XX-245-stability-report.md`，对齐 154 模板结构）
- deploy 脚本: `scripts/deploy-245.sh`, `scripts/deploy-seamless.sh`
- deploy skill: `/deploy-245` (in `~/.agents/skills/deploy-245/`)
- P0/P1 修复 commit: 见 `git log --grep="P0\|P1" --oneline main`
- live swim lane UI: 打开 llmgo.kxpms.cn admin → "供应商" 维度泳道
- pprof diagnostic: `http://127.0.0.1:6060/debug/pprof/` (loopback only, ssh tunnel: `ssh -L 6060:127.0.0.1:6060 root@245`)

## 12. Request-Detail pre-release validation (2026-08-28 audit follow-up)

Request-Detail 审计闭环第三轮新增的回归验证。每次发版前在本地跑一遍，全部通过再 deploy 到 245。

### 12.1 编译 / 测试

```bash
# 1. 编译 (5 秒以内)
go build ./cmd/gateway

# 2. 单元测试 (requestdetail 包 < 5 秒)
go test ./domains/requestdetail/ -race -count=1

# 3. admin 包必须全绿 (含 5 个跨租户隔离回归测试)
go test ./admin/ -count=1 -timeout 5m

# 4. bg 包
go test ./bg/... -count=1 -timeout 3m

# 5. vet
go vet ./...
```

任何一步失败 → **不要 deploy**，回滚到上一个 verified 版本。

### 12.2 request_id 兼容性

`domains/requestdetail/safe_id_test.go` 的两个测试是 request-id 格式的"宪法"，任何修改都必须保持通过：

- `TestSafeRequestIDCompatibilityMatrix` — 30+ 行兼容性矩阵（含 path-traversal 向量、UUID 格式、连字符连号拒绝等）
- `TestSafeRequestIDCompatibleIDsEndToEnd` — hex-only / uuid-dashed / prefixed 三类 id 端到端 round-trip

如果新增了一种 request_id 格式（例如新引入 ulid），先在矩阵里加一行 + 更新 store.go 的 `safeRequestIDPattern`，再发布。

### 12.3 部署后残留监控

部署成功后立即验证两个新指标（§5 已加入监控表）：

```bash
# prometheus 直接拉
curl -s http://127.0.0.1:9090/metrics | grep requestdetail_store_clear_failures_total
curl -s http://127.0.0.1:9090/metrics | grep requestdetail_store_eviction_failures_total
```

正常情况下两者都应 = 0。如果出现 > 0：

1. 立即 `journalctl -u llmgo-245 --since "10m ago" | grep requestdetail` 看 slog.Warn
2. 命中 `result="permission"` 标签 → 检查 `/tmp/llmgw-request-detail` 的 ownership/mount flag
3. 命中 `result="other"` 标签 → 检查 `/tmp` 的 inode/磁盘空间

### 12.4 多副本可见性（2026-08-28 仍为已知缺陷）

详见 `docs/implementation/request-detail-cross-replica-visibility-20260828.md`。当前部署假设 sticky-session，跨副本查询在 persist 完成前会返回 metadata-only 警告或 404。已记录为中期改造项（方案 B + D），本节不阻塞 245 验证。


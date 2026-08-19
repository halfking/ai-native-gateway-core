# 245 pre-prod gateway runbook

> **Audience**: 245 SRE / on-call
> **Date**: 2026-07-20
> **Build under test**: `v2.4.7-cdb185f86-20260720-1215-7e721f86` (seq 1215)
> **角色**: pre-prod（llmgo.kxpms.cn），新版本必须**先 245 后 154**（详见 §10）

## 1. 245 主机基础

```
hostname:        [TODO: verify]
公网 IP:         8.136.114.245
私网 IP:         172.16.2.241
SSH 端口:        25022
SSH key:         ~/.ssh/id_ed25519 (operator's default)
DB 接入:         172.16.2.210:5432/llm_gateway (shared PG17 on 252)
Redis:           172.16.2.210:6389 (252, shared)
services:        llmgo-245.service (systemd, port 8781, pre-prod vendor unit)
                 nginx (systemd, ports 80/443)
                 [TODO: verify - 其他常驻 service 列表]
```

## 2. 启动顺序 (cold start)

1. 252 / 184 先起 (网关 DB / Redis / NPS / cert relay 都在 252)
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
- **certbot-renew.timer** 是否启用与 154 独立。`[TODO: verify - 245 certbot timer 启用状态与续签 cron 配置]`
- **252 nginx kxpms-on-252.conf 现在 upstream 指向 172.16.2.241:8781 (245)**。
  245 是 pre-prod，154 production upstream = 172.16.2.209:8781。两个后端 IP 不混用，分别承载 llmgo.kxpms.cn / llm.kxpms.cn。
- **`/etc/nginx/conf.d/llm-kxpms-cn.conf`** 重复 listen 警告（80 + 443 + IPv4/IPv6）[TODO: verify - 245 是否与 154 相同警告]。

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
# 245 上是否独立 redis? [TODO: verify - 245 local-redis 是否启用, 还是只用 252 shared redis]
ssh 245 'redis-cli dbsize'
# 若 dbsize > 0 且 keys 数异常, 走 §8.1 排查
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

### 9.4 "DB 太慢 / connection 池满"
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
- 245 stability report: [TODO: verify]
- deploy 脚本: `scripts/deploy-245.sh`, `scripts/deploy-seamless.sh`
- deploy skill: `/deploy-245` (in `~/.agents/skills/deploy-245/`)
- P0/P1 修复 commit: 见 `git log --grep="P0\|P1" --oneline main`
- live swim lane UI: 打开 llmgo.kxpms.cn admin → "供应商" 维度泳道
- pprof diagnostic: `http://127.0.0.1:6060/debug/pprof/` (loopback only, ssh tunnel: `ssh -L 6060:127.0.0.1:6060 root@245`)
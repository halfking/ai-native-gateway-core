# 154 production gateway runbook

> **Audience**: 154 SRE / on-call
> **Date**: 2026-07-20
> **Build under test**: `v2.4.7-cdb185f86-20260720-1215-7e721f86` (seq 1215)

## 1. 154 主机基础

```
hostname:        iZbp1efbv6824518ejqh8aZ (alibaba-aliyun ECS)
公网 IP:         47.97.111.154
私网 IP:         172.16.2.209
SSH 端口:        25022
SSH key:         ~/.ssh/id_ed25519 (operator's default)
DB 接入:         172.16.2.210:5432/llm_gateway (shared PG17 on 252)
Redis:           172.16.2.210:6389 (252)
services:        llm-gateway-go (systemd, port 8781)
                 nginx (systemd, ports 80/443)
                 casdoor-server (port 8000, 5 days up)
                 license-authority (port 8443, 5 days up)
                 maintain (port 8082, 5 minutes up)
                 cloudreve (port 5212, 5 days up)
                 local-redis (port 6379, 0 keys, only used by gateway internals)
                 sendmail (port 25, legacy, not used)
```

## 2. 启动顺序 (cold start)

1. 252 / 184 先起 (网关 DB / Redis / NPS / cert relay 都在 252)
2. 154 自动起来 (`systemd default target`)
3. `systemctl is-active llm-gateway-go nginx` 都应 `active`
4. `curl -sS http://127.0.0.1:8781/healthz` 应返回 200 + `2.4.7-...`
5. 252 nginx 上 `kxpms-on-252.conf` 应当把 `kxpms_llm_backend` 指向 `172.16.2.209:8781` (=154)，**不是** 245。如果发现还是 245，跑：
   ```bash
   ssh 252 'sed -i "s|server 172.16.2.241:8781 max_fails=2 fail_timeout=5s;|server 172.16.2.209:8781 max_fails=2 fail_timeout=5s;|" /etc/nginx/conf.d/kxpms-on-252.conf'
   ssh 252 'nginx -t && nginx -s reload'
   ```

## 3. 部署 (deploy-245-style / deploy-seamless)

154 部署走 `deploy-seamless.sh deploy 154 --direct --seq N`：

```bash
# 1. 准备 (本地 main 分支)
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
git status -uno    # 确认 working tree 干净
git log -1         # 看 HEAD commit
git fetch origin

# 2. bump version
bash scripts/bump-version.sh --seq 1216

# 3. 部署
bash scripts/deploy-seamless.sh deploy 154 --direct --seq 1216
#  --direct: 跳过 252 跳板机 (154 公网 SSH 已通)
#  --seq N:   build_seq 强制

# 4. 部署过程会做:
#   - 前端 npm build + 后端 CGO_ENABLED=0 go build (本地 Mac 不需要 cross-compile)
#   - tar pipe 把 release/{seq}-sha 推到 154 releases/
#   - 远端 sha256 校验
#   - DB pending migration 跑 + db-changelog
#   - 原子符号链接 current → releases/{seq}-sha
#   - systemd restart llm-gateway-go
#   - healthz + DB ready 验证 (失败自动 rollback)

# 5. 部署完必看
ssh 154 'systemctl status llm-gateway-go --no-pager -l | head -10'
ssh 154 'curl -sS http://127.0.0.1:8781/healthz'
ssh 154 'journalctl -u llm-gateway-go --since "3 min ago" --no-pager | grep -E "WARN|ERROR|panic" | head -10'
```

## 4. 回滚 (rollback)

```bash
# 方式 1: deploy-seamless 自动选择 verified+active 之前的版本
bash scripts/deploy-seamless.sh rollback 154

# 方式 2: 手动回滚
ssh 154 '
  cd /opt/llm-gateway-go
  CURRENT=$(readlink current)
  # 列已有 releases:
  ls -lt releases/ | head -10
  # 选老版本:
  OLD=1201-c1a01552  # 改为你想回滚的版本
  ln -sfn releases/$OLD current
  ln -sfn current/llm-gateway-go llm-gateway-go
  systemctl restart llm-gateway-go
'
```

## 5. 监控 (observability checklist)

| signal | how to check | threshold |
|---|---|---|
| healthz | `curl -sS http://127.0.0.1:8781/healthz` | 200 |
| gateway 上游超时 | `journalctl -u llm-gateway-go --since "10m ago" \| grep "upstream_status" \| grep -E "(50[0-9]\|40[0-9])"` | 0 个 5xx 4xx |
| 长 latency | `journalctl -u llm-gateway-go --since "10m ago" \| grep "latency_ms" \| awk -F, '{print $1}' \| sort -t: -k2 -n` | max < 30s |
| commit/rollback | `journalctl -u llm-gateway-go --since "1h ago" \| grep -i "rollback"` | 0 |
| panic | `journalctl -u llm-gateway-go --since "30d ago" \| grep -i panic` | 0 |
| OOM killed | `dmesg --since "30d ago" \| grep -iE "oom\|killed process"` | 0 |
| cert 剩余 | `ssh 154 'certbot certificates 2>&1 \| grep "Expiry Date"'` | ≥ 30 days |
| DB cache hit | `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT ROUND(100.0*blks_hit/(blks_hit+blks_read),1) FROM pg_stat_database WHERE datname='llm_gateway';"` | ≥ 99 % |
| DB rollback 率 | `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT ROUND(100.0*xact_rollback::numeric/(xact_commit+xact_rollback),1) FROM pg_stat_database WHERE datname='llm_gateway';"` | ≤ 5 % |
| 慢查询 top-5 | `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT substring(query,1,80), ROUND(mean_exec_time::numeric,1) FROM pg_stat_statements ORDER BY mean_exec_time DESC LIMIT 5;"` | mean < 1000 ms |
| 连接池 | `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT count(*) FROM pg_stat_activity WHERE datname='llm_gateway';"` | ≤ 50 |
| upstream candidates | 打开 dashboard swim lane "供应商" 维度 | open_conns/inflight 接近 1.0 是健康 |

## 6. 已知警告 / 已知问题 / 风险

- **certbot-renew.timer** 装在 154 (Mon 03:30)；reload-nginx hook 已配置。
  风险：cron 失效时 cert 不会自动续签。需人工 fallback 流程（见 §7）。
- **NVIDIA NIM provider_models.id=1155355 已 `available=false`**。客户端 minimax-m3 路由到 MiniMax 直连 (provider 14)。
  风险：若要重新启用 NVIDIA minimax-m3，先确认 `integrate.api.nvidia.com/v1/chat/completions` model=`minimaxai/minimax-m3` 已恢复可用。
- **`/etc/nginx/conf.d/llm-kxpms-cn.conf:27,28`** 仍有两个 `protocol options redefined` 警告 (重复 listen 80 + 443 + IPv4/IPv6)，不影响 serve。
  是 nginx 老问题，可在下次 maintain 时一起改。
- **`/etc/nginx/conf.d/kxpms-cn-auth.conf:13,14, kxpms-cn-www.conf:13,14`** 警告是其他 service conf，不归 154 管。
- **252 nginx kxpms-on-252.conf 现在 upstream 指向 172.16.2.209:8781 (154)**。如果 154 down，fallback 是 252 → 252 → 154 自然错误，不会 fallback 到 245 (除非手动改 nginx conf)。

## 7. cert 应急 / 失败时

```bash
# 1. 看 cert 状态
ssh 154 'certbot certificates'

# 2. 如果 cert 真的过期 (X days negative), 手动跑 certonly
ssh 154 'certbot certonly --nginx -d <DOMAIN> --non-interactive --agree-tos -m ops@kaixuan.ai'

# 3. 确认 timer 仍然 enabled
ssh 154 'systemctl list-timers | grep certbot'

# 4. dry-run 一次
ssh 154 'certbot renew --dry-run'
```

## 8. Redis 容量 / cleanup

```bash
# Redis local (154 上) 仅存 gateway 进程内部数据, 几乎空
ssh 154 'redis-cli dbsize'
# 应是 0 键, 看到 100+ 键要看为什么
```

## 9. 常见 on-call 场景

### 9.1 "minimax 调用全 5xx"
- `journalctl -u llm-gateway-go --since "10m ago" | grep "upstream_status\":5`
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
- 立即 `systemctl status llm-gateway-go --no-pager -l | head -20`
- 若 OOM, 看 `dmesg | tail -20` (kernel 杀进程记录)
- 若 panic, 看最近 200 行 gateway log
- `kill -9` + `systemctl reset-failed llm-gateway-go && systemctl start llm-gateway-go`
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

## 10. 升级 checklist (新版本发布前)

```
[ ] git log upstream main..HEAD 看新提交 (改了什么)
[ ] 245 预发布已经 deploy + 1 周无 P0/P1 issue
[ ] deploy-seamless dry-run: bash scripts/deploy-seamless.sh status 154
[ ] bump version 1216+ (按 deploy 顺序)
[ ] deploy 到 154 (本文档 §3)
[ ] 部署后必做:
    [ ] curl healthz
    [ ] curl minimax-m3 一次 (live, 用 hermes api key)
    [ ] curl claude-sonnet-5 一次
    [ ] journalctl 最近 5 min 无 panic / OOM
    [ ] gateway pprof 看 goroutine 数 (稳定 100±10)
[ ] 通知 stakeholder (飞书 / slack)
```

## 11. Reference

- 154 stability report: `docs/design/2026-07-20-154-stability-report.md`
- deploy 脚本: `scripts/deploy-154.sh`, `scripts/deploy-seamless.sh`
- P0/P1 修复 commit: 见 `git log --grep="P0\|P1" --oneline main`
- live swim lane UI: 打开 llm.kxpms.cn admin → "供应商" 维度泳道
- pprof diagnostic: `http://127.0.0.1:6060/debug/pprof/` (loopback only, ssh tunnel: `ssh -L 6060:127.0.0.1:6060 root@154`)

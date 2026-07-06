# 56 Nginx + PostgreSQL 双活高可用最终方案

**版本**：v3.0（主从自动切换版）  
**创建时间**：2026-07-06  
**状态**：✅ 已调研完成，待评审

---

## 1. 需求回顾

用户的核心需求：

1. **71 与 184 负载均衡**（nginx 层）
2. **以前端特性固定链路**（sticky session）
3. **数据库主从自动同步**，故障时自动切换主从
4. **轮轮切换**：71 主 → 184 主 → 71 主 → ...

---

## 2. 现有架构调研结论

### 2.1 ✅ 已具备的基础

| 组件 | 现状 | 用途 |
|------|------|------|
| **`scripts/db-backup-71/init-pg-standby.sh`** | ✅ 已存在 | PG 流复制初始化（71→184 或反向） |
| **`scripts/db-backup-71/failover.sh`** | ✅ 已存在 | 71 standby → primary 提升 |
| **`scripts/db-backup-71/failback.sh`** | ✅ 已存在 | 184 primary → 71 standby 重建 |
| **`scripts/db-backup-71/health-check.sh`** | ✅ 已存在 | 监控主从延迟、健康状态 |
| **`scripts/db-backup-71/sync-llm-gateway-pg.sh`** | ✅ 已存在 | 业务表数据同步 |
| **71 PG 容器** | ✅ 已部署 | `pgvector:latest` + postgres 15（端口 5435） |

### 2.2 ✅ 已验证的复制能力

**当前部署**（基于 `init-pg-standby.sh`）：
- **PRIMARY**：184 k3s `llm-gateway-pg` Pod（hostNetwork 5432，14 个业务库）
- **STANDBY**：71 `pg-backup-184` 容器（端口 5433）
- **复制方式**：流复制（streaming replication），`pg_basebackup -Fp -Xs -P -R`

**关键配置**：
- `wal_receiver_timeout = 60s`
- `hot_standby = on`（71 可读）
- `hot_standby_feedback = on`（防主备 query 冲突）
- `max_standby_streaming_delay = 30s`

---

## 3. 目标架构设计

### 3.1 初始部署（71 主 / 184 从）

```
┌─────────────────────────────────────────────────────────────┐
│ 用户终端（Web / OpenCode / Cursor）                         │
└─────────────────────────────────────────────────────────────┘
                       ↓ HTTPS
┌─────────────────────────────────────────────────────────────┐
│ 56 nginx (14.103.169.56) — 负载均衡入口                    │
│   upstream llm-backend {                                    │
│     hash $sticky_key consistent;                            │
│     server 172.31.0.3:8781 max_fails=3 fail_timeout=15s;   │
│     server 172.31.0.4:10023 max_fails=3 fail_timeout=15s; │
│   }                                                         │
└─────────────────────────────────────────────────────────────┘
        │                              │
        ↓                              ↓
┌──────────────────┐          ┌──────────────────┐
│ 71 PRIMARY       │          │ 184 STANDBY      │
│ llm-gateway-go   │ ←────────│ llm-gateway-go   │
│ (172.31.0.3:8781)│   stream │ (172.31.0.4:10023)│
│                  │   repl   │                  │
│ 71 PostgreSQL    │──────────│ 184 PostgreSQL   │
│ (127.0.0.1:5432) │  ←─WAL──→│ (127.0.0.1:5432) │
│ 【PRIMARY】      │          │ 【STANDBY】      │
└──────────────────┘          └──────────────────┘
        │                              │
        └───── Redis (71 主/184 从) ───┘
```

### 3.2 故障转移后（184 主 / 71 从）

```
┌─────────────────────────────────────────────────────────────┐
│ 56 nginx — 检测到 71 DB 故障 → 流量全切到 184              │
│   (sticky 仍生效，因为每个 user 都有 sticky key)             │
└─────────────────────────────────────────────────────────────┘
        │                              │
        ↓                              ↓
┌──────────────────┐          ┌──────────────────┐
│ 71 STANDBY       │          │ 184 PRIMARY      │
│ llm-gateway-go   │ ←────────│ llm-gateway-go   │
│ (172.31.0.3:8781)│   stream │ (172.31.0.4:10023)│
│                  │   repl   │                  │
│ 71 PostgreSQL    │──────────│ 184 PostgreSQL   │
│ (127.0.0.1:5432) │  ←─WAL──→│ (127.0.0.1:5432) │
│ 【STANDBY】      │          │ 【PRIMARY】      │
└──────────────────┘          └──────────────────┘
```

---

## 4. 完整实施步骤

### 4.1 Phase 1: 初始化 71 主库（当前 184 → 71 数据迁移）

> ⚠️ **关键决策点**：用户希望"71 主、184 从"。需要先在 71 上准备好数据。

#### Step 1.1: 在 71 上准备 PostgreSQL

**当前状态**：
- 71 上已有 `llm-pg-71` 容器（pgvector + postgres，端口 5435）
- 这是一个独立运行的 PG，**不是从 184 复制的**

**问题**：要让 71 成为 PRIMARY，需要让 71 上有完整的 llm_gateway 业务数据。

**方案**：使用 `pg_basebackup` 从 184 拉取基础数据，然后在 71 上以 PRIMARY 模式运行。

```bash
# 在 71 上执行
ssh root@14.103.174.71

# 1. 停止当前 71 的 PG 容器
docker stop llm-pg-71

# 2. 备份当前数据（以防回滚）
mv /data/postgres-llm /data/postgres-llm.bak-$(date +%Y%m%d)

# 3. 准备新数据目录
mkdir -p /data/postgres-llm
chmod 700 /data/postgres-llm
chown 70:70 /data/postgres-llm

# 4. 从 184 pg_basebackup 拉取基础数据
docker run --rm \
  -v /data/postgres-llm:/var/lib/postgresql/data \
  -e PGPASSWORD="${REPL_PASSWORD}" \
  postgres:15-alpine \
  pg_basebackup \
    -h 172.31.0.4 \
    -p 5432 \
    -U repl_184_to_71_2026 \
    -D /var/lib/postgresql/data \
    -Fp -Xs -P -R
# 注意：-R 自动生成 standby.signal 和 postgresql.auto.conf
# 此时 71 是 standby 状态，需要 promote 成 primary

# 5. 修复权限
chown -R 70:70 /data/postgres-llm

# 6. 启动 71 PG（暂时还是 standby）
docker run -d \
  --name llm-pg-71 \
  --restart unless-stopped \
  -p 127.0.0.1:5432:5432 \
  -e POSTGRES_DB=llm_gateway \
  -e POSTGRES_USER=llm_gateway \
  -e POSTGRES_PASSWORD="${LLM_GATEWAY_DB_PASSWORD}" \
  -v /data/postgres-llm:/var/lib/postgresql/data \
  postgres:15-alpine
```

#### Step 1.2: Promote 71 成为 PRIMARY

```bash
# 在 71 上
docker exec -it llm-pg-71 psql -U llm_gateway -d llm_gateway -c "SELECT pg_promote();"

# 验证
docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c "SELECT pg_is_in_recovery();"
# 预期输出: f (false, 已提升)

docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c "SELECT pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn();"
```

#### Step 1.3: 71 上创建复制用户（给 184 用）

```bash
# 71 PG 是 PRIMARY 后，创建 replication 角色
docker exec -it llm-pg-71 psql -U llm_gateway -d llm_gateway <<SQL
CREATE USER repl_71_to_184_2026 WITH REPLICATION PASSWORD '${REPL_PASSWORD}';
-- pg_hba.conf 已经允许 172.31.0.0/20 复制
SQL
```

#### Step 1.4: 让 184 上的 PG 改为 71 的从库

**问题**：184 上的 PG 当前是 hostNetwork 5432 的 k3s pod。要让 184 改成 71 的从库，需要：

1. 停止 184 的 PG pod（业务会短暂中断）
2. 用 pg_basebackup 从 71 拉取数据
3. 配置 standby.signal + postgresql.auto.conf 指向 71
4. 启动 184 PG（standby 模式）

```bash
# 在 184 上
ssh root@14.103.112.184

# 1. 停止 k3s 上的 PG pod
kubectl -n pms-test scale deploy llm-gateway-pg --replicas=0

# 2. 备份当前数据
mv /data/pms-test/llm-gateway-pg /data/pms-test/llm-gateway-pg.bak-$(date +%Y%m%d)

# 3. 准备新数据目录
mkdir -p /data/pms-test/llm-gateway-pg
chmod 700 /data/pms-test/llm-gateway-pg

# 4. pg_basebackup 从 71 拉取
docker run --rm \
  -v /data/pms-test/llm-gateway-pg:/var/lib/postgresql/data \
  -e PGPASSWORD="${REPL_PASSWORD}" \
  postgres:15-alpine \
  pg_basebackup \
    -h 172.31.0.3 \
    -p 5432 \
    -U repl_71_to_184_2026 \
    -D /var/lib/postgresql/data \
    -Fp -Xs -P -R
# -R 会自动写入 standby.signal 和 primary_conninfo

# 5. 追加 standby 配置
cat >> /data/pms-test/llm-gateway-pg/postgresql.conf <<'EOF'

# === Standby Configuration ===
hot_standby = on
max_standby_streaming_delay = 30s
max_standby_archive_delay = 60s
wal_receiver_timeout = 60s
wal_receiver_status_interval = 10s
hot_standby_feedback = on
primary_conninfo = 'host=172.31.0.3 port=5432 user=repl_71_to_184_2026 password=${REPL_PASSWORD}'
EOF

# 6. 修复权限
chown -R 70:70 /data/pms-test/llm-gateway-pg

# 7. 启动 k3s pod
kubectl -n pms-test scale deploy llm-gateway-pg --replicas=1

# 8. 验证
sleep 30
kubectl -n pms-test exec deploy/llm-gateway-pg -- \
  psql -U llm_gateway -d llm_gateway -c "SELECT pg_is_in_recovery();"
# 预期: t (true, standby 模式)
```

### 4.2 Phase 2: 71 上部署 llm-gateway-go 指向本地主库

```bash
# 修改 71 上的 env
ssh root@14.103.174.71
chattr -i /etc/llm-gateway-go/env

# 改为指向本地主库
sed -i 's|@172.31.0.4:5432|@127.0.0.1:5432|g' /etc/llm-gateway-go/env
sed -i 's|@172.31.0.3:5432|@127.0.0.1:5432|g' /etc/llm-gateway-go/env

chattr +i /etc/llm-gateway-go/env

# 重启 llm-gateway-go
systemctl restart llm-gateway-go.service

# 验证
sleep 15
curl -s http://127.0.0.1:8781/healthz
```

### 4.3 Phase 3: 184 上部署 llm-gateway-go 指向本地从库

```bash
# 184 k8s 上的 llm-gateway-go-deployment
# 修改 secret 中的 LLM_GATEWAY_DATABASE_URL

# 184 上的 PG 改为 standby 后，端口仍是 5432 (hostNetwork)
# 但需要确保 184 上的 llm-gateway-go 指向本地 PG，而不是 71

# 当前 184 的 k8s 中，LLM_GATEWAY_DATABASE_URL 指向 llm-gateway-pg-svc:5432
# 这个 svc 指向本地 PG pod，所以保持不变

# 但要修改 standby 配置：让 184 PG 是只读 standby
# 这已经在 Step 1.4 完成
```

### 4.4 Phase 4: 配置 56 nginx 负载均衡

参考之前的设计方案 v2.0，配置多层级 sticky + 双后端。

---

## 5. 自动故障转移设计

### 5.1 三层监控体系

```
┌─────────────────────────────────────────────────────────────┐
│ Layer 1: 56 nginx 内置被动健康检查                           │
│   max_fails=3 fail_timeout=15s                                │
│   检测后端 llm-gateway-go 进程的 HTTP /healthz               │
└─────────────────────────────────────────────────────────────┘
            ↓ 触发条件：后端应用层失败
┌─────────────────────────────────────────────────────────────┐
│ Layer 2: 外部脚本主动探测（每分钟）                          │
│   /usr/local/bin/check-llm-health.sh                         │
│   - 探测 71:8781/healthz                                     │
│   - 探测 184:10023/healthz                                   │
│   - 探测 71/184 数据库状态                                   │
│   - 探测复制延迟                                              │
│   写入 /var/log/llm-health.log                               │
└─────────────────────────────────────────────────────────────┘
            ↓ 触发条件：DB 主库不可达
┌─────────────────────────────────────────────────────────────┐
│ Layer 3: PostgreSQL 主从切换守护进程                         │
│   脚本: /usr/local/bin/pg-failover-monitor.sh               │
│   监控对象: 当前 PRIMARY 的 PG 状态                          │
│   触发条件: 连续 5 次探测失败（30s 探测 1 次 = 2.5 分钟）   │
│   动作:                                                        │
│     1. promote standby 成为 new primary                     │
│     2. 更新 56 nginx upstream（通过 API 或 reload）         │
│     3. 重启 71 上 llm-gateway-go（指向新 primary）         │
│     4. 告警通知                                              │
└─────────────────────────────────────────────────────────────┘
```

### 5.2 故障检测脚本（每分钟运行）

**文件**：`/usr/local/bin/pg-failover-monitor.sh`

```bash
#!/bin/bash
# pg-failover-monitor.sh — 主库健康检查 + 自动切换
# 部署位置：71 和 184 各一份（双向监控）
# 运行方式：crontab 每分钟执行

set -euo pipefail

PRIMARY_HOST="${PRIMARY_HOST:-172.31.0.3}"  # 当前 PRIMARY (71)
PRIMARY_PORT="${PRIMARY_PORT:-5432}"
STANDBY_HOST="${STANDBY_HOST:-172.31.0.4}"   # 当前 STANDBY (184)
STANDBY_PORT="${STANDBY_PORT:-5432}"
STATE_FILE="/var/lib/pg-failover/state.json"
FAIL_COUNT_FILE="/var/lib/pg-failover/fail_count"
MAX_FAILS=5

# ── 状态文件管理 ──────────────────────────────────────────────
mkdir -p "$(dirname "$STATE_FILE")"
if [ ! -f "$STATE_FILE" ]; then
  cat > "$STATE_FILE" <<EOF
{
  "current_primary": "$PRIMARY_HOST",
  "current_standby": "$STANDBY_HOST",
  "last_switch_at": "never",
  "switch_count": 0
}
EOF
fi

# ── 检查 PRIMARY 健康 ─────────────────────────────────────────
check_primary() {
  local host="$1" port="$2"
  timeout 5 bash -c "echo > /dev/tcp/${host}/${port}" 2>/dev/null || return 1
  PGPASSWORD="${PRIMARY_DB_PASSWORD}" \
    pg_isready -h "$host" -p "$port" -U "llm_gateway" -d "llm_gateway" >/dev/null 2>&1 || return 1
  return 0
}

# ── 检查 STANDBY 复制状态 ─────────────────────────────────────
check_standby_replication() {
  local host="$1" port="$2"
  local lag
  lag=$(PGPASSWORD="${PRIMARY_DB_PASSWORD}" \
    psql -h "$host" -p "$port" -U "llm_gateway" -d "llm_gateway" -t -A -c \
    "SELECT EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))::int;" 2>/dev/null || echo "")
  if [ -z "$lag" ]; then
    return 1  # 不是 standby 或连接失败
  fi
  if [ "$lag" -gt 300 ]; then
    return 1  # 延迟过大
  fi
  return 0
}

# ── 主逻辑 ──────────────────────────────────────────────────────
if check_primary "$PRIMARY_HOST" "$PRIMARY_PORT"; then
  # PRIMARY 健康，重置 fail count
  echo "0" > "$FAIL_COUNT_FILE"
  log "$(date): PRIMARY ($PRIMARY_HOST) healthy"
  exit 0
fi

# PRIMARY 失败，累加 fail count
FAIL_COUNT=$(($(cat "$FAIL_COUNT_FILE" 2>/dev/null || echo "0") + 1))
echo "$FAIL_COUNT" > "$FAIL_COUNT_FILE"

log "$(date): PRIMARY ($PRIMARY_HOST) check failed #$FAIL_COUNT/$MAX_FAILS"

if [ "$FAIL_COUNT" -lt "$MAX_FAILS" ]; then
  log "Waiting for more failures before triggering failover..."
  exit 0
fi

# ── 触发自动故障转移 ──────────────────────────────────────────
log "=== FAILOVER TRIGGERED ==="
log "Switching from PRIMARY=$PRIMARY_HOST to STANDBY=$STANDBY_HOST"

# 1. 验证 STANDBY 健康
if ! check_standby_replication "$STANDBY_HOST" "$STANDBY_PORT"; then
  log "ERROR: STANDBY ($STANDBY_HOST) is not healthy, cannot failover"
  alert "PG_FAILOVER_FAILED: STANDBY unhealthy"
  exit 1
fi

# 2. Promote STANDBY
PGPASSWORD="${PRIMARY_DB_PASSWORD}" \
  psql -h "$STANDBY_HOST" -p "$STANDBY_PORT" -U "llm_gateway" -d "llm_gateway" \
  -c "SELECT pg_promote();"

# 3. 等待 promote 完成
for i in $(seq 1 30); do
  IS_PRIMARY=$(PGPASSWORD="${PRIMARY_DB_PASSWORD}" \
    psql -h "$STANDBY_HOST" -p "$STANDBY_PORT" -U "llm_gateway" -d "llm_gateway" \
    -t -A -c "SELECT pg_is_in_recovery();" 2>/dev/null || echo "t")
  if [ "$IS_PRIMARY" = "f" ]; then
    log "✅ $STANDBY_HOST promoted to PRIMARY"
    break
  fi
  log "Waiting for promote... ($i/30)"
  sleep 2
done

# 4. 更新 56 nginx (移除 old primary)
# 通过 consul-template 或 API 触发（简化方案：直接 reload nginx）
update_nginx_upstream() {
  # 调用 56 上的 reload 接口（如果部署了）
  curl -sf -X POST "http://14.103.169.56:9090/api/nginx/reload" || \
    log "WARN: nginx reload API not available, manual reload needed"
}

# 5. 通知所有 llm-gateway-go 实例更新 DB 指向
# 通过 systemd 重启 + 重新读取 env
restart_llm_gateway() {
  # 71 上的 llm-gateway-go 切换指向
  if [ "$STANDBY_HOST" = "172.31.0.3" ]; then
    # 71 提升为 primary, 重启 llm-gateway-go 仍然指向本地
    ssh root@172.31.0.3 "systemctl restart llm-gateway-go.service"
  fi
  # 184 上的 llm-gateway-go 重启（仍在本地指向）
  if [ "$STANDBY_HOST" = "172.31.0.4" ]; then
    # 184 已是 primary, 不需要重启
    :
  fi
}

# 6. 更新状态文件
NEW_STATE=$(jq -n \
  --arg cp "$STANDBY_HOST" \
  --arg cs "$PRIMARY_HOST" \
  --arg ts "$(date -Iseconds)" \
  --argjson sc "$(jq '.switch_count + 1' "$STATE_FILE")" \
  '{current_primary: $cp, current_standby: $cs, last_switch_at: $ts, switch_count: $sc}')

echo "$NEW_STATE" > "$STATE_FILE"

# 7. 告警
alert "PG_FAILOVER_COMPLETED: $PRIMARY_HOST → $STANDBY_HOST (switch #$SC)"

log "=== FAILOVER COMPLETE ==="
echo "0" > "$FAIL_COUNT_FILE"
```

### 5.3 简化版：手动半自动切换

如果觉得全自动风险太高，可以采用**半自动方案**：

1. **检测**：crontab 每分钟探测，失败时记录到日志 + 告警（不发自动切换）
2. **告警**：通过 webhook / 邮件 / Slack 通知 oncall
3. **人工决策**：oncall 确认故障后，手动执行 `failover.sh --confirm`
4. **自动恢复**：原 PRIMARY 恢复后，自动重建为 STANDBY（通过 `failback.sh`）

### 5.4 Redis 主从切换

**问题**：业务还依赖 Redis（如 session、缓存）。Redis 也需要主从切换。

**方案**：使用 Redis Sentinel 或简单的 `REPLICAOF` 切换。

```bash
# 在 71 上启动 Redis
docker run -d --name redis-71 -p 6379:6379 redis:7-alpine

# 在 184 上配置 Redis 为 71 的副本
docker exec redis-184 redis-cli REPLICAOF 172.31.0.3 6379

# 故障时切换
docker exec redis-184 redis-cli REPLICAOF NO ONE
```

**简化方案**：使用现有的 `scripts/db-backup-71/failover.sh` 中的 Redis 切换逻辑。

---

## 6. 故障恢复（failback）

当原 PRIMARY 恢复后，需要把它重建为新的 STANDBY：

### 6.1 自动 failback 脚本

**文件**：`/usr/local/bin/pg-failback.sh`

```bash
#!/bin/bash
# pg-failback.sh — 当旧 PRIMARY 恢复后，将其重建为 STANDBY
# 注意：这是破坏性的，会重新拉取数据

set -euo pipefail

OLD_PRIMARY="${1:?Usage: pg-failback.sh <old_primary_host> <new_primary_host>}"
NEW_PRIMARY="${2:?Usage: pg-failback.sh <old_primary_host> <new_primary_host>}"

REPL_USER="repl_${NEW_PRIMARY//./_}_to_${OLD_PRIMARY//./_}_2026"

log "=== Reinitializing $OLD_PRIMARY as STANDBY of $NEW_PRIMARY ==="

# 1. 停止 old primary 上的 PG
ssh root@$OLD_PRIMARY "docker stop llm-pg-71 || systemctl stop postgresql"

# 2. 清空数据目录
ssh root@$OLD_PRIMARY "rm -rf /data/postgres-llm/* && mkdir -p /data/postgres-llm && chmod 700 /data/postgres-llm"

# 3. pg_basebackup 从 new primary 拉取
ssh root@$OLD_PRIMARY <<EOF
docker run --rm \
  -v /data/postgres-llm:/var/lib/postgresql/data \
  -e PGPASSWORD="\${REPL_PASSWORD}" \
  postgres:15-alpine \
  pg_basebackup \
    -h $NEW_PRIMARY \
    -p 5432 \
    -U $REPL_USER \
    -D /var/lib/postgresql/data \
    -Fp -Xs -P -R
EOF

# 4. 追加 standby 配置
ssh root@$OLD_PRIMARY "cat >> /data/postgres-llm/postgresql.conf <<'CONF'
hot_standby = on
primary_conninfo = 'host=$NEW_PRIMARY port=5432 user=$REPL_USER password=${REPL_PASSWORD}'
CONF"

# 5. 修复权限并启动
ssh root@$OLD_PRIMARY "chown -R 70:70 /data/postgres-llm && docker start llm-pg-71"

# 6. 验证
sleep 15
ssh root@$OLD_PRIMARY "docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c 'SELECT pg_is_in_recovery();'"
# 预期: t (true, standby 模式)

log "=== Failback complete ==="
```

### 6.2 是否自动 failback？

**建议**：
- ❌ **不自动 failback**：避免反复切换造成数据丢失
- ✅ **告警通知 + 人工确认**：旧 PRIMARY 恢复后，告警通知 oncall
- ✅ **维护窗口执行**：业务低峰期手动执行 failback

---

## 7. 端到端验证清单

### 7.1 初始部署验证

- [ ] **71 PG 是 PRIMARY**：
  ```bash
  ssh root@14.103.174.71 "docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c 'SELECT pg_is_in_recovery();'"
  # 预期: f
  ```

- [ ] **184 PG 是 STANDBY**：
  ```bash
  ssh root@14.103.112.184 "kubectl -n pms-test exec deploy/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -c 'SELECT pg_is_in_recovery();'"
  # 预期: t
  ```

- [ ] **复制延迟 < 5s**：
  ```bash
  ssh root@14.103.112.184 "kubectl -n pms-test exec deploy/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -c \"SELECT EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))::int AS lag_s;\""
  # 预期: 0~5
  ```

- [ ] **71 上的 llm-gateway-go 指向本地主库**：
  ```bash
  curl -s http://172.31.0.3:8781/healthz
  # 预期: {"status":"ok"}
  ```

- [ ] **184 上的 llm-gateway-go 指向本地从库**：
  ```bash
  curl -s http://172.31.0.4:10023/healthz
  # 预期: {"status":"ok"}
  ```

- [ ] **56 nginx 双活**：
  ```bash
  # 用同一 sticky key 测试两次
  curl -H "X-Device-Seed: test-001" https://llm.kxpms.cn/healthz
  curl -H "X-Device-Seed: test-002" https://llm.kxpms.cn/healthz
  # 预期: 都 200，可能路由到不同节点
  ```

### 7.2 写操作验证（数据同步）

- [ ] **在 71 上插入测试数据**：
  ```bash
  ssh root@14.103.174.71 "docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c \
    \"INSERT INTO api_keys (key_prefix, encrypted_key, user_id, active) VALUES ('test-71', 'dummy', 1, true) RETURNING id;\""
  ```

- [ ] **在 184 上能读到该数据**（自动同步）：
  ```bash
  sleep 5
  ssh root@14.103.112.184 "kubectl -n pms-test exec deploy/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -c \
    \"SELECT id, key_prefix FROM api_keys WHERE key_prefix='test-71';\""
  # 预期: 能查到刚才插入的记录
  ```

### 7.3 故障转移演练

- [ ] **模拟 71 PG 故障**：
  ```bash
  ssh root@14.103.174.71 "docker stop llm-pg-71"
  ```

- [ ] **等待 5 分钟**（监控脚本每分钟探测 1 次，需 5 次失败才触发）

- [ ] **验证 184 自动 promote**：
  ```bash
  ssh root@14.103.112.184 "kubectl -n pms-test exec deploy/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -c 'SELECT pg_is_in_recovery();'"
  # 预期: f (已 promote 为 primary)
  ```

- [ ] **验证 71 上的 llm-gateway-go 自动切换**（指向 184）：
  ```bash
  # 71 上的 llm-gateway-go 需要修改 env 指向 184
  # 自动监控脚本应该已经修改了 /etc/llm-gateway-go/env 并重启服务
  ssh root@14.103.174.71 "systemctl status llm-gateway-go.service"
  ```

- [ ] **验证业务继续可用**：
  ```bash
  curl -H "X-Device-Seed: test-001" https://llm.kxpms.cn/healthz
  # 预期: 200
  ```

- [ ] **恢复 71 PG**：
  ```bash
  ssh root@14.103.174.71 "docker start llm-pg-71"
  # 此时 71 PG 是原始数据（不是 standby），需要手动 failback
  ```

- [ ] **执行 failback**（人工）：
  ```bash
  /usr/local/bin/pg-failback.sh 172.31.0.3 172.31.0.4
  ```

---

## 8. 风险评估与缓解

### 8.1 数据丢失风险

| 场景 | 风险 | 缓解 |
|------|------|------|
| **PRIMARY 故障时未同步的数据** | 中（流复制是异步） | `synchronous_standby_names = '*'` 改为同步复制（牺牲性能） |
| **切换瞬间的双写** | 低（切换是原子的） | 使用 `pg_promote()` 时会自动停止写入 |
| **脑裂（两边都是 PRIMARY）** | 低 | 56 nginx 健康检查会探测到其中一个不可用 |

### 8.2 性能影响

| 影响 | 说明 | 缓解 |
|------|------|------|
| **同步复制延迟** | 同步复制会增加写延迟 10-50ms | 默认使用异步，按需开启同步 |
| **从库读延迟** | hot_standby 读可能延迟 | 业务读请求路由到主库 |

### 8.3 切换风险

| 风险 | 说明 | 缓解 |
|------|------|------|
| **切换中数据丢失** | 复制延迟期间的数据 | `failover.sh` 会有日志，可手动评估 |
| **切换后应用未更新** | llm-gateway-go env 未更新指向新 primary | 自动监控脚本会触发 systemctl restart |
| **nginx 上游未更新** | 56 nginx 仍把请求路由到故障节点 | nginx `max_fails` 自动剔除 + 健康检查 cron |

---

## 9. 实施时间表

| 阶段 | 任务 | 预计时间 | 影响 |
|------|------|----------|------|
| **准备** | 备份所有数据 | 10 min | 无 |
| **Phase 1** | 71 改为 PRIMARY（pg_basebackup + promote） | 30 min | 业务短暂中断（5 min） |
| **Phase 2** | 184 改为 STANDBY | 30 min | 业务短暂中断（5 min） |
| **Phase 3** | 71 llm-gateway-go 指向本地 | 10 min | 71 短暂不可用 |
| **Phase 4** | 56 nginx 负载均衡配置 | 10 min | 无 |
| **Phase 5** | 监控脚本部署 | 20 min | 无 |
| **Phase 6** | 验证演练 | 30 min | 无 |
| **总耗时** | | **约 2.5 小时** | **总中断 < 15 min** |

---

## 10. 运维建议

### 10.1 监控指标

| 指标 | 告警阈值 | 检查命令 |
|------|----------|----------|
| **复制延迟** | > 60s | `SELECT EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))` |
| **WAL 接收状态** | down | `SELECT status FROM pg_stat_wal_receiver;` |
| **PRIMARY 健康** | 连续 5 次失败 | crontab + 自定义脚本 |
| **磁盘空间** | > 80% | `df -h /data/postgres-llm` |
| **连接数** | > 800 (max 1000) | `SELECT count(*) FROM pg_stat_activity;` |

### 10.2 日常维护

1. **每日**：检查复制延迟和日志
2. **每周**：手动触发一次故障演练（开发环境）
3. **每月**：备份验证 + 文档更新
4. **每季度**：完整的切换演练 + 性能评估

---

## 11. 决策点（请确认）

请回答以下问题，以便最终确定方案：

1. **切换自动化程度**：
   - 选项 A：全自动切换（检测到故障 → 自动 promote → 自动重启应用）
   - 选项 B：半自动（检测到故障 → 告警 → 人工确认 → 自动切换）
   - 选项 C：手动（检测到故障 → 告警 → 人工全程操作）

2. **复制模式**：
   - 选项 A：异步复制（默认，性能好，可能丢数据）
   - 选项 B：同步复制（数据 0 丢失，性能略差）

3. **Redis 主从**：
   - 选项 A：Redis 也跟随 PG 主从切换（完整切换）
   - 选项 B：Redis 独立主从，不跟随 PG（简化）

4. **切换触发时间**：
   - 选项 A：1 分钟（连续 1 次失败就触发）
   - 选项 B：5 分钟（默认推荐）
   - 选项 C：30 分钟（保守）

---

**下一步**：请确认方案 + 回答决策点 + 是否开始实施。
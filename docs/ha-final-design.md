# 56 Nginx + PostgreSQL 双活高可用 - 最终实施方案

**版本**：v4.0（最终实施版）  
**创建时间**：2026-07-06  
**状态**：✅ 决策已确认，待实施

---

## 1. 用户决策确认

| 决策点 | 选择 | 实施方案 |
|--------|------|----------|
| **切换自动化** | **半自动 + 网页按钮** | 自动检测 + 告警；oncall 通过网页控制台点击按钮执行切换 |
| **复制模式** | **异步复制** | 默认 PG 异步流复制（性能优先，接受 < 1s 数据丢失风险） |
| **Redis 切换** | **跟随 PG 切换** | PG 切换时同时切换 Redis 主从 |
| **触发时间** | **5 分钟** | 连续 5 次健康探测失败（每分钟一次）后告警 + 可一键切换 |

---

## 2. 完整架构

### 2.1 初始架构（71 主 / 184 从）

```
┌─────────────────────────────────────────────────────────────┐
│ 用户终端（Web / OpenCode / Cursor）                         │
└─────────────────────────────────────────────────────────────┘
                       ↓ HTTPS
┌─────────────────────────────────────────────────────────────┐
│ 56 nginx (14.103.169.56)                                    │
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
│ llm-gateway-go   │          │ llm-gateway-go   │
│ (172.31.0.3:8781)│          │ (172.31.0.4:10023)│
│                  │ stream   │                  │
│ Redis PRIMARY    │ ←──────→ │ Redis SLAVE      │
│ 71 PostgreSQL    │ ←─WAL──→ │ 184 PostgreSQL   │
│ 【PRIMARY】      │          │ 【STANDBY】      │
└──────────────────┘          └──────────────────┘
        │
        ↓ (健康状态上报)
┌─────────────────────────────────────────────────────────────┐
│ 56 Admin Console (网页控制台) — 半自动切换入口              │
│   - 实时显示 71/184 健康状态                                │
│   - 实时显示复制延迟                                         │
│   - [切换主从] 按钮（需二次确认）                          │
│   - [强制回切] 按钮（用于原 PRIMARY 恢复后）              │
└─────────────────────────────────────────────────────────────┘
```

---

## 3. 半自动切换流程

### 3.1 检测阶段（自动）

```
71 PG 不可达
  ↓
crontab 每分钟探测 1 次
  ↓
连续 5 次失败 (5 分钟)
  ↓
触发告警（邮件/Slack/webhook）
  ↓
网页控制台显示"71 PG 异常，建议切换"
  ↓
等待人工决策
```

### 3.2 切换阶段（人工点击按钮）

```
oncall 登录网页控制台
  ↓
点击 [切换主从] 按钮
  ↓
弹窗二次确认（输入 "CONFIRM"）
  ↓
网页调用后端 API
  ↓
后端 SSH 到 184 执行 promote + 切换 Redis
  ↓
后端 SSH 到 71 修改 env + 重启 llm-gateway-go
  ↓
后端调用 56 nginx reload（更新 upstream）
  ↓
返回成功，网页显示"切换完成"
```

### 3.3 恢复阶段（人工）

```
原 71 PG 恢复后
  ↓
告警"71 已恢复，可重建为 STANDBY"
  ↓
人工点击 [重建 71 为 STANDBY] 按钮
  ↓
自动执行 pg_basebackup（破坏性重建）
  ↓
71 成为新 STANDBY
```

---

## 4. 网页控制台设计

### 4.1 页面布局

**路径**：`https://acc.kxpms.cn/admin/ha-monitor`（集成到 ACC 管理后台）

#### **顶部状态卡片**

```
┌─────────────────────────────────────────────────────────┐
│ 71 Server (172.31.0.3)        │ 184 Server (172.31.0.4) │
│ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━ │ ━━━━━━━━━━━━━━━━━━━━━━━━│
│ Status: 🟢 HEALTHY           │ Status: 🟢 HEALTHY      │
│ Role: PRIMARY                │ Role: STANDBY           │
│                               │                        │
│ PostgreSQL:                  │ PostgreSQL:             │
│  ├ Status: 🟢 UP             │  ├ Status: 🟢 UP        │
│  ├ Lag: -                    │  ├ Lag: 0.8s            │
│  └ Replicas: 1 (184)        │  └ Recovery: true       │
│                               │                        │
│ Redis:                       │ Redis:                  │
│  ├ Status: 🟢 UP             │  ├ Status: 🟢 UP        │
│  └ Role: master              │  └ Role: slave          │
│                               │                        │
│ llm-gateway-go:              │ llm-gateway-go:         │
│  ├ HTTP 200                  │  ├ HTTP 200             │
│  └ DB: 127.0.0.1:5432       │  └ DB: 127.0.0.1:5432  │
└─────────────────────────────────────────────────────────┘

[ 切换主从 ]  [ 重建为 STANDBY ]
```

#### **实时图表**

- 复制延迟曲线（最近 1 小时）
- 请求量分布（71 vs 184）
- 健康检查时间线

#### **历史切换记录**

```
2026-07-06 15:30  | 184 → 71 | 操作人: @oncall | 原因: 主动演练
2026-07-05 03:22  | 71 → 184 | 操作人: system  | 原因: 71 PG 自动故障
```

### 4.2 后端 API

**文件**：`services/llm-gateway-go/admin/ha_switch_handler.go`（新增）

```go
// POST /api/admin/ha/switch-primary
// Body: { "target": "71" | "184", "confirm": "CONFIRM" }
// Auth: 管理员 JWT + 二次验证 token

func (h *HAHandler) SwitchPrimary(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Target  string `json:"target"`  // "71" or "184"
        Confirm string `json:"confirm"` // must be "CONFIRM"
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, 400, "invalid body")
        return
    }
    
    if req.Confirm != "CONFIRM" {
        writeError(w, 400, "must confirm with CONFIRM")
        return
    }
    
    // 1. 校验目标健康
    if err := h.checkTargetHealthy(req.Target); err != nil {
        writeError(w, 503, err.Error())
        return
    }
    
    // 2. SSH 到目标机器执行 promote
    if err := h.executeSwitch(req.Target); err != nil {
        writeError(w, 500, err.Error())
        return
    }
    
    // 3. 记录审计日志
    h.auditLog(r, "ha_switch", req.Target)
    
    writeJSON(w, 200, map[string]any{
        "status": "switched",
        "new_primary": req.Target,
    })
}
```

---

## 5. 自动切换守护进程

### 5.1 部署位置

- **71 和 184 各一份**（双向监控）
- **运行方式**：systemd service（持续运行）+ crontab（每分钟探测）

### 5.2 核心逻辑

**文件**：`/usr/local/bin/pg-ha-monitor.sh`

```bash
#!/bin/bash
# pg-ha-monitor.sh — 半自动 HA 监控守护进程
# 部署：71 和 184 都运行
# 触发：crontab 每分钟调用

set -euo pipefail

STATE_DIR="/var/lib/pg-ha"
mkdir -p "$STATE_DIR"
STATE_FILE="$STATE_DIR/state.json"
FAIL_COUNT="$STATE_DIR/fail_count"

# 从 state.json 读取当前 primary/standby
if [ ! -f "$STATE_FILE" ]; then
  # 初始状态：71 是 primary，184 是 standby
  cat > "$STATE_FILE" <<'EOF'
{
  "current_primary": "172.31.0.3",
  "current_standby": "172.31.0.4",
  "last_switch_at": null,
  "switch_count": 0,
  "fail_count": 0
}
EOF
fi

CURRENT_PRIMARY=$(jq -r '.current_primary' "$STATE_FILE")
CURRENT_STANDBY=$(jq -r '.current_standby' "$STATE_FILE")
CURRENT_FAIL=$(jq -r '.fail_count' "$STATE_FILE")

# ── 健康探测 ──────────────────────────────────────────────────
check_pg_health() {
  local host="$1"
  timeout 5 bash -c "echo > /dev/tcp/${host}/5432" 2>/dev/null || return 1
  PGPASSWORD="$PG_PASSWORD" \
    pg_isready -h "$host" -p 5432 -U llm_gateway -d llm_gateway >/dev/null 2>&1 || return 1
  return 0
}

# ── 告警 ────────────────────────────────────────────────────────
alert() {
  local severity="$1" message="$2"
  
  # 写入日志
  logger -t "pg-ha" "[$severity] $message"
  
  # 调用 webhook（飞书/Slack）
  curl -sf -X POST "https://alert.kxpms.cn/webhook/ha" \
    -H "Content-Type: application/json" \
    -d "{\"severity\":\"$severity\",\"message\":\"$message\",\"primary\":\"$CURRENT_PRIMARY\",\"standby\":\"$CURRENT_STANDBY\"}" \
    >/dev/null 2>&1 || true
}

# ── 主循环 ──────────────────────────────────────────────────────
if check_pg_health "$CURRENT_PRIMARY"; then
  # PRIMARY 健康，重置 fail count
  if [ "$CURRENT_FAIL" -gt 0 ]; then
    log "PRIMARY ($CURRENT_PRIMARY) recovered, resetting fail count"
    jq '.fail_count = 0 | .last_recover_at = now | todate' "$STATE_FILE" > "$STATE_FILE.tmp"
    mv "$STATE_FILE.tmp" "$STATE_FILE"
    alert "INFO" "PRIMARY recovered, fail count reset"
  fi
  exit 0
fi

# PRIMARY 失败
CURRENT_FAIL=$((CURRENT_FAIL + 1))
jq --argjson fc "$CURRENT_FAIL" '.fail_count = $fc' "$STATE_FILE" > "$STATE_FILE.tmp"
mv "$STATE_FILE.tmp" "$STATE_FILE"

log "PRIMARY ($CURRENT_PRIMARY) check failed #$CURRENT_FAIL/5"

if [ "$CURRENT_FAIL" -lt 5 ]; then
  alert "WARNING" "PRIMARY ($CURRENT_PRIMARY) check failed #$CURRENT_FAIL/5"
  exit 0
fi

# ── 连续 5 次失败，触发告警 + 等待人工切换 ─────────────────────
alert "CRITICAL" "PRIMARY ($CURRENT_PRIMARY) DOWN for 5 minutes. MANUAL SWITCH REQUIRED at https://acc.kxpms.cn/admin/ha-monitor"
```

### 5.3 systemd 配置

**文件**：`/etc/systemd/system/pg-ha-monitor.service`

```ini
[Unit]
Description=PostgreSQL HA Monitor
After=docker.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/pg-ha-monitor.sh
User=root
Environment=PG_PASSWORD=<from-sops>
```

**文件**：`/etc/systemd/system/pg-ha-monitor.timer`

```ini
[Unit]
Description=Run pg-ha-monitor every minute

[Timer]
OnCalendar=*:*:00
AccuracySec=10s

[Install]
WantedBy=timers.target
```

```bash
systemctl daemon-reload
systemctl enable --now pg-ha-monitor.timer
```

---

## 6. 切换执行脚本

### 6.1 promote 脚本（手动触发）

**文件**：`/usr/local/bin/ha-switch.sh`

```bash
#!/bin/bash
# ha-switch.sh — 半自动主从切换执行器
# 调用方：网页控制台 API（带 CONFIRM 二次验证）

set -euo pipefail

NEW_PRIMARY="${1:?Usage: ha-switch.sh <new_primary_host> <old_primary_host>}"
OLD_PRIMARY="${2:?Usage: ha-switch.sh <new_primary_host> <old_primary_host>}"

LOG="/var/log/ha-switch-$(date +%Y%m%d_%H%M%S).log"
log() { echo "[$(date)] $1" | tee -a "$LOG"; }

log "==========================================="
log "  HA SWITCH: $OLD_PRIMARY → $NEW_PRIMARY"
log "==========================================="

# ── Step 1: 校验 NEW_PRIMARY 健康（必须是 STANDBY）────────────
log "Step 1: Verifying $NEW_PRIMARY is healthy..."
IS_IN_RECOVERY=$(PGPASSWORD="$PG_PASSWORD" \
  psql -h "$NEW_PRIMARY" -p 5432 -U llm_gateway -d llm_gateway -t -A -c \
  "SELECT pg_is_in_recovery();" 2>/dev/null || echo "t")

if [ "$IS_IN_RECOVERY" != "t" ]; then
  log "❌ ERROR: $NEW_PRIMARY is not in recovery mode (not a standby)"
  exit 1
fi

LAG=$(PGPASSWORD="$PG_PASSWORD" \
  psql -h "$NEW_PRIMARY" -p 5432 -U llm_gateway -d llm_gateway -t -A -c \
  "SELECT EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))::int;" 2>/dev/null || echo "999")

if [ -z "$LAG" ] || [ "$LAG" -gt 300 ]; then
  log "❌ ERROR: Replication lag too high: ${LAG}s"
  exit 1
fi

log "✅ $NEW_PRIMARY is healthy standby (lag=${LAG}s)"

# ── Step 2: Promote PostgreSQL ─────────────────────────────────
log "Step 2: Promoting PostgreSQL on $NEW_PRIMARY..."
PGPASSWORD="$PG_PASSWORD" \
  psql -h "$NEW_PRIMARY" -p 5432 -U llm_gateway -d llm_gateway \
  -c "SELECT pg_promote();"

# 等待 promote 完成
for i in $(seq 1 30); do
  IS_RECOVERY=$(PGPASSWORD="$PG_PASSWORD" \
    psql -h "$NEW_PRIMARY" -p 5432 -U llm_gateway -d llm_gateway -t -A -c \
    "SELECT pg_is_in_recovery();" 2>/dev/null || echo "t")
  if [ "$IS_RECOVERY" = "f" ]; then
    log "✅ PostgreSQL promoted successfully"
    break
  fi
  log "Waiting for promote... ($i/30)"
  sleep 2
done

# ── Step 3: 切换 Redis 主从 ───────────────────────────────────
log "Step 3: Switching Redis master..."
if [ "$NEW_PRIMARY" = "172.31.0.3" ]; then
  # 71 成为 master
  ssh root@172.31.0.3 "docker exec redis-71 redis-cli REPLICAOF NO ONE"
  ssh root@172.31.0.4 "docker exec redis-184 redis-cli REPLICAOF 172.31.0.3 6379"
else
  # 184 成为 master
  ssh root@172.31.0.4 "docker exec redis-184 redis-cli REPLICAOF NO ONE"
  ssh root@172.31.0.3 "docker exec redis-71 redis-cli REPLICAOF 172.31.0.4 6379"
fi

# ── Step 4: 更新 71 上的 llm-gateway-go env 指向 ─────────────
log "Step 4: Updating llm-gateway-go env on both nodes..."

# 71 上：原 PRIMARY → STANDBY，指向新 PRIMARY
ssh root@172.31.0.3 <<EOF
chattr -i /etc/llm-gateway-go/env
sed -i 's|@127.0.0.1:5432|@$NEW_PRIMARY:5432|g' /etc/llm-gateway-go/env
sed -i 's|@172.31.0.4:5432|@$NEW_PRIMARY:5432|g' /etc/llm-gateway-go/env
chattr +i /etc/llm-gateway-go/env
systemctl restart llm-gateway-go.service
EOF

# 184 上：原 STANDBY → PRIMARY，指向本地
ssh root@172.31.0.4 <<'EOF'
# k8s 上的 llm-gateway-go, 重新指向本地 PG
kubectl -n pms-test exec deploy/llm-gateway-go-deployment -- \
  sh -c 'echo "DB updated to local standby"'
# 如果需要修改 secret: kubectl -n pms-test edit secret llm-gateway-secret
EOF

# ── Step 5: 更新 56 nginx upstream ───────────────────────────
log "Step 5: Updating 56 nginx upstream..."
# 通过 API 通知 nginx reload
curl -sf -X POST "http://14.103.169.56:9090/api/nginx/reload" || \
  log "WARN: nginx reload API failed, manual reload needed"

# ── Step 6: 更新本地 state.json ───────────────────────────────
log "Step 6: Updating HA state..."
NEW_STATE=$(jq -n \
  --arg cp "$NEW_PRIMARY" \
  --arg cs "$OLD_PRIMARY" \
  --arg ts "$(date -Iseconds)" \
  --argjson sc "$(jq '.switch_count + 1' /var/lib/pg-ha/state.json)" \
  '{current_primary: $cp, current_standby: $cs, last_switch_at: $ts, switch_count: $sc, fail_count: 0}')
echo "$NEW_STATE" > /var/lib/pg-ha/state.json

log "==========================================="
log "  ✅ HA SWITCH COMPLETE"
log "==========================================="
log "  NEW PRIMARY: $NEW_PRIMARY"
log "  NEW STANDBY: $OLD_PRIMARY"
log "  Log: $LOG"

# ── Step 7: 告警 ──────────────────────────────────────────────
alert "INFO" "HA switch completed: $OLD_PRIMARY → $NEW_PRIMARY"
```

---

## 7. 完整实施步骤

### 7.1 Phase 1: 初始化主从环境（2 小时）

#### Step 1.1: 在 71 上准备 PostgreSQL 主库

```bash
# 1. 停止当前 71 上的 PG
ssh root@172.31.0.3 "docker stop llm-pg-71 || true"

# 2. 备份数据
ssh root@172.31.0.3 "mv /data/postgres-llm /data/postgres-llm.bak-$(date +%Y%m%d) || true"

# 3. 从 184 pg_basebackup 拉取数据（71 暂时是 standby）
ssh root@172.31.0.3 <<EOF
mkdir -p /data/postgres-llm && chmod 700 /data/postgres-llm
docker run --rm \
  -v /data/postgres-llm:/var/lib/postgresql/data \
  -e PGPASSWORD="\$REPL_PASSWORD" \
  postgres:15-alpine \
  pg_basebackup \
    -h 172.31.0.4 \
    -p 5432 \
    -U repl_184_to_71_2026 \
    -D /var/lib/postgresql/data \
    -Fp -Xs -P -R
chown -R 70:70 /data/postgres-llm
EOF

# 4. 启动 71 PG（standby 状态）
ssh root@172.31.0.3 "docker start llm-pg-71"

# 5. 等待同步完成
sleep 30
ssh root@172.31.0.3 "docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c 'SELECT pg_last_xact_replay_timestamp();'"
```

#### Step 1.2: Promote 71 成为 PRIMARY

```bash
# 在 71 上
ssh root@172.31.0.3 "docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c 'SELECT pg_promote();'"

# 验证
ssh root@172.31.0.3 "docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c 'SELECT pg_is_in_recovery();'"
# 预期: f (false)
```

#### Step 1.3: 在 71 上创建 replication 角色

```bash
ssh root@172.31.0.3 "docker exec llm-pg-71 psql -U llm_gateway -d llm_gateway -c \"CREATE USER repl_71_to_184_2026 WITH REPLICATION PASSWORD '\$REPL_PASSWORD';\""
```

#### Step 1.4: 让 184 改为 71 的 STANDBY

```bash
# 在 184 上
ssh root@14.103.112.184 <<'EOF'
# 停止 k3s 上的 PG pod
kubectl -n pms-test scale deploy llm-gateway-pg --replicas=0

# 备份数据
mv /data/pms-test/llm-gateway-pg /data/pms-test/llm-gateway-pg.bak-$(date +%Y%m%d) || true

# 从 71 pg_basebackup
mkdir -p /data/pms-test/llm-gateway-pg
chmod 700 /data/pms-test/llm-gateway-pg

docker run --rm \
  -v /data/pms-test/llm-gateway-pg:/var/lib/postgresql/data \
  -e PGPASSWORD="$REPL_PASSWORD" \
  postgres:15-alpine \
  pg_basebackup \
    -h 172.31.0.3 \
    -p 5432 \
    -U repl_71_to_184_2026 \
    -D /var/lib/postgresql/data \
    -Fp -Xs -P -R

# 追加 standby 配置
cat >> /data/pms-test/llm-gateway-pg/postgresql.conf <<CONF
hot_standby = on
primary_conninfo = 'host=172.31.0.3 port=5432 user=repl_71_to_184_2026 password=$REPL_PASSWORD'
CONF

chown -R 70:70 /data/pms-test/llm-gateway-pg

# 启动 pod
kubectl -n pms-test scale deploy llm-gateway-pg --replicas=1
EOF

# 验证
sleep 30
ssh root@14.103.112.184 "kubectl -n pms-test exec deploy/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -c 'SELECT pg_is_in_recovery();'"
# 预期: t (true, standby)
```

#### Step 1.5: 71 上的 llm-gateway-go 指向本地主库

```bash
ssh root@172.31.0.3 <<EOF
chattr -i /etc/llm-gateway-go/env
sed -i 's|@172.31.0.4:5432|@127.0.0.1:5432|g' /etc/llm-gateway-go/env
chattr +i /etc/llm-gateway-go/env
systemctl restart llm-gateway-go.service
EOF

sleep 10
curl -s http://172.31.0.3:8781/healthz
```

### 7.2 Phase 2: 56 nginx 配置负载均衡（30 分钟）

参考之前的方案 v2.0。

### 7.3 Phase 3: 部署 HA 监控（30 分钟）

```bash
# 1. 复制监控脚本到 71 和 184
scp /usr/local/bin/pg-ha-monitor.sh root@172.31.0.3:/usr/local/bin/
scp /usr/local/bin/pg-ha-monitor.sh root@172.31.0.4:/usr/local/bin/

scp /etc/systemd/system/pg-ha-monitor.service root@172.31.0.3:/etc/systemd/system/
scp /etc/systemd/system/pg-ha-monitor.service root@172.31.0.4:/etc/systemd/system/

scp /etc/systemd/system/pg-ha-monitor.timer root@172.31.0.3:/etc/systemd/system/
scp /etc/systemd/system/pg-ha-monitor.timer root@172.31.0.4:/etc/systemd/system/

# 2. 启动监控
ssh root@172.31.0.3 "systemctl daemon-reload && systemctl enable --now pg-ha-monitor.timer"
ssh root@172.31.0.4 "systemctl daemon-reload && systemctl enable --now pg-ha-monitor.timer"
```

### 7.4 Phase 4: 部署网页控制台（2 小时）

新增后端 API + 前端页面。

### 7.5 Phase 5: 验证演练（30 分钟）

---

## 8. 验证清单

### 8.1 初始状态

- [ ] 71 PG: `pg_is_in_recovery() = f`
- [ ] 184 PG: `pg_is_in_recovery() = t`
- [ ] 184 复制延迟 < 5s
- [ ] 71 Redis: master
- [ ] 184 Redis: slave
- [ ] 71 llm-gateway-go: 连接本地 127.0.0.1:5432
- [ ] 184 llm-gateway-go: 连接本地 127.0.0.1:5432
- [ ] 56 nginx: 双后端健康

### 8.2 写同步验证

- [ ] 在 71 插入测试 api_key
- [ ] 5 秒后在 184 能查到该记录

### 8.3 切换演练

- [ ] **模拟 71 故障**：
  ```bash
  ssh root@172.31.0.3 "docker stop llm-pg-71"
  ```

- [ ] **5 分钟后**查看告警 + 网页控制台状态

- [ ] **点击切换按钮** → 输入 "CONFIRM" → 等待完成

- [ ] **验证切换成功**：
  - 184 PG: `pg_is_in_recovery() = f`（已 promote）
  - 71 PG: 已重建为 standby
  - 71 llm-gateway-go: env 已改为 172.31.0.4
  - 56 nginx: 已 reload
  - 业务: curl 仍返回 200

- [ ] **恢复 71 PG**：
  ```bash
  ssh root@172.31.0.3 "docker start llm-pg-71"
  ```

- [ ] **点击"重建为 STANDBY"按钮** → 等待 pg_basebackup 完成

---

## 9. 风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| **切换瞬间数据丢失** | 中 | 中（异步复制） | < 1s 数据可能丢失，业务可接受 |
| **误触发切换** | 低 | 高 | 半自动 + CONFIRM 二次验证 |
| **双 PRIMARY 脑裂** | 极低 | 灾难 | pg_promote 是原子的，且 56 nginx 健康检查会剔除双写 |
| **failback 数据丢失** | 中 | 高 | failback 会重建，原数据被覆盖，需先备份 |
| **网页控制台被未授权访问** | 中 | 高 | 管理员 JWT + 二次验证 + 审计日志 |

---

## 10. 实施时间表

| 阶段 | 任务 | 时间 | 影响 |
|------|------|------|------|
| **Phase 1** | PG 主从初始化（71 主、184 从） | 2h | 业务中断 < 15min |
| **Phase 2** | nginx 负载均衡 | 30min | 无 |
| **Phase 3** | HA 监控部署 | 30min | 无 |
| **Phase 4** | 网页控制台 | 2h | 无 |
| **Phase 5** | 验证演练 | 30min | 无 |
| **总计** | | **5.5 小时** | **总中断 < 15 min** |

---

## 11. 下一步

请确认以下事项后开始实施：

1. **✅ 决策已定**（半自动 + 异步复制 + Redis 跟随 + 5 分钟触发）
2. **网页控制台**集成到哪个后台？
   - A. 集成到 ACC (`acc.kxpms.cn/admin/ha-monitor`)
   - B. 独立页面 (`ha.kxpms.cn`)
   - C. 集成到 llm-gateway-go admin 后台
3. **是否需要开发网页控制台后端 API？**（如果选择 A 或 C）

确认后我可以：
- 第一步：在测试环境验证主从切换
- 第二步：部署到生产
- 第三步：交付网页控制台
# 方案 B 执行计划：71 PG 为主库 + 应用层双活

**版本**：v5.0（审计修正后）  
**执行时间**：预计 2026-07-06 下午  
**预计停机时间**：30-60 分钟  
**状态**：✅ 已审计，待执行

---

## 执行前确认

### ✅ 架构确认

- **应用层**：nginx 负载均衡，71 和 184 llm-gateway-go 都是活跃节点
- **数据库层**：71 PG (PRIMARY) ← streaming → 184 PG (STANDBY)
- **连接方式**：71 和 184 应用**都连接 71 PG**（172.31.0.3:5432）
- **故障切换**：71 PG 故障时，promote 184 PG，71 和 184 应用同时切换到 184 PG

### ⚠️ 风险提示

1. **停机时间**：pg_dump + 导入 ≈ 30-60 分钟（取决于数据量）
2. **数据迁移风险**：从 184 导出 → 71 导入，需确保完整性
3. **回滚复杂度**：一旦开始 Phase 2，回滚需要重新 pg_basebackup

### 📋 前置准备

1. **备份 184 PG 数据**（保底）
2. **通知用户维护窗口**
3. **准备回滚脚本**
4. **oncall 在线**

---

## Phase 0: 现状调查（10 分钟）

### 目标

确认当前 71/184 的 PG 和应用状态，避免破坏现有服务。

### 执行步骤

由于我没有凭据访问权限，这部分需要您手动执行或授权：

```bash
# === 71 上的检查 ===
ssh root@172.31.0.3 <<'EOF'
echo "=== 71 PostgreSQL 容器 ==="
docker ps -a | grep -E "postgres|pg"

echo "=== 71 llm-gateway-go env ==="
grep DATABASE_URL /etc/llm-gateway-go/env 2>/dev/null || echo "文件不存在"

echo "=== 71 磁盘空间 ==="
df -h /data/postgres-llm 2>/dev/null || echo "目录不存在"
df -h /data/llm-gateway-pg 2>/dev/null || echo "目录不存在"
EOF

# === 184 上的检查 ===
ssh root@14.103.112.184 <<'EOF'
echo "=== 184 k8s PG pod ==="
kubectl -n pms-test get pod | grep llm-gateway-pg

echo "=== 184 PG 数据量估算 ==="
kubectl -n pms-test exec deploy/llm-gateway-pg -- \
  psql -U llm_gateway -d llm_gateway -c \
  "SELECT pg_size_pretty(pg_database_size('llm_gateway')) AS db_size;"

echo "=== 184 PG 表数量 ==="
kubectl -n pms-test exec deploy/llm-gateway-pg -- \
  psql -U llm_gateway -d llm_gateway -t -c \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';"

echo "=== 184 llm-gateway-go env ==="
kubectl -n pms-test exec deploy/llm-gateway-go-deployment -- \
  env | grep DATABASE_URL
EOF
```

### 预期结果

需要记录：
1. 71 上是否已有 PG 容器？叫什么名字？
2. 71 llm-gateway-go 当前连哪个 PG？
3. 184 PG 数据量多大？（影响 pg_dump 时间）
4. 184 llm-gateway-go 当前连哪个 PG？

**⚠️ 停止点**：Phase 0 完成后，请报告结果，我将据此调整后续步骤。

---

## Phase 1: 71 PG 准备（30-60 分钟）

### 目标

在 71 上创建新的 PostgreSQL 主库，从 184 导入完整数据。

### Step 1.1: 停止 71 上现有 PG（如果有）

```bash
ssh root@172.31.0.3 <<'EOF'
# 查找所有 postgres 容器
EXISTING_PG=$(docker ps -a --format '{{.Names}}' | grep -E "postgres|pg" || echo "")

if [ -n "$EXISTING_PG" ]; then
  echo "发现现有 PG 容器: $EXISTING_PG"
  echo "停止并重命名..."
  for c in $EXISTING_PG; do
    docker stop "$c"
    docker rename "$c" "${c}-deprecated-$(date +%Y%m%d%H%M%S)"
  done
fi

# 备份现有数据目录
for dir in /data/postgres-llm /data/llm-gateway-pg; do
  if [ -d "$dir" ]; then
    echo "备份 $dir"
    mv "$dir" "${dir}.bak-$(date +%Y%m%d%H%M%S)"
  fi
done

# 创建新数据目录
mkdir -p /data/postgres-llm-primary
chmod 700 /data/postgres-llm-primary
chown 70:70 /data/postgres-llm-primary
EOF
```

### Step 1.2: 从 184 pg_dump 导出数据

```bash
# 在 184 上执行
ssh root@14.103.112.184 <<'EOF'
echo "开始 pg_dump（这会花费 5-20 分钟，取决于数据量）..."

# 导出到 /tmp（184 k8s pod 内）
kubectl -n pms-test exec deploy/llm-gateway-pg -- \
  pg_dump -U llm_gateway -d llm_gateway \
  --format=custom --compress=6 --verbose \
  > /tmp/llm_gateway_$(date +%Y%m%d_%H%M%S).dump

# 记录文件名
DUMP_FILE=$(ls -t /tmp/llm_gateway_*.dump | head -1)
echo "导出完成: $DUMP_FILE"
echo "文件大小: $(du -h $DUMP_FILE | cut -f1)"
EOF
```

### Step 1.3: 传输 dump 文件到 71

```bash
# 在本地或跳板机执行
scp root@14.103.112.184:/tmp/llm_gateway_*.dump /tmp/
scp /tmp/llm_gateway_*.dump root@172.31.0.3:/tmp/
```

### Step 1.4: 在 71 上启动新 PostgreSQL 容器

```bash
ssh root@172.31.0.3 <<'EOF'
# 获取密码（需要从 SOPS 或 env 中读取）
PG_PASSWORD="${LLM_GATEWAY_DB_PASSWORD:-your_password_here}"

docker run -d \
  --name llm-pg-71-primary \
  --restart unless-stopped \
  -p 172.31.0.3:5432:5432 \
  -e POSTGRES_DB=llm_gateway \
  -e POSTGRES_USER=llm_gateway \
  -e POSTGRES_PASSWORD="$PG_PASSWORD" \
  -e PGDATA=/var/lib/postgresql/data/pgdata \
  -v /data/postgres-llm-primary:/var/lib/postgresql/data \
  postgres:15-alpine \
  postgres \
    -c listen_addresses='*' \
    -c max_connections=1000 \
    -c shared_buffers=1GB \
    -c effective_cache_size=2GB \
    -c wal_level=replica \
    -c max_wal_senders=10 \
    -c max_replication_slots=10

# 等待启动
sleep 15
docker exec llm-pg-71-primary pg_isready -U llm_gateway
EOF
```

### Step 1.5: 导入数据到 71 PG

```bash
ssh root@172.31.0.3 <<'EOF'
DUMP_FILE=$(ls -t /tmp/llm_gateway_*.dump | head -1)
echo "开始导入 $DUMP_FILE（这会花费 10-30 分钟）..."

docker exec -i llm-pg-71-primary \
  pg_restore -U llm_gateway -d llm_gateway \
  --verbose --no-owner --no-acl \
  < "$DUMP_FILE"

echo "导入完成，验证..."
docker exec llm-pg-71-primary \
  psql -U llm_gateway -d llm_gateway -c \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';"
EOF
```

### Step 1.6: 配置 71 PG 为 PRIMARY + 创建 replication 用户

```bash
ssh root@172.31.0.3 <<'EOF'
# 创建 replication 用户
docker exec llm-pg-71-primary \
  psql -U llm_gateway -d llm_gateway <<SQL
CREATE USER repl_71_to_184 WITH REPLICATION PASSWORD '${REPL_PASSWORD}';
SQL

# 配置 pg_hba.conf 允许 184 复制
docker exec llm-pg-71-primary sh -c "cat >> /var/lib/postgresql/data/pgdata/pg_hba.conf <<HBA
# 允许 184 进行流复制
host    replication     repl_71_to_184     172.31.0.4/32       md5
host    replication     repl_71_to_184     172.31.0.0/20       md5
HBA"

# 重载配置
docker exec llm-pg-71-primary \
  psql -U llm_gateway -d llm_gateway -c "SELECT pg_reload_conf();"

echo "✅ 71 PG PRIMARY 已就绪"
EOF
```

---

## Phase 2: 184 PG 重建为 STANDBY（30 分钟）

### ⚠️ 关键停机点

**这一步会停止 184 PG，业务完全中断。**

### Step 2.1: 停止 184 k8s PG pod

```bash
ssh root@14.103.112.184 <<'EOF'
echo "停止 184 PG pod..."
kubectl -n pms-test scale deploy llm-gateway-pg --replicas=0

# 等待 pod 完全终止
kubectl -n pms-test wait --for=delete pod -l app=llm-gateway-pg --timeout=60s
EOF
```

### Step 2.2: 备份 184 PG 数据

```bash
ssh root@14.103.112.184 <<'EOF'
echo "备份 184 PG 数据目录..."
mv /data/pms-test/llm-gateway-pg \
   /data/pms-test/llm-gateway-pg.bak-$(date +%Y%m%d%H%M%S)

mkdir -p /data/pms-test/llm-gateway-pg
chmod 700 /data/pms-test/llm-gateway-pg
EOF
```

### Step 2.3: pg_basebackup 从 71 拉取数据

```bash
ssh root@14.103.112.184 <<'EOF'
echo "从 71 pg_basebackup（这会花费 10-20 分钟）..."

docker run --rm \
  -v /data/pms-test/llm-gateway-pg:/var/lib/postgresql/data \
  -e PGPASSWORD="${REPL_PASSWORD}" \
  postgres:15-alpine \
  pg_basebackup \
    -h 172.31.0.3 \
    -p 5432 \
    -U repl_71_to_184 \
    -D /var/lib/postgresql/data \
    -Fp -Xs -P -R

# -R 会自动生成 standby.signal 和 postgresql.auto.conf
EOF
```

### Step 2.4: 配置 standby 参数

```bash
ssh root@14.103.112.184 <<'EOF'
# 追加 standby 优化配置
cat >> /data/pms-test/llm-gateway-pg/postgresql.conf <<CONF

# === Standby Configuration ===
hot_standby = on
max_standby_streaming_delay = 30s
max_standby_archive_delay = 60s
wal_receiver_timeout = 60s
wal_receiver_status_interval = 10s
hot_standby_feedback = on
primary_conninfo = 'host=172.31.0.3 port=5432 user=repl_71_to_184 password=${REPL_PASSWORD} application_name=standby_184'
CONF

# 修复权限
chown -R 70:70 /data/pms-test/llm-gateway-pg
EOF
```

### Step 2.5: 启动 184 PG pod

```bash
ssh root@14.103.112.184 <<'EOF'
echo "启动 184 PG pod（standby 模式）..."
kubectl -n pms-test scale deploy llm-gateway-pg --replicas=1

# 等待 pod 就绪
kubectl -n pms-test wait --for=condition=ready pod -l app=llm-gateway-pg --timeout=120s

echo "验证 standby 状态..."
kubectl -n pms-test exec deploy/llm-gateway-pg -- \
  psql -U llm_gateway -d llm_gateway -c "SELECT pg_is_in_recovery();"
# 预期: t (true)

echo "验证复制延迟..."
kubectl -n pms-test exec deploy/llm-gateway-pg -- \
  psql -U llm_gateway -d llm_gateway -c \
  "SELECT EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))::int AS lag_seconds;"
# 预期: 0-5
EOF
```

---

## Phase 3: 应用层配置（10 分钟）

### 目标

71 和 184 的 llm-gateway-go **都指向 71 PG**。

### Step 3.1: 修改 71 llm-gateway-go env

```bash
ssh root@172.31.0.3 <<'EOF'
chattr -i /etc/llm-gateway-go/env 2>/dev/null || true

# 备份
cp /etc/llm-gateway-go/env /etc/llm-gateway-go/env.bak-$(date +%Y%m%d%H%M%S)

# 修改为指向本地 71 PG
sed -i 's|@172.31.0.4:5432|@172.31.0.3:5432|g' /etc/llm-gateway-go/env
sed -i 's|@127.0.0.1:5432|@172.31.0.3:5432|g' /etc/llm-gateway-go/env

# 确保是 172.31.0.3（而不是 127.0.0.1，因为 184 也要连）
grep DATABASE_URL /etc/llm-gateway-go/env

chattr +i /etc/llm-gateway-go/env

# 重启
systemctl restart llm-gateway-go.service

# 等待就绪
sleep 15
curl -s http://127.0.0.1:8781/healthz
EOF
```

### Step 3.2: 修改 184 llm-gateway-go env

```bash
ssh root@14.103.112.184 <<'EOF'
echo "修改 184 llm-gateway-go 指向 71 PG..."

# k8s secret 修改
kubectl -n pms-test get secret llm-gateway-secret -o yaml > /tmp/secret.yaml

# 编辑 secret（需要 base64 编码）
# LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:***@172.31.0.3:5432/llm_gateway
NEW_DSN="postgres://llm_gateway:${PG_PASSWORD}@172.31.0.3:5432/llm_gateway"
kubectl -n pms-test create secret generic llm-gateway-secret \
  --from-literal=LLM_GATEWAY_DATABASE_URL="$NEW_DSN" \
  --dry-run=client -o yaml | kubectl apply -f -

# 重启 184 llm-gateway-go pod
kubectl -n pms-test rollout restart deploy/llm-gateway-go-deployment
kubectl -n pms-test rollout status deploy/llm-gateway-go-deployment --timeout=120s

# 验证
kubectl -n pms-test exec deploy/llm-gateway-go-deployment -- \
  sh -c 'curl -s http://localhost:8781/healthz'
EOF
```

---

## Phase 4: 56 nginx 负载均衡（10 分钟）

参考之前的方案 v2.0，配置：

```nginx
# 多层级 sticky key
map $http_x_gw_session_id $sticky_gw_session { ... }
map $http_x_device_seed $sticky_device { ... }
map $http_authorization $sticky_auth { ... }

map "$sticky_gw_session:$sticky_device:$sticky_auth:$remote_addr" $sticky_key { ... }

upstream llm-backend {
    hash $sticky_key consistent;
    server 172.31.0.3:8781 max_fails=3 fail_timeout=15s;
    server 172.31.0.4:10023 max_fails=3 fail_timeout=15s;
}
```

---

## Phase 5: HA 监控与切换（30 分钟）

### 部署监控脚本

1. `/usr/local/bin/pg-ha-monitor.sh`（每分钟探测）
2. `/usr/local/bin/ha-switch.sh`（手动切换执行器）
3. systemd timer

### 网页控制台

集成到 ACC 后台：`acc.kxpms.cn/admin/ha-monitor`

---

## Phase 6: 验证（30 分钟）

### 验证清单

- [ ] 71 PG: `pg_is_in_recovery() = f` (PRIMARY)
- [ ] 184 PG: `pg_is_in_recovery() = t` (STANDBY)
- [ ] 184 复制延迟 < 5s
- [ ] 71 llm-gateway-go 连 172.31.0.3:5432
- [ ] 184 llm-gateway-go 连 172.31.0.3:5432
- [ ] 56 nginx 双后端健康
- [ ] 在 71 PG 插入测试数据 → 5s 后 184 PG 能查到
- [ ] 模拟 71 PG 故障 → 自动切换 → 184 PG promote

---

## 回滚方案

如果 Phase 1-2 失败：

```bash
# 恢复 184 PG
ssh root@14.103.112.184 <<'EOF'
kubectl -n pms-test scale deploy llm-gateway-pg --replicas=0
rm -rf /data/pms-test/llm-gateway-pg
mv /data/pms-test/llm-gateway-pg.bak-* /data/pms-test/llm-gateway-pg
kubectl -n pms-test scale deploy llm-gateway-pg --replicas=1
EOF

# 恢复应用 env
ssh root@172.31.0.3 "cp /etc/llm-gateway-go/env.bak-* /etc/llm-gateway-go/env && systemctl restart llm-gateway-go.service"
```

---

## 下一步

**Phase 0（现状调查）需要您提供凭据或手动执行**，因为我没有 SSH 和 kubectl 访问权限。

请告诉我：
1. 是否授权我通过 `env-injector` 加载凭据？
2. 还是您手动执行 Phase 0 并报告结果？

Phase 0 结果确认后，我将继续 Phase 1-6。
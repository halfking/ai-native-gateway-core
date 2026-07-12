# kaixuan-1 数据库同步指南

> 生成时间: 2026-07-10 19:00
> 
> **当前状态**: 等待执行（PG 连接池已满，需先释放连接）

---

## 1. 前置条件

### 1.1 问题诊断

**当前问题**: `FATAL: sorry, too many clients already`

**解决方案（选择其一）**:

#### 方案 A: 重启 PG pod (推荐)

```bash
# 在 kaixuan-1 主机上 (192.168.31.28)
export SSHPASS='kaixuan123'
sshpass -e ssh kaixuan@192.168.31.28

# 进入 Tart VM (k3s-server at 192.168.31.8)
# 方法1: 直接在 VM 内执行
sudo kubectl get pods -A | grep postgres
sudo kubectl delete pod <pg-pod-name> -n <namespace>

# 方法2: 如果有 kubeconfig
export KUBECONFIG=/path/to/k3s.yaml
kubectl get pods -A | grep postgres
kubectl delete pod <pg-pod-name> -n <namespace>
```

#### 方案 B: 等待连接释放

等待 10-30 分钟，让空闲连接超时释放。

#### 方案 C: 调整 max_connections

```sql
-- 临时增加连接数
ALTER SYSTEM SET max_connections = 200;
SELECT pg_reload_conf();
```

### 1.2 验证连接可用

```bash
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql \
  -h 192.168.31.8 -p 30432 -U llm_gateway -d llm_gateway \
  -c "SELECT 1;"
```

输出 `(1 row)` 表示连接正常。

---

## 2. 执行同步

### 2.1 完整同步（推荐）

```bash
cd ~/workspace/official-deploy/services/llm-gateway-go

# 执行同步
./scripts/pg-table-copy.sh \
  --source configs/env-252.sh \
  --target configs/env-kaixuan1.sh \
  --verbose
```

**预期结果**:
- 表数量: ~223 (含 Citus 内部表)
- DB 大小: ~58 MB
- 行数匹配: ~24/30 (与本地同步一致)
- 执行时间: ~3-5 分钟

### 2.2 仅同步结构（快速）

```bash
./scripts/pg-table-copy.sh \
  --source configs/env-252.sh \
  --target configs/env-kaixuan1.sh \
  --schema-only
```

### 2.3 Dry Run（预览）

```bash
./scripts/pg-table-copy.sh \
  --source configs/env-252.sh \
  --target configs/env-kaixuan1.sh \
  --dry-run --verbose
```

---

## 3. 验证同步结果

### 3.1 表数量

```bash
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql \
  -h 192.168.31.8 -p 30432 -U llm_gateway -d llm_gateway \
  -c "SELECT count(*) FROM pg_tables WHERE schemaname='public';"
```

**预期**: 184 表 (不含 Citus 内部表)

### 3.2 DB 大小

```bash
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql \
  -h 192.168.31.8 -p 30432 -U llm_gateway -d llm_gateway \
  -c "SELECT pg_size_pretty(pg_database_size('llm_gateway'));"
```

**预期**: ~58 MB

### 3.3 扩展检查

```bash
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql \
  -h 192.168.31.8 -p 30432 -U llm_gateway -d llm_gateway \
  -c "SELECT extname, extversion FROM pg_extension WHERE extname IN ('vector','citus','columnar_am');"
```

**预期**:
- `citus | 13.3-1`
- `vector | <version>`
- `columnar_am | <version>` (可能没有)

### 3.4 行数对比（Top 10）

```bash
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql \
  -h 192.168.31.8 -p 30432 -U llm_gateway -d llm_gateway \
  -c "
SELECT schemaname||'.'||tablename AS tbl,
       pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size,
       (SELECT count(*) FROM pg_tables t2 WHERE t2.schemaname=pg_tables.schemaname AND t2.tablename=pg_tables.tablename) AS rows
FROM pg_tables
WHERE schemaname='public'
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC
LIMIT 10;
"
```

---

## 4. 对比验证（本地 vs kaixuan-1）

### 4.1 生成对比报告

```bash
cd ~/workspace/official-deploy/services/llm-gateway-go

# 生成 kaixuan-1 状态
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql \
  -h 192.168.31.8 -p 30432 -U llm_gateway -d llm_gateway \
  -c "SELECT count(*) as tables, pg_size_pretty(pg_database_size('llm_gateway')) as size FROM pg_tables WHERE schemaname='public';" \
  > /tmp/kaixuan1-status.txt

# 生成本地状态
PGPASSWORD="llm_gateway_db_pass_2026_secure" psql \
  -h localhost -p 5432 -U llm_gateway -d llm_gateway \
  -c "SELECT count(*) as tables, pg_size_pretty(pg_database_size('llm_gateway')) as size FROM pg_tables WHERE schemaname='public';" \
  > /tmp/local-status.txt

# 对比
diff /tmp/local-status.txt /tmp/kaixuan1-status.txt
```

**预期**: 无差异（或仅有微小行数差异）

---

## 5. 故障排查

### 5.1 连接超时

```bash
# 检查网络
ping -c 3 192.168.31.8
telnet 192.168.31.8 30432

# 检查 PG 状态
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" pg_isready \
  -h 192.168.31.8 -p 30432
```

### 5.2 权限错误

```bash
# 确认用户
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql \
  -h 192.168.31.8 -p 30432 -U llm_gateway -d llm_gateway \
  -c "\du llm_gateway"

# 确认数据库
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql \
  -h 192.168.31.8 -p 30432 -U llm_gateway -d postgres \
  -c "\l llm_gateway"
```

### 5.3 同步失败

```bash
# 查看详细日志
./scripts/pg-table-copy.sh \
  --source configs/env-252.sh \
  --target configs/env-kaixuan1.sh \
  --verbose 2>&1 | tee /tmp/sync.log

# 检查 dump 文件
ls -lh /tmp/pg-table-copy/<timestamp>/
```

---

## 6. 配置文件

### 6.1 源配置 (252)

```bash
# configs/env-252.sh
PG_HOST="localhost"              # Via SSH tunnel
PG_PORT="15432"                  # SSH tunnel port
PG_USER="llm_gateway"
PG_PASS="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"
PG_DB="llm_gateway"
```

**注意**: 需要先建立 SSH tunnel:

```bash
export SSHPASS='Kaixuan2026&#*9527'
sshpass -e ssh -f -N -o ServerAliveInterval=30 \
  -L 15432:172.16.2.210:5432 \
  -p 25022 root@115.29.212.252
```

### 6.2 目标配置 (kaixuan-1)

```bash
# configs/env-kaixuan1.sh
PG_HOST="192.168.31.8"          # k3s server (Tart VM)
PG_PORT="30432"                  # k3s NodePort
PG_USER="llm_gateway"
PG_PASS="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"
PG_DB="llm_gateway"
```

---

## 7. 快速命令参考

```bash
# 1. 检查连接
pg_isready -h 192.168.31.8 -p 30432

# 2. 建立 SSH tunnel (252)
export SSHPASS='Kaixuan2026&#*9527'
sshpass -e ssh -f -N -L 15432:172.16.2.210:5432 -p 25022 root@115.29.212.252

# 3. 执行同步
cd ~/workspace/official-deploy/services/llm-gateway-go
./scripts/pg-table-copy.sh --source configs/env-252.sh --target configs/env-kaixuan1.sh --verbose

# 4. 验证
PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql -h 192.168.31.8 -p 30432 -U llm_gateway -d llm_gateway -c "SELECT count(*) FROM pg_tables WHERE schemaname='public';"

# 5. 清理 SSH tunnel
lsof -tiTCP:15432 -sTCP:LISTEN 2>/dev/null | xargs kill 2>/dev/null
```

---

## 8. 注意事项

1. **连接池**: 确保 PG 有足够的可用连接（max_connections - current_connections > 5）
2. **SSH Tunnel**: 252 的 SSH tunnel 必须保持活跃（ServerAliveInterval=30）
3. **执行时间**: 完整同步需要 3-5 分钟，请勿中断
4. **热表**: `*_hot`, `*_2026_*` 表仅同步结构，不含数据（设计如此）
5. **验证**: 同步后务必执行第 3 节的验证步骤

---

## 9. 本地同步结果（参考）

| 指标 | 结果 |
|------|------|
| 源 DB 大小 | 8257 MB |
| 目标 DB 大小 | 58 MB |
| 表数量 | 184 (public) / 223 (含 Citus) |
| 行数匹配 | 24/30 |
| 执行时间 | 3 分钟 |
| 状态 | ✅ 成功 |

**预期 kaixuan-1 同步结果与本地一致。**

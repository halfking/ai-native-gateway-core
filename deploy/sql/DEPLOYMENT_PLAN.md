# 数据库部署方案

> **版本**: v1.0  
> **适用项目**: llm-gateway-go  
> **目标环境**: 184服务器 (PostgreSQL + Citus 11.3)

---

## 目录

1. [方案概述](#方案概述)
2. [部署架构](#部署架构)
3. [部署前置条件](#部署前置条件)
4. [部署流程](#部署流程)
5. [迁移策略](#迁移策略)
6. [回滚方案](#回滚方案)
7. [验证清单](#验证清单)
8. [监控告警](#监控告警)
9. [应急预案](#应急预案)

---

## 方案概述

### 设计目标

1. **零停机部署**：通过蓝绿部署和在线迁移实现
2. **可回滚**：每个迁移都有回滚脚本
3. **可验证**：自动化测试验证部署结果
4. **可追踪**：完整的部署日志和版本记录

### 部署范围

| 组件 | 说明 | 来源 |
|------|------|------|
| Schema基线 | 完整DDL + 扩展 + 初始数据 | `deploy/sql/schemas/baseline/` |
| Startup迁移 | Go应用启动时自动应用 | `sql/migrations/startup/` |
| Domain迁移 | 业务领域迁移 | `sql/migrations/domain/` |
| 定时任务 | K8s CronJob调度的SQL | `deploy/sql/cron/` |
| 验证测试 | 部署后验证脚本 | `deploy/sql/tests/` |

---

## 部署架构

### 数据库架构

```
┌─────────────────────────────────────────────┐
│         184 服务器 (14.103.112.184)         │
│                                             │
│  ┌───────────────────────────────────────┐ │
│  │   PostgreSQL + Citus 11.3 Cluster    │ │
│  │                                       │ │
│  │   ┌─────────────┐  ┌─────────────┐  │ │
│  │   │ Coordinator │  │   Worker 1  │  │ │
│  │   │   (主节点)  │  │             │  │ │
│  │   └─────────────┘  └─────────────┘  │ │
│  │         │                  │         │ │
│  │         └──────────────────┘         │ │
│  │                                       │ │
│  │   Database: llm_gateway              │ │
│  │   Port: 5432                         │ │
│  │   User: llm_gateway_user             │ │
│  └───────────────────────────────────────┘ │
│                                             │
│  ┌───────────────────────────────────────┐ │
│  │    K8s Deployment (pms-test ns)      │ │
│  │                                       │ │
│  │   llm-gateway-go-deployment          │ │
│  │   ├── Startup Migrations (自动)      │ │
│  │   └── Application Server             │ │
│  └───────────────────────────────────────┘ │
└─────────────────────────────────────────────┘
```

### 迁移流程架构

```
┌──────────────┐
│  部署触发    │
│ (deploy-184) │
└──────┬───────┘
       │
       ▼
┌──────────────────┐
│ 1. 构建镜像      │
│    & 推送        │
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 2. 备份数据库    │
│  (pg_dump -Fc)   │
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 3. 检查schema版本│
│  (version表)     │
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 4. 应用Domain    │
│    迁移 (手动)   │
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 5. 更新K8s部署   │
│  (kubectl set)   │
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 6. Pod启动       │
│  + Startup迁移   │
│    (自动)        │
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 7. 健康检查      │
│  (readiness)     │
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 8. 验证测试      │
│  (smoke tests)   │
└──────┬───────────┘
       │
   ┌───▼───┐
   │成功？ │
   └─┬───┬─┘
     │   │
   是│   │否
     │   │
     ▼   ▼
  ┌────┐ ┌──────┐
  │完成│ │回滚  │
  └────┘ └──────┘
```

---

## 部署前置条件

### 1. 环境检查

```bash
# 检查数据库连接
psql "postgresql://llm_gateway_user:xxx@14.103.112.184:5432/llm_gateway?sslmode=disable" -c "SELECT version();"

# 检查Citus版本
psql "postgresql://..." -c "SELECT * FROM citus_version();"

# 检查磁盘空间（至少20GB可用）
df -h | grep /var/lib/postgresql

# 检查K8s集群状态
kubectl get nodes
kubectl get pods -n pms-test
```

### 2. 权限确认

```sql
-- 确认用户权限
SELECT grantee, privilege_type 
FROM information_schema.role_table_grants 
WHERE grantee = 'llm_gateway_user';

-- 确认扩展安装权限
SELECT * FROM pg_available_extensions 
WHERE name IN ('citus', 'pgcrypto', 'uuid-ossp');
```

### 3. 备份验证

```bash
# 创建全量备份
pg_dump -h 14.103.112.184 -U llm_gateway_user -d llm_gateway \
  -Fc -f /backup/llm_gateway_$(date +%Y%m%d_%H%M%S).dump

# 验证备份完整性
pg_restore --list /backup/llm_gateway_*.dump | wc -l
```

---

## 部署流程

### 阶段 1: 新环境初始化（仅首次）

**场景**: 全新数据库实例

```bash
# 1. 安装扩展
psql "$DB_URL" -f deploy/sql/schemas/baseline/00-prereqs.sql

# 2. 创建Schema
psql "$DB_URL" -f deploy/sql/schemas/baseline/01-schema.sql

# 3. 导入初始数据
psql "$DB_URL" -f deploy/sql/schemas/baseline/02-seed.sql

# 4. 验证
psql "$DB_URL" -c "\dt" | wc -l  # 应该有103个表
```

### 阶段 2: Domain迁移（手动）

**场景**: 部署新版本前，需要手动执行的数据库变更

```bash
# 检查当前版本
psql "$DB_URL" -c "SELECT * FROM schema_version ORDER BY version DESC LIMIT 1;"

# 按序号应用未执行的迁移
cd sql/migrations/domain/

for f in [0-9]*.sql; do
  echo "Applying $f..."
  psql "$DB_URL" -f "$f"
  
  # 记录版本
  version=$(basename "$f" .sql)
  psql "$DB_URL" -c "INSERT INTO migration_log (version, applied_at) VALUES ('$version', NOW());"
done
```

### 阶段 3: 应用部署

**由 `deploy-184.sh` 自动执行**

```bash
# 1. 构建并推送镜像
bash deploy-184.sh

# 内部流程：
# - 检查 build_seq
# - docker build
# - docker push registry.kxpms.cn
# - 同步到 127.0.0.1:5000
# - kubectl set image
# - kubectl rollout status
```

### 阶段 4: Startup迁移（自动）

**由 Go 应用启动时自动执行**

```go
// cmd/llm-gateway-go/main.go
func applyStartupMigrations(db *sql.DB) error {
    files, _ := filepath.Glob("sql/migrations/startup/*.sql")
    sort.Strings(files)
    
    for _, file := range files {
        // 检查是否已应用
        if isMigrationApplied(db, file) {
            continue
        }
        
        // 应用迁移
        content, _ := os.ReadFile(file)
        _, err := db.Exec(string(content))
        if err != nil {
            return fmt.Errorf("migration %s failed: %w", file, err)
        }
        
        // 记录版本
        recordMigration(db, file)
    }
    return nil
}
```

### 阶段 5: 验证测试

```bash
# 1. 运行冒烟测试
psql "$DB_URL" -f deploy/sql/tests/038_adaptive_probe_test.sql

# 2. 检查应用健康
curl http://llmgo.kxpms.cn/health

# 3. 检查K8s状态
kubectl get pods -n pms-test -l app=llm-gateway-go
kubectl logs -n pms-test -l app=llm-gateway-go --tail=50
```

---

## 迁移策略

### 1. 表结构变更

**添加列（推荐）**

```sql
-- ✓ 安全：添加可空列，不阻塞
ALTER TABLE my_table 
ADD COLUMN IF NOT EXISTS new_column TEXT;

-- 后续可选：设置默认值
ALTER TABLE my_table 
ALTER COLUMN new_column SET DEFAULT 'default_value';
```

**修改列类型（需评估）**

```sql
-- 小表：直接修改
ALTER TABLE small_table 
ALTER COLUMN my_column TYPE BIGINT USING my_column::BIGINT;

-- 大表：分步迁移
-- Step 1: 添加新列
ALTER TABLE large_table ADD COLUMN my_column_new BIGINT;

-- Step 2: 应用代码同时写入两列（双写）

-- Step 3: 批量回填数据
UPDATE large_table SET my_column_new = my_column::BIGINT 
WHERE my_column_new IS NULL LIMIT 10000;
-- 重复直到全部完成

-- Step 4: 应用代码切换读取新列

-- Step 5: 删除旧列
ALTER TABLE large_table DROP COLUMN my_column;
ALTER TABLE large_table RENAME COLUMN my_column_new TO my_column;
```

**删除列（需评估影响）**

```sql
-- 先部署代码移除依赖，再删除列
ALTER TABLE my_table DROP COLUMN IF EXISTS old_column;
```

### 2. 索引变更

**在线创建索引**

```sql
-- ✓ 不阻塞：并发创建
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_my_table_column 
ON my_table (column);
```

**删除索引**

```sql
-- ✓ 安全：立即生效
DROP INDEX CONCURRENTLY IF EXISTS idx_old;
```

### 3. 数据迁移

**小数据量（<10万行）**

```sql
-- 直接更新
UPDATE my_table SET status = 'active' WHERE status IS NULL;
```

**大数据量（>10万行）**

```sql
-- 分批处理
DO $$
DECLARE
  batch_size INT := 10000;
  processed INT := 0;
BEGIN
  LOOP
    WITH batch AS (
      SELECT id FROM my_table 
      WHERE status IS NULL 
      LIMIT batch_size
      FOR UPDATE SKIP LOCKED
    )
    UPDATE my_table SET status = 'active'
    WHERE id IN (SELECT id FROM batch);
    
    GET DIAGNOSTICS processed = ROW_COUNT;
    EXIT WHEN processed = 0;
    
    COMMIT;
    PERFORM pg_sleep(0.1);  -- 避免长时间锁表
  END LOOP;
END $$;
```

### 4. 分布式表（Citus）变更

**添加分布式表**

```sql
-- 创建表
CREATE TABLE distributed_table (
    id BIGSERIAL PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    data JSONB
);

-- 分布到所有worker
SELECT create_distributed_table('distributed_table', 'tenant_id');
```

**修改分布键（需重建）**

```sql
-- 无法直接修改分布键，需要：
-- 1. 创建新表
-- 2. 迁移数据
-- 3. 切换应用
-- 4. 删除旧表
```

---

## 回滚方案

### 1. 应用回滚

```bash
# 快速回滚到上一版本
kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test

# 回滚到指定版本
kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test --to-revision=3

# 查看回滚历史
kubectl rollout history deployment/llm-gateway-go-deployment -n pms-test
```

### 2. 数据库回滚

**策略 A: 执行回滚SQL（推荐）**

```bash
# 每个迁移都应该有对应的 .down.sql
psql "$DB_URL" -f deploy/sql/docs/features/2026-06-15-auto-route-mode.down.sql
```

**策略 B: 恢复备份（最后手段）**

```bash
# 1. 停止应用
kubectl scale deployment/llm-gateway-go-deployment -n pms-test --replicas=0

# 2. 清空数据库
psql "$DB_URL" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

# 3. 恢复备份
pg_restore -h 14.103.112.184 -U llm_gateway_user -d llm_gateway \
  --clean --if-exists /backup/llm_gateway_20260706.dump

# 4. 启动应用
kubectl scale deployment/llm-gateway-go-deployment -n pms-test --replicas=3
```

### 3. 回滚决策矩阵

| 场景 | 回滚策略 | 执行时间 | 数据丢失风险 |
|------|----------|----------|--------------|
| 应用启动失败 | 应用回滚 | <2分钟 | 无 |
| 迁移失败 | 执行 .down.sql | <5分钟 | 无 |
| 数据损坏 | 恢复备份 | 10-30分钟 | 有（损失备份后数据） |
| 性能下降 | 应用回滚 + 索引回滚 | <5分钟 | 无 |

---

## 验证清单

### 部署前验证

- [ ] 备份已创建并验证完整性
- [ ] Domain迁移已在测试环境验证
- [ ] 磁盘空间充足（>20GB）
- [ ] 数据库连接正常
- [ ] K8s集群健康

### 部署后验证

- [ ] 所有Pod状态为Running
- [ ] Readiness探针通过
- [ ] 健康检查接口返回200
- [ ] 日志无ERROR级别错误
- [ ] Startup迁移全部应用
- [ ] 冒烟测试通过
- [ ] 监控指标正常

### SQL验证脚本

```sql
-- 1. 检查表数量
SELECT COUNT(*) FROM information_schema.tables 
WHERE table_schema = 'public' AND table_type = 'BASE TABLE';
-- 期望: 103

-- 2. 检查迁移版本
SELECT * FROM migration_log ORDER BY applied_at DESC LIMIT 10;

-- 3. 检查分布式表
SELECT * FROM citus_tables;

-- 4. 检查索引健康
SELECT schemaname, tablename, indexname 
FROM pg_indexes 
WHERE schemaname = 'public' 
ORDER BY tablename, indexname;

-- 5. 检查RLS策略
SELECT schemaname, tablename, policyname 
FROM pg_policies 
WHERE schemaname = 'public';
```

---

## 监控告警

### 关键指标

| 指标 | 阈值 | 告警级别 |
|------|------|----------|
| 数据库连接数 | >80% max_connections | Warning |
| 慢查询（>1s） | >10/min | Warning |
| 死锁数量 | >0 | Critical |
| 磁盘使用率 | >85% | Critical |
| Replication延迟 | >10s | Warning |
| 长事务（>30s） | >0 | Warning |

### 监控查询

```sql
-- 1. 当前连接数
SELECT count(*) as connections, 
       current_setting('max_connections')::int as max
FROM pg_stat_activity;

-- 2. 慢查询
SELECT pid, now() - query_start AS duration, query 
FROM pg_stat_activity 
WHERE state = 'active' AND now() - query_start > interval '1 second';

-- 3. 死锁
SELECT * FROM pg_stat_database WHERE datname = 'llm_gateway';
-- 查看 deadlocks 字段

-- 4. 表膨胀
SELECT schemaname, tablename, 
       pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_tables 
WHERE schemaname = 'public' 
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC 
LIMIT 10;
```

---

## 应急预案

### 场景1: 迁移执行超时

**症状**: Domain迁移执行超过预期时间

**处理**:
1. 检查是否有锁等待：
   ```sql
   SELECT * FROM pg_locks WHERE NOT granted;
   ```
2. 识别阻塞会话并终止：
   ```sql
   SELECT pg_terminate_backend(pid) FROM pg_stat_activity 
   WHERE state = 'idle in transaction' AND now() - state_change > interval '5 minutes';
   ```
3. 如果迁移可中断，执行回滚
4. 优化迁移脚本（批量处理、添加LIMIT）

### 场景2: 磁盘空间不足

**症状**: 磁盘使用率>90%

**处理**:
1. 清理WAL日志：
   ```bash
   sudo -u postgres pg_archivecleanup /var/lib/postgresql/11/main/pg_wal/ <oldest_wal>
   ```
2. VACUUM回收空间：
   ```sql
   VACUUM FULL ANALYZE;
   ```
3. 清理过期备份
4. 扩容磁盘

### 场景3: 主从复制延迟

**症状**: Replication lag > 10s

**处理**:
1. 检查延迟：
   ```sql
   SELECT client_addr, state, 
          pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) AS byte_lag
   FROM pg_stat_replication;
   ```
2. 检查网络和磁盘IO
3. 临时停止非关键写入
4. 考虑调整 `max_wal_senders`, `wal_keep_segments`

### 场景4: 应用无法连接数据库

**症状**: 连接池耗尽或连接超时

**处理**:
1. 检查连接数：
   ```sql
   SELECT count(*), state FROM pg_stat_activity GROUP BY state;
   ```
2. 终止空闲连接：
   ```sql
   SELECT pg_terminate_backend(pid) FROM pg_stat_activity 
   WHERE state = 'idle' AND now() - state_change > interval '10 minutes';
   ```
3. 检查应用连接池配置
4. 临时提高 `max_connections`（需重启）

---

## 附录

### A. 常用命令

```bash
# 连接数据库
export DB_URL="postgresql://llm_gateway_user:xxx@14.103.112.184:5432/llm_gateway?sslmode=disable"
psql "$DB_URL"

# 备份数据库
pg_dump -h 14.103.112.184 -U llm_gateway_user -d llm_gateway -Fc -f backup.dump

# 恢复数据库
pg_restore -h 14.103.112.184 -U llm_gateway_user -d llm_gateway backup.dump

# 查看K8s Pod日志
kubectl logs -n pms-test -l app=llm-gateway-go --tail=100 -f

# 进入Pod调试
kubectl exec -it -n pms-test <pod-name> -- /bin/sh
```

### B. 相关文档

- [sql/README.md](../../sql/README.md) - SQL资产SSOT
- [deploy/sql/README.md](./README.md) - 部署SQL资产
- [deploy/DEPLOYMENT_GUIDE.md](../DEPLOYMENT_GUIDE.md) - 完整部署指南
- [deploy-184.sh](../deploy-184.sh) - 184服务器部署脚本

### C. 联系方式

- **DBA**: xxx@example.com
- **DevOps**: devops@example.com
- **On-call**: +86-xxx-xxxx-xxxx

---

**文档版本**: v1.0  
**最后更新**: 2026-07-06  
**维护者**: DevOps Team

# Migration 341 本地测试检查清单

**日期**: 2026-07-05  
**环境**: 本地开发环境 (localhost)  
**状态**: 准备测试

---

## ⚠️ 重要约束

**禁止操作**：
- ❌ 不得连接 71 环境数据库（__DOMAIN_2__）
- ❌ 不得修改 71 环境任何数据
- ❌ 不得部署代码到 71 环境

**允许操作**：
- ✅ 本地环境测试
- ✅ 184 环境测试（用户批准后）
- ✅ 代码提交到 Git（但不自动部署到 71）

---

## 测试前准备

### 1. 确认本地环境

```bash
# 检查数据库连接
psql -h localhost -U kxuser -d llm_gateway -c "SELECT version();"

# 检查 request_logs_default 数据
psql -h localhost -U kxuser -d llm_gateway -c "
  SELECT count(*) as rows,
         pg_size_pretty(pg_total_relation_size('request_logs_default')) as size
  FROM request_logs_default;
"
```

### 2. 代码变更清单

已修改文件：
- ✅ `db/migrations/341_hot_table_independence.sql`
- ✅ `db/migrations/341_hot_table_independence.down.sql`
- ✅ `domains/hooks/observability/telemetry/client.go`
- ✅ `admin/*.go` (8 个文件)
- ✅ `bg/partition_manager.go`
- ✅ `scripts/partition/test-migration-341.sh`

### 3. Git 提交准备

```bash
# 查看变更
git status

# 分阶段提交（建议）
git add db/migrations/341_*
git commit -m "feat(partition): Migration 341 - hot table independence (request_logs only)"

git add domains/hooks/observability/telemetry/client.go admin/*.go bg/partition_manager.go
git commit -m "refactor(partition): update code to use request_logs_hot instead of _default"

git add scripts/partition/test-migration-341.sh
git commit -m "test(partition): add migration 341 local test script"

git add docs/partition/*.md
git commit -m "docs(partition): add hot table optimization documentation"
```

---

## 本地测试步骤

### Step 1: 备份当前数据

```bash
# 备份整个数据库
pg_dump -h localhost -U kxuser llm_gateway > backup_local_before_341_$(date +%Y%m%d_%H%M%S).sql

# 或仅备份 request_logs_default
psql -h localhost -U kxuser -d llm_gateway -c "
  CREATE TABLE request_logs_default_backup_341 AS 
  SELECT * FROM request_logs_default;
"
```

### Step 2: 运行自动化测试

```bash
cd __LOCAL_PATH_1__
./scripts/partition/test-migration-341.sh
```

**预期输出**：
```
========================================
Migration 341 本地测试
========================================
环境: localhost:__PORT_5__/llm_gateway

✅ 备份完成: XXXX 行
✅ Migration 341 应用成功
✅ 数据完整性验证通过
✅ INSERT 测试通过
✅ UPDATE 测试通过
✅ UPSERT 测试通过
✅ Promote 函数工作正常
✅ 测试数据已清理

========================================
✅ Migration 341 本地测试通过
========================================
```

### Step 3: 手动验证

```bash
# 1. 检查热表
psql -h localhost -U kxuser -d llm_gateway -c "
  SELECT count(*) as rows,
         pg_size_pretty(pg_total_relation_size('request_logs_hot')) as size
  FROM request_logs_hot;
"

# 2. 检查索引
psql -h localhost -U kxuser -d llm_gateway -c "
  SELECT indexname FROM pg_indexes 
  WHERE tablename = 'request_logs_hot';
"

# 3. 检查分区状态（应全部 ATTACHED）
psql -h localhost -U kxuser -d llm_gateway -c "
  SELECT c.relname,
         CASE WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED' ELSE 'DETACHED' END
  FROM pg_class c
  LEFT JOIN pg_inherits i ON c.oid = i.inhrelid
  WHERE c.relname LIKE 'request_logs_2026%'
  ORDER BY c.relname;
"

# 4. 检查 VIEW
psql -h localhost -U kxuser -d llm_gateway -c "
  \d+ request_logs_with_current_month
"
```

### Step 4: 重启本地服务

```bash
# 停止服务
systemctl stop llm-gateway
# 或
pkill -f llm-gateway

# 重新构建
go build -o llm-gateway cmd/gateway/main.go

# 启动服务
./llm-gateway &

# 观察日志
tail -f __SERVER_PATH_6__.log | grep "request_logs\|partition"
```

### Step 5: 实际业务测试

```bash
# 发送真实请求
curl -X POST http://localhost:__PORT_12__/v1/chat/completions \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "测试 Migration 341"}]
  }'

# 验证写入
psql -h localhost -U kxuser -d llm_gateway -c "
  SELECT request_id, ts, client_model, success
  FROM request_logs_hot
  ORDER BY ts DESC LIMIT 5;
"
```

---

## 故障回滚

如果测试失败，执行回滚：

```bash
# 应用回滚 migration
psql -h localhost -U kxuser -d llm_gateway < db/migrations/341_hot_table_independence.down.sql

# 恢复代码
git checkout HEAD -- domains/hooks/observability/telemetry/client.go admin/*.go bg/partition_manager.go

# 重启服务
systemctl restart llm-gateway
```

---

## 184 环境部署准备

### 前置条件（全部满足才能继续）

- [ ] 本地测试 100% 通过
- [ ] 服务重启后写入正常
- [ ] Promote 函数验证工作
- [ ] 查询性能符合预期
- [ ] 本地运行稳定 24 小时以上

### 184 部署步骤（待用户批准）

```bash
# 1. 连接 184
export PGHOST=__DOMAIN_6__
export PGUSER=kxuser
export PGDATABASE=llm_gateway

# 2. 备份
pg_dump -h __DOMAIN_6__ -U kxuser llm_gateway > backup_184_before_341.sql

# 3. 应用 migration
psql -h __DOMAIN_6__ < db/migrations/341_hot_table_independence.sql

# 4. 验证
./scripts/partition/check-partition-health.sh --env 184

# 5. 部署代码
git push origin main  # 触发 CI/CD 到 184
```

---

## 检查清单

### 测试完成

- [ ] 自动化测试通过
- [ ] 手动验证通过
- [ ] 服务重启成功
- [ ] 实际业务请求写入成功
- [ ] 查询返回正确数据
- [ ] Promote 函数正常工作
- [ ] 性能符合预期

### 代码提交

- [ ] Git commit 完成
- [ ] 代码已 push 到远程仓库
- [ ] 未触发 71 环境自动部署

### 文档更新

- [ ] 本测试记录填写完整
- [ ] 性能基准数据记录
- [ ] 问题和解决方案记录

---

**测试负责人**: _______________  
**测试日期**: _______________  
**批准部署到 184**: ⬜ 是 / ⬜ 否  
**批准部署到 71**: ⬜ 是 / ⬜ 否（需用户明确命令）

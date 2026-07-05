# 分区表架构审计与修复 - 本地+184环境快速指南

## 🎯 快速执行

### 1. 验证当前状态

```bash
cd __LOCAL_PATH_1__

# 检查本地和184环境的架构状态
./scripts/verify_partition_architecture.sh
```

这个脚本会检查：
- ✅ 本地数据库的hot表架构
- ✅ 184环境数据库的hot表架构
- ✅ 服务健康状态
- ✅ 代码是否正确使用hot表

### 2. 执行迁移

#### 本地环境
```bash
# 应用所有3个迁移到本地数据库
./scripts/apply_hot_table_migrations_v2.sh local
```

#### 184环境
```bash
# 应用所有3个迁移到184服务器
./scripts/apply_hot_table_migrations_v2.sh 184
```

### 3. 再次验证
```bash
# 确认迁移成功
./scripts/verify_partition_architecture.sh
```

---

## 📋 检查清单

### 执行前检查
- [ ] 本地数据库可连接
- [ ] 184服务器SSH可访问
- [ ] 184数据库可连接
- [ ] 代码已修改（registry/usage_stats.go）

### 执行后验证
- [ ] 所有hot表创建成功
- [ ] 所有VIEW创建成功
- [ ] 所有promote函数创建成功
- [ ] _default分区已删除
- [ ] 数据迁移完整
- [ ] 集成测试通过

---

## 🔧 环境配置

### 本地环境变量（可选）
```bash
export LOCAL_DB_HOST=localhost
export LOCAL_DB_PORT=__PORT_5__
export LOCAL_DB_USER=postgres
export LOCAL_DB_NAME=llm_gateway
export LOCAL_SERVICE_URL=http://localhost:__PORT_12__
```

### 184环境变量（可选）
```bash
export REMOTE_184_HOST=10.0.0.184
export REMOTE_184_DB_HOST=10.0.0.184
export REMOTE_184_DB_PORT=__PORT_5__
export REMOTE_184_DB_USER=postgres
export REMOTE_184_DB_NAME=llm_gateway
export REMOTE_184_SERVICE_URL=http://10.0.0.184:__PORT_12__
```

如果需要密码：
```bash
export PGPASSWORD=your_password
```

---

## 📊 验证脚本输出示例

### ✅ 正常输出
```
==========================================
  分区表架构验证
  时间: 2026-07-05 10:30:00
==========================================

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  1️⃣  本地环境检查
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

[INFO] 检查 本地 数据库连接...
[✓] 本地 数据库连接正常
[INFO] 检查 本地 的hot表...
[✓]   request_logs_hot 存在
[✓]   usage_ledger_hot 存在
[✓]   credit_ledger_hot 存在
[✓]   tool_usage_stats_hot 存在
...
[✓] 本地环境: 架构完整 ✓
[✓] 184环境: 架构完整 ✓
[✓] 🎉 所有环境检查通过！
```

### ⚠️ 需要迁移的输出
```
[!]   tool_usage_stats_hot 不存在（可能需要迁移）
[!]   credit_ledger_hot 不存在（可能需要迁移）
[!]   request_logs_bodies_hot 不存在（可能需要迁移）
[!] 本地 缺少 3 个hot表: tool_usage_stats_hot credit_ledger_hot request_logs_bodies_hot

[!] 本地环境: 需要执行迁移或修复
  运行: ./scripts/apply_hot_table_migrations_v2.sh local
```

---

## 🛠️ 故障排查

### 问题1: 无法连接本地数据库
**解决**:
```bash
# 检查PostgreSQL是否运行
pg_isready -h localhost -p __PORT_5__

# 检查数据库是否存在
psql -h localhost -U postgres -l | grep llm_gateway

# 如果端口不是__PORT_5__，设置环境变量
export LOCAL_DB_PORT=你的端口
```

### 问题2: 无法SSH到184服务器
**解决**:
```bash
# 测试SSH连接
ssh 10.0.0.184 "echo 'SSH OK'"

# 如果需要密钥
ssh -i ~/.ssh/your_key 10.0.0.184

# 在脚本中使用不同的主机
export REMOTE_184_HOST=your_custom_host
```

### 问题3: 184数据库连接失败
**解决**:
```bash
# 测试数据库连接
ssh 10.0.0.184 "psql -h localhost -U postgres -d llm_gateway -c 'SELECT 1'"

# 设置密码
export PGPASSWORD=your_password
```

### 问题4: 迁移执行失败
**解决**:
1. 查看错误信息，确定是哪个表失败
2. 手动检查该表的状态
```sql
\dt tool_usage_stats*
\dv tool_usage_stats*
```
3. 如果表已部分创建，可以手动清理后重试
4. 查看迁移日志：`ls -la scripts/test_results_*.log`

---

## 📝 手动检查命令

### 检查hot表
```bash
psql -h localhost -U postgres -d llm_gateway -c "
SELECT tablename, pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename))
FROM pg_tables 
WHERE tablename LIKE '%_hot' 
ORDER BY tablename;
"
```

### 检查VIEW
```bash
psql -h localhost -U postgres -d llm_gateway -c "
\dv *_with_current_month
"
```

### 检查数据量
```bash
psql -h localhost -U postgres -d llm_gateway -c "
SELECT 
  'tool_usage_stats_hot' as table,
  count(*) as rows
FROM tool_usage_stats_hot
UNION ALL
SELECT 'credit_ledger_hot', count(*) FROM credit_ledger_hot
UNION ALL
SELECT 'request_logs_bodies_hot', count(*) FROM request_logs_bodies_hot;
"
```

---

## 🔄 回滚方案

如果迁移后出现问题，可以回滚：

### 本地回滚
```bash
psql -h localhost -U postgres -d llm_gateway <<EOF
BEGIN;
-- 以tool_usage_stats为例
ALTER TABLE tool_usage_stats ATTACH PARTITION tool_usage_stats_default DEFAULT;
INSERT INTO tool_usage_stats_default SELECT * FROM tool_usage_stats_hot ON CONFLICT DO NOTHING;
DROP TABLE tool_usage_stats_hot CASCADE;
COMMIT;
EOF
```

### 184回滚
```bash
ssh 10.0.0.184 "psql -h localhost -U postgres -d llm_gateway" <<EOF
-- 同上SQL
EOF
```

---

## 📚 相关文档

- **完整审计报告**: `docs/partition-table-audit-2026-07-05.md`
- **修复总结**: `docs/partition-table-fix-summary.md`
- **最终报告**: `docs/PARTITION_MIGRATION_FINAL_REPORT.md`
- **快速指南**: `docs/PARTITION_MIGRATION_QUICKSTART.md`

---

## ✅ 执行流程总结

```
1. 验证当前状态
   └─> ./scripts/verify_partition_architecture.sh

2. 如果需要迁移
   ├─> 本地: ./scripts/apply_hot_table_migrations_v2.sh local
   └─> 184: ./scripts/apply_hot_table_migrations_v2.sh 184

3. 再次验证
   └─> ./scripts/verify_partition_architecture.sh

4. 监控运行
   ├─> 检查日志
   ├─> 监控hot表数据量
   └─> 验证promote函数运行
```

---

**最后更新**: 2026-07-05  
**维护者**: LLM Gateway OPS Team

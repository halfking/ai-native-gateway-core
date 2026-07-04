# ⚠️ 本文件已废弃 / This File Is Deprecated

> **归档日期**: 2026-07-05
> **替代文档**: [`docs/partition/MONTHLY_CHECKLIST.md`](../../docs/partition/MONTHLY_CHECKLIST.md) 第 §部署前分区归档验证清单 节
> **原因**: 内容已合并到主月度维护清单（部署前验证、API 端点测试、回滚计划）
> **保留原因**: 提供历史归档追溯

---

# 分区表列存储归档功能 - 部署检查清单

## 部署前检查

### 1. 代码部署
- [ ] 拉取最新代码：`git pull origin main`
- [ ] 验证提交存在：`git log --oneline | grep "feat(admin): add partition table columnar archive"`
- [ ] 检查文件完整性：
  ```bash
  ls -la admin/data_lifecycle_partition.go
  ls -la db/migrations/305_partition_archive_functions.sql
  ls -la docs/data-lifecycle-partition-archive.md
  ```

### 2. 环境准备
- [ ] 确认数据库连接正常
- [ ] 确认 Citus columnar 扩展已安装：
  ```sql
  SELECT * FROM pg_extension WHERE extname = 'citus_columnar';
  ```
- [ ] 备份数据库（建议）：
  ```bash
  pg_dump -h $DB_HOST -U $DB_USER -d llm_gateway > backup_before_migration_305.sql
  ```

### 3. 构建和测试
- [ ] 编译项目：`go build ./cmd/gateway`
- [ ] 运行单元测试：`go test ./admin -run TestPartition -v`
- [ ] 检查测试通过

## 部署步骤

### Step 1: 应用数据库迁移

**手动执行（推荐，可控性强）**：
```bash
# 连接到数据库
psql -h $DB_HOST -U $DB_USER -d llm_gateway

# 检查当前迁移状态
SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 5;

# 应用 Migration 305
\i db/migrations/305_partition_archive_functions.sql
```

**或者让服务自动执行**：
- 服务启动时会自动应用未执行的迁移

### Step 2: 验证数据库对象

```sql
-- 验证归档函数存在
\df archive_request_*

-- 应该看到：
-- archive_request_logs(date)
-- archive_request_wal(date)

-- 验证归档表存在
\d request_wal_archive

-- 验证分区创建函数
\df ensure_request_*
```

### Step 3: 重启服务

```bash
# 方式 1: systemd
sudo systemctl restart llm-gateway-go

# 方式 2: docker
docker restart llm-gateway-go

# 方式 3: kubernetes
kubectl rollout restart deployment/llm-gateway-go -n production
```

### Step 4: 验证服务启动

```bash
# 检查服务状态
sudo systemctl status llm-gateway-go

# 检查日志
tail -f /var/log/llm-gateway.log | grep -E "partition|archive"

# 检查健康端点
curl http://localhost:8080/health
```

### Step 5: 验证 API 端点

**使用验证脚本**：
```bash
export ADMIN_TOKEN=your_admin_token_here
export API_BASE_URL=https://llmgateway.internal.example.com
./scripts/deploy-verify-partition-archive.sh
```

**或手动验证**：

1. **查询分区状态**：
```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions | jq .
```

预期输出：包含 `request_logs` 和 `request_wal` 的分区信息

2. **测试试运行归档**：
```bash
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":true}' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive | jq .
```

预期输出：`status: "dry_run"` 或 `status: "skipped"`（如果分区不存在）

### Step 6: 功能测试

1. **访问管理界面**：
   - URL: https://llmgateway.internal.example.com/admin/data-lifecycle
   - 确认页面正常加载（前端需要单独开发，目前仅 API）

2. **测试权限控制**：
```bash
# 使用非 super_admin token 测试归档端点（应该失败）
curl -X POST \
  -H "Authorization: Bearer $NON_SUPER_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":true}' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive
```

预期：403 Forbidden

3. **执行真实归档测试（可选）**：
```bash
# 找一个确实存在的旧分区
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions | \
  jq '.[] | select(.table_name=="request_logs") | .partitions[] | select(.can_archive==true) | .partition_name' | head -1

# 假设找到 request_logs_2026_03，执行归档
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-03","dry_run":false}' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive | jq .
```

预期：`status: "success"`, `rows_migrated: <数字>`, `partition_dropped: true`

## 部署后验证

### 1. 监控检查
- [ ] 检查服务日志无错误
- [ ] 检查 Prometheus 指标正常
- [ ] 检查数据库连接池正常

### 2. 功能验证
- [ ] 分区查询端点返回正确数据
- [ ] 可归档分区数量合理
- [ ] 归档表大小符合预期

### 3. 性能验证
- [ ] API 响应时间 < 5秒（分区查询）
- [ ] 归档操作完成时间合理（100万行 < 5分钟）
- [ ] 无内存泄漏或异常资源占用

## 回滚计划

如果部署出现问题，执行以下回滚步骤：

### Option 1: 仅回滚代码（保留数据库更改）
```bash
# 回到上一个提交
git revert cec00d34

# 重新构建和部署
go build ./cmd/gateway
sudo systemctl restart llm-gateway-go
```

### Option 2: 完全回滚（包括数据库）
```bash
# 1. 回滚代码
git revert cec00d34

# 2. 回滚数据库迁移
psql -h $DB_HOST -U $DB_USER -d llm_gateway \
  -f db/migrations/305_partition_archive_functions.down.sql

# 3. 重新构建和部署
go build ./cmd/gateway
sudo systemctl restart llm-gateway-go
```

**注意**：如果已经有数据在 `request_wal_archive` 表中，回滚脚本会拒绝删除该表以防止数据丢失。

## 常见问题

### Q1: 迁移执行失败
**症状**：Migration 305 执行时报错

**排查**：
```sql
-- 检查是否有语法错误
\i db/migrations/305_partition_archive_functions.sql

-- 检查权限
SELECT current_user, session_user;

-- 检查 columnar 扩展
SELECT * FROM pg_extension WHERE extname = 'citus_columnar';
```

### Q2: API 返回 500 错误
**症状**：调用 `/api/admin/data-lifecycle/partitions` 返回 500

**排查**：
```bash
# 检查服务日志
tail -100 /var/log/llm-gateway.log | grep -i error

# 检查数据库连接
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "SELECT 1"
```

### Q3: 归档操作卡住
**症状**：归档请求超时或长时间无响应

**排查**：
```sql
-- 检查是否有锁等待
SELECT * FROM pg_stat_activity WHERE state = 'active';

-- 检查长时间运行的查询
SELECT pid, now() - query_start AS duration, query 
FROM pg_stat_activity 
WHERE query NOT LIKE '%pg_stat_activity%' 
ORDER BY duration DESC;
```

## 监控和告警

### 推荐告警规则

```yaml
# Prometheus alert rules
groups:
  - name: partition_archive
    rules:
      - alert: PartitionArchiveBacklog
        expr: llmgw_partition_archivable_count > 3
        for: 24h
        labels:
          severity: warning
        annotations:
          summary: "Partition archive backlog detected"
          description: "Table {{ $labels.table }} has {{ $value }} partitions eligible for archive"
```

### 日志监控
```bash
# 监控归档操作
tail -f /var/log/llm-gateway.log | grep "partition_manager\|archive"

# 查看最近的归档成功记录
grep "archive succeeded" /var/log/llm-gateway.log | tail -10
```

## 文档更新

部署完成后，更新相关文档：
- [ ] 在运维文档中记录部署时间和负责人
- [ ] 更新架构图（如有必要）
- [ ] 在团队知识库中分享使用指南

## 签名确认

- **部署执行人**：_________________ 日期：_______
- **验证确认人**：_________________ 日期：_______
- **上线审批人**：_________________ 日期：_______

---

## 附录：快速命令参考

```bash
# 部署验证一键脚本
export ADMIN_TOKEN=xxx
./scripts/deploy-verify-partition-archive.sh

# 查看可归档分区
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions | \
  jq '.[] | {table: .table_name, archivable: .archivable_count}'

# 执行单次归档（试运行）
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":true}' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive | jq .

# 批量归档（试运行）
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","months":["2026-02","2026-03","2026-04"],"dry_run":true}' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive-batch | jq .
```

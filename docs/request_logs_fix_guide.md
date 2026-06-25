# Request Logs 修复文档

## 问题描述

**症状：**
1. 同一个 `request_id` 在 `request_logs` 表中重复出现多次（通常5次左右）
2. 所有记录的 `request_status` 都卡在 `'in_progress'` 状态
3. 成功的请求没有被更新为 `'success'` 状态

**根本原因：**

系统使用 `(request_id, ts)` 作为 UPSERT 的冲突键，其中 `ts = now()` 在每次 INSERT 时都不同。这导致：

1. **初始 INSERT**：创建记录，`ts = T1`，`status = 'in_progress'`
2. **UPDATE 尝试**：批处理延迟200ms后执行，但 CTE 查询 `ORDER BY ts DESC` 可能找错行
3. **UPDATE 失败**：`RowsAffected() = 0`
4. **Fallback INSERT**：创建新记录，`ts = T2`
5. **无限循环**：重复步骤2-4，产生多条记录

## 修复方案

### 修改内容

#### 1. 禁用 fallback INSERT (`telemetry/client.go` line 904-925)

**修改前：**
```go
if tag.RowsAffected() == 0 {
    // No early row — fall back to insert so the request is not lost.
    if rbErr := tx.Rollback(ctx); rbErr != nil {
        slog.Warn("telemetry update rollback failed", "request_id", entry.RequestID, "error", rbErr)
    }
    fallback := *entry
    fallback.Op = RequestLogInsert
    return c.insertRequestLog(&fallback)
}
```

**修改后：**
```go
if tag.RowsAffected() == 0 {
    // 2026-06-26: Disable fallback INSERT to prevent duplicate records.
    // If we truly have no initial row, we log a warning but do not create
    // duplicate rows. Missing request is better than 5 duplicate 'in_progress' rows.
    slog.Warn("telemetry update matched zero rows, skipping fallback insert",
        "request_id", entry.RequestID,
        "success", entry.Success,
        "request_status", strPtrValue(entry.RequestStatus))
    return tx.Commit(ctx)
}
```

#### 2. 改进 UPDATE 查询 (`telemetry/client.go` line 740-826)

**修改前：**
```sql
WITH latest AS (
    SELECT id, ts
    FROM request_logs
    WHERE request_id = $1
    ORDER BY ts DESC  -- 查找最新记录
    LIMIT 1
)
UPDATE request_logs rl
...
FROM latest
WHERE rl.id = latest.id AND rl.ts = latest.ts
```

**修改后：**
```sql
WITH earliest AS (
    SELECT id, ts
    FROM request_logs
    WHERE request_id = $1
    ORDER BY ts ASC  -- 查找最早记录
    LIMIT 1
)
UPDATE request_logs rl
...
FROM earliest
WHERE rl.id = earliest.id AND rl.ts = earliest.ts
```

#### 3. 修改 UPSERT 冲突键 (`telemetry/client.go` line 497)

**修改前：**
```sql
ON CONFLICT (request_id, ts) DO UPDATE SET ...
```

**修改后：**
```sql
ON CONFLICT (request_id) DO UPDATE SET ...
```

#### 4. 数据库迁移 (`db/migrations/301_*.sql`)

创建了两个迁移文件：
- `301_request_logs_unique_request_id_only.sql` - 正向迁移
- `301_request_logs_unique_request_id_only.down.sql` - 回滚脚本

迁移内容：
1. 删除旧的 `(request_id, ts)` 唯一索引
2. 清理现有重复记录（保留最早的）
3. 创建新的 `(request_id)` 唯一索引

## 部署步骤

### 1. 代码部署

已完成的代码修改会自动包含在下次部署中。

### 2. 数据库迁移

**选项A：自动迁移（如果有迁移工具）**

```bash
# 如果使用 migrate 工具
migrate -path db/migrations -database "$DATABASE_URL" up

# 如果使用 golang-migrate
migrate -source file://db/migrations -database "$DATABASE_URL" up
```

**选项B：手动执行（推荐）**

```bash
# 1. 备份当前数据（可选但强烈推荐）
pg_dump -t request_logs "$DATABASE_URL" > request_logs_backup_$(date +%Y%m%d).sql

# 2. 应用迁移
psql "$DATABASE_URL" -f db/migrations/301_request_logs_unique_request_id_only.sql

# 3. 验证迁移结果
psql "$DATABASE_URL" -f db/scripts/diagnose_and_clean_request_logs.sql
```

### 3. 验证修复

运行诊断脚本检查：

```bash
psql "$DATABASE_URL" -f db/scripts/diagnose_and_clean_request_logs.sql
```

**预期结果：**
- ✓ No duplicates found
- ✓ All success=true requests have status=success
- 新的索引 `idx_request_logs_request_id_unique` 存在

### 4. 监控

部署后监控以下指标：

1. **重复记录数量**
   ```sql
   SELECT COUNT(*) - COUNT(DISTINCT request_id) as duplicates
   FROM request_logs
   WHERE ts > now() - interval '1 hour';
   ```
   预期：0

2. **状态分布**
   ```sql
   SELECT request_status, COUNT(*) 
   FROM request_logs
   WHERE ts > now() - interval '1 hour'
   GROUP BY request_status;
   ```
   预期：看到 'success' 和 'failure' 状态，不只是 'in_progress'

3. **UPDATE 失败警告日志**
   ```bash
   grep "telemetry update matched zero rows" /var/log/gateway.log
   ```
   预期：很少或没有

## 回滚计划

如果迁移出现问题，可以回滚：

```bash
# 回滚数据库迁移
psql "$DATABASE_URL" -f db/migrations/301_request_logs_unique_request_id_only.down.sql

# 回滚代码到上一个版本
git revert <commit-hash>
```

## 注意事项

### 清理历史数据

迁移只清理最近7天的重复记录。如果需要清理更旧的数据：

```sql
-- 检查旧数据中的重复情况
SELECT COUNT(*) - COUNT(DISTINCT request_id) as old_duplicates
FROM request_logs
WHERE ts < now() - interval '7 days';

-- 如果有重复，运行清理脚本（分批执行）
-- 见 db/scripts/diagnose_and_clean_request_logs.sql 第8部分
```

## 测试建议

### 功能测试

1. **正常请求流程**
   ```bash
   # 发送测试请求
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Authorization: Bearer $API_KEY" \
     -H "Content-Type: application/json" \
     -d '{
       "model": "gpt-4",
       "messages": [{"role": "user", "content": "Hello"}]
     }'
   
   # 检查 request_logs
   psql "$DATABASE_URL" -c "
     SELECT request_id, request_status, success 
     FROM request_logs 
     WHERE ts > now() - interval '1 minute'
     ORDER BY ts DESC
     LIMIT 5;
   "
   ```
   预期：每个 request_id 只有1条记录，状态为 'success'

2. **失败请求流程**
   ```bash
   # 发送无效请求
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Authorization: Bearer invalid_key" \
     -H "Content-Type: application/json" \
     -d '{"model": "gpt-4", "messages": []}'
   
   # 检查记录
   psql "$DATABASE_URL" -c "
     SELECT request_id, request_status, error_kind 
     FROM request_logs 
     WHERE ts > now() - interval '1 minute'
     AND success = false;
   "
   ```
   预期：状态为 'failure'，有 error_kind

3. **并发请求压力测试**
   ```bash
   # 使用 ab 或 hey 进行压测
   hey -n 1000 -c 50 -m POST \
     -H "Authorization: Bearer $API_KEY" \
     -H "Content-Type: application/json" \
     -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' \
     http://localhost:8080/v1/chat/completions
   
   # 检查是否有重复
   psql "$DATABASE_URL" -c "
     SELECT COUNT(*) - COUNT(DISTINCT request_id) as duplicates
     FROM request_logs
     WHERE ts > now() - interval '5 minutes';
   "
   ```
   预期：duplicates = 0

### 监控指标

在生产环境部署后，关注：

1. **日志告警**：`telemetry update matched zero rows` 出现频率
2. **数据库性能**：新索引对查询性能的影响
3. **请求完整性**：确保没有请求丢失

## 相关文件

- `telemetry/client.go` - 核心逻辑修复
- `db/migrations/301_request_logs_unique_request_id_only.sql` - 数据库迁移
- `db/migrations/301_request_logs_unique_request_id_only.down.sql` - 回滚脚本
- `db/scripts/diagnose_and_clean_request_logs.sql` - 诊断和清理工具

## 联系信息

如有问题，请联系：
- 开发负责人：[your-name]
- 修复日期：2026-06-26
- Git commit: [待添加]

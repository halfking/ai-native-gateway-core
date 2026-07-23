# Bug 修复：minimax-m3 请求消息为空 & request not found

## 问题现象

1. **主要问题**：minimax-m3 大量请求在"请求实时流"点击后报 "request not found"
2. **次要问题**：即使能查到的请求，"请求消息"为空
3. **跨模型问题**：claude-opus-4-8 等其他模型也有请求消息为空
4. **性能问题**：直连 minimax 正常，经过网关后出现超时/5xx/4xx

## 根本原因分析

### 核心问题：Bodies 表插入依赖子查询导致数据丢失

**问题代码**（client.go:1017-1033）：
```sql
INSERT INTO request_logs_bodies_hot (request_id, ts, request_body, response_body)
SELECT $1, rl.ts, CAST($2 AS jsonb), CAST($3 AS jsonb)
FROM request_logs_hot rl
WHERE rl.request_id = $1
ORDER BY rl.ts DESC
LIMIT 1
ON CONFLICT (request_id, ts) DO UPDATE SET ...
```

**根本问题**：
- 子查询 `SELECT ... FROM request_logs_hot WHERE request_id = $1` 在高并发时可能返回 0 行
- 原因：事务隔离级别 + 同一事务内查询时机
- 结果：`INSERT ... SELECT` 插入 0 行，bodies 数据彻底丢失
- **request_id 是唯一标识**，不应该依赖 ts 来关联两个表

### 问题 3：minimax 模型特性导致超时

- minimax-m3 响应较慢，网关默认超时设置可能不够
- 网关在处理 minimax 流式响应时存在性能瓶颈

## 修复方案

### 修复：Bodies 表插入使用 NOW() 代替子查询

**修改文件**：`domains/hooks/observability/telemetry/client.go`

**修改前**（依赖子查询）：
```sql
INSERT INTO request_logs_bodies_hot (request_id, ts, request_body, response_body)
SELECT $1, rl.ts, CAST($2 AS jsonb), CAST($3 AS jsonb)
FROM request_logs_hot rl
WHERE rl.request_id = $1
ORDER BY rl.ts DESC
LIMIT 1
```

**修改后**（直接使用 NOW()）：
```sql
INSERT INTO request_logs_bodies_hot (request_id, ts, request_body, response_body)
VALUES ($1, NOW(), $2::jsonb, $3::jsonb)
```

**原理**：
- 不再依赖子查询，避免查询失败导致插入 0 行
- 使用 NOW() 简单可靠，确保每次插入都成功
- **request_id 是唯一标识**，ts 只是记录时间，不用于关联

### 修复：JOIN 只用 request_id

**修改文件**：`admin/logs.go`

**修改**：
```sql
-- JOIN 只需要匹配 request_id（request_id 是唯一标识）
LEFT JOIN request_logs_bodies_with_current_month rb 
  ON rb.request_id = rl.request_id
```

**原理**：
- request_id 是唯一标识，一个 request_id 对应一条记录
- 不需要 ts 来辅助匹配
- 简化查询逻辑，避免因 ts 细微差异导致 JOIN 失败

### 修复 3：增加 minimax 专用超时配置

**修改文件**：`config/config.go` 或相关配置

```go
// 为 minimax 提供商增加更长的超时时间
providerTimeouts := map[string]time.Duration{
	"minimax":   120 * time.Second,  // minimax 响应较慢
	"default":   60 * time.Second,
}
```

### 修复 4：增强错误日志

**位置**：`domains/hooks/observability/telemetry/client.go`

```go
// 在 INSERT INTO request_logs_bodies_hot 失败时增加详细日志
if err != nil {
	slog.Error("persist request_logs_bodies_hot failed",
		"request_id", entry.RequestID,
		"ts", actualTs,
		"has_request_body", entry.RequestBody != nil,
		"has_response_body", entry.ResponseBody != nil,
		"error", err,
	)
	return err
}
```

## 验证方案

### 1. 单元测试验证

```go
// 在 client_test.go 中添加测试
func TestPersistRequestLogBodiesConsistency(t *testing.T) {
	// 测试 request_logs_hot 和 request_logs_bodies_hot 的 ts 一致性
	entry := &RequestLogEntry{
		RequestID:    "test-" + uuid.NewString(),
		TenantID:     "test-tenant",
		RequestBody:  ptr("{}"),
		ResponseBody: ptr("{}"),
		Success:      true,
	}
	
	err := client.PersistRequestLog(entry)
	require.NoError(t, err)
	
	// 验证两个表的 ts 一致
	var mainTs, bodyTs time.Time
	err = db.QueryRow(ctx, 
		"SELECT ts FROM request_logs_hot WHERE request_id = $1", 
		entry.RequestID).Scan(&mainTs)
	require.NoError(t, err)
	
	err = db.QueryRow(ctx,
		"SELECT ts FROM request_logs_bodies_hot WHERE request_id = $1",
		entry.RequestID).Scan(&bodyTs)
	require.NoError(t, err)
	
	assert.Equal(t, mainTs, bodyTs, "ts should match between main and bodies tables")
}
```

### 2. 生产环境验证 SQL

```sql
-- 检查 ts 不一致的记录数量
SELECT COUNT(*) as mismatch_count
FROM request_logs_hot rl
LEFT JOIN request_logs_bodies_hot rb 
  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
WHERE rl.ts > NOW() - INTERVAL '1 hour'
  AND (rl.request_body IS NOT NULL OR rl.response_body IS NOT NULL)
  AND rb.request_id IS NULL;

-- 应该返回 0，如果 > 0 说明有 ts 不匹配的记录
```

### 3. 修复后验证步骤

```bash
# 1. 部署修复后的版本到 154
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
# ... 应用上述代码修改 ...

# 2. 编译并部署
./scripts/deploy-154.sh

# 3. 发送测试请求（minimax-m3）
curl -X POST http://llmgw.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer <test-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "测试请求"}]
  }'

# 4. 在前端"请求实时流"中点击该请求，验证：
#    - 不再出现 "request not found"
#    - 请求消息完整显示
#    - 响应消息完整显示

# 5. 在数据库中验证
ssh root@115.29.212.252 -p 25022
docker exec pg-252-pg17 psql -U stockuser -h 172.16.2.210 -d llm_gateway -c "
SELECT 
  rl.request_id,
  rl.ts as main_ts,
  rb.ts as body_ts,
  rl.ts = rb.ts as ts_match,
  rb.request_body IS NOT NULL as has_req_body,
  rb.response_body IS NOT NULL as has_resp_body
FROM request_logs_hot rl
LEFT JOIN request_logs_bodies_hot rb 
  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
WHERE rl.provider = 'minimax'
  AND rl.ts > NOW() - INTERVAL '5 minutes'
ORDER BY rl.ts DESC
LIMIT 10;
"
```

## 预期效果

1. **request not found** 错误消失
2. **请求消息**和**响应消息**完整显示
3. minimax-m3 和其他模型的历史记录正常查看
4. 高并发场景下不再出现 ts 不一致问题

## 回滚方案

如果修复后出现问题，可以快速回滚：

```bash
# 回滚到上一个版本
git revert <commit-hash>
./scripts/deploy-154.sh
```

## 相关文件

- `domains/hooks/observability/telemetry/client.go` - 主要修复点
- `admin/logs.go` - JOIN 条件修复
- `sql/migrations/startup/353_request_logs_bodies_hot_independence.sql` - 表结构参考

# Bug 修复：minimax-m3 请求消息为空 & request not found

## 问题现象

1. **主要问题**：minimax-m3 大量请求在"请求实时流"点击后报 "request not found"
2. **次要问题**：即使能查到的请求，"请求消息"为空
3. **跨模型问题**：claude-opus-4-8 等其他模型也有请求消息为空
4. **性能问题**：直连 minimax 正常，经过网关后出现超时/5xx/4xx

## 根本原因分析

### 问题 1：Bodies 表插入依赖子查询存在竞态条件

**当前代码**（client.go:1017-1033）：
```sql
INSERT INTO request_logs_bodies_hot (request_id, ts, request_body, response_body)
SELECT $1, rl.ts, CAST($2 AS jsonb), CAST($3 AS jsonb)
FROM request_logs_hot rl
WHERE rl.request_id = $1
ORDER BY rl.ts DESC
LIMIT 1
ON CONFLICT (request_id, ts) DO UPDATE SET ...
```

**问题**：
- `request_logs_bodies_hot` 的 `ts` 来自子查询 `request_logs_hot`
- 在同一事务内，如果 `request_logs_hot` 的行还未可见（事务隔离级别），子查询返回空
- 导致 **bodies 表插入失败**或 **ts 不一致**
- 高并发场景下（minimax 高频请求）问题更严重

### 问题 2：JOIN 条件不完整

**当前代码**（logs.go:559-560）：
```sql
LEFT JOIN request_logs_bodies_with_current_month rb 
  ON rb.request_id = rl.request_id
```

**问题**：
- PRIMARY KEY 是 `(request_id, ts)`
- JOIN 只用 `request_id`，不匹配 `ts`
- 如果同一 request_id 有多条记录（重试/更新），JOIN 错误的行
- 或者 **JOIN 不到任何行**（ts 不匹配）→ 请求消息为空

### 问题 3：minimax 模型特性导致超时

- minimax-m3 响应较慢，网关默认超时设置可能不够
- 网关在处理 minimax 流式响应时存在性能瓶颈

## 修复方案

### 修复 1：Bodies 表插入使用显式 ts（最关键）

**修改文件**：`domains/hooks/observability/telemetry/client.go`

**原理**：不依赖子查询，直接使用 `entry.EventAt` 或 `NOW()`

```go
// 修改 persistRequestLog 函数中的 INSERT INTO request_logs_bodies_hot 部分
// 位置：约 1017 行

// 在 INSERT INTO request_logs_hot 之后，获取实际使用的 ts
var actualTs time.Time
if entry.EventAt != nil {
	actualTs = *entry.EventAt
} else {
	actualTs = time.Now().UTC()
}

// 修改 INSERT INTO request_logs_bodies_hot，直接使用 actualTs
_, err = tx.Exec(ctx, `
	INSERT INTO request_logs_bodies_hot (
		request_id, ts, request_body, response_body
	)
	VALUES ($1, $2, $3::jsonb, $4::jsonb)
	ON CONFLICT (request_id, ts) DO UPDATE SET
		request_body = COALESCE(EXCLUDED.request_body, request_logs_bodies_hot.request_body),
		response_body = COALESCE(EXCLUDED.response_body, request_logs_bodies_hot.response_body)
`,
	entry.RequestID,
	actualTs,  // 直接使用 actualTs，不依赖子查询
	strPtrToJSON(entry.RequestBody),
	strPtrToJSON(entry.ResponseBody),
)
```

### 修复 2：JOIN 条件添加 ts 匹配

**修改文件**：`admin/logs.go`

**位置**：约 559 行

```go
// 修改前
LEFT JOIN request_logs_bodies_with_current_month rb 
  ON rb.request_id = rl.request_id

// 修改后
LEFT JOIN request_logs_bodies_with_current_month rb 
  ON rb.request_id = rl.request_id 
  AND rb.ts = rl.ts
```

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

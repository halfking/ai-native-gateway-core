# Analytics 统计数据缺失问题 - 修复总结

## 问题分析

### 症状
两个 analytics 接口返回空数据：
1. `GET /api/admin/auto-route/analytics/matrix?window=7d&metric=count&row=task_type`
2. `GET /api/admin/auto-route/analytics/flow?window=7d`

### 根本原因

**问题1：`is_auto_request` 字段为 NULL**

在 `relay/request_log_pipeline.go` 的 `applyAutoRouteFields` 函数中：
- 对于 `IsAutoRequest=true` 的请求，字段被设置为 `true` ✓
- 对于 `IsAutoRequest=false` 的请求，字段保持为 `nil` (NULL) ✗

```go
// 修复前的代码
if !c.IsAutoRequest {
    return  // 直接返回，没有设置 entry.IsAutoRequest
}
entry.IsAutoRequest = boolPtr(true)
```

**问题2：SQL 三值逻辑导致查询失败**

在 `admin/analytics.go` 的查询条件中：
```sql
AND (
  is_auto_request = TRUE
  OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
)
```

当 `is_auto_request` 为 `NULL` 时：
- `is_auto_request = TRUE` → FALSE（不匹配）
- `is_auto_request = FALSE` → NULL（不匹配，因为 NULL = FALSE 结果是 NULL）
- 整个 OR 条件为 FALSE → 该行被过滤掉

**结果**：所有指定模型的请求（非auto请求）都被排除在统计之外。

## 修复方案

### 1. 代码修复

**文件**: `relay/request_log_pipeline.go`

```go
func applyAutoRouteFields(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil {
		return
	}
	applyWorkTypeField(entry, c)
	if !c.IsAutoRequest {
		// Fix: explicitly set is_auto_request=false for non-auto requests
		entry.IsAutoRequest = boolPtr(false)
		return
	}
	entry.IsAutoRequest = boolPtr(true)
	// ... rest of the function
}
```

**变更说明**：
- 显式设置 `is_auto_request = false` 而不是保持 NULL
- 确保 SQL 查询条件 `is_auto_request = FALSE` 能够匹配这些行

### 2. 数据库迁移

**文件**: `db/migrations/302_fix_is_auto_request_null.sql`

修复历史数据中的 NULL 值：
```sql
-- 策略1: 根据 client_model 和 auto_profile 判断
UPDATE request_logs
SET is_auto_request = FALSE
WHERE is_auto_request IS NULL
  AND client_model IS NOT NULL
  AND client_model <> ''
  AND (auto_profile IS NULL OR auto_profile = '')
  AND ts >= NOW() - INTERVAL '30 days';

-- 策略2: 根据 routing_decision_log 交叉验证
UPDATE request_logs rl
SET is_auto_request = FALSE
FROM routing_decision_log rdl
WHERE rl.request_id = rdl.request_id
  AND rl.is_auto_request IS NULL
  AND rdl.client_model IS NOT NULL
  AND ts >= NOW() - INTERVAL '30 days';
```

### 3. 测试验证

**文件**: `relay/request_log_pipeline_auto_fix_test.go`

新增测试用例：
- `TestApplyAutoRouteFields_NonAutoRequest` - 验证非auto请求的字段设置
- `TestApplyAutoRouteFields_AutoRequest` - 验证auto请求的字段设置
- `TestApplyAutoRouteFields_WorkTypeOnly` - 验证work_type独立设置
- `TestBuildFailureEntry_NonAutoRequest` - 验证失败日志的字段设置

**更新的测试**: `relay/auto_route_pipeline_test.go`
- `TestApplyAutoRouteFields_SkipsWhenNotAutoRequest` - 更新期望值为 `false` 而不是 `nil`

## 测试结果

### 单元测试
```bash
$ go test -v ./relay -run TestApplyAutoRouteFields
=== RUN   TestApplyAutoRouteFields_PopulatesPromotedColumnsFromJSON
--- PASS: TestApplyAutoRouteFields_PopulatesPromotedColumnsFromJSON (0.00s)
=== RUN   TestApplyAutoRouteFields_SkipsWhenNotAutoRequest
--- PASS: TestApplyAutoRouteFields_SkipsWhenNotAutoRequest (0.00s)
=== RUN   TestApplyAutoRouteFields_HandlesEmptyJSON
--- PASS: TestApplyAutoRouteFields_HandlesEmptyJSON (0.00s)
=== RUN   TestApplyAutoRouteFields_HandlesMalformedJSON
--- PASS: TestApplyAutoRouteFields_HandlesMalformedJSON (0.00s)
=== RUN   TestApplyAutoRouteFields_HandlesZeroConfidence
--- PASS: TestApplyAutoRouteFields_HandlesZeroConfidence (0.00s)
=== RUN   TestApplyAutoRouteFields_NonAutoRequest
--- PASS: TestApplyAutoRouteFields_NonAutoRequest (0.00s)
=== RUN   TestApplyAutoRouteFields_AutoRequest
--- PASS: TestApplyAutoRouteFields_AutoRequest (0.00s)
=== RUN   TestApplyAutoRouteFields_NilContext
--- PASS: TestApplyAutoRouteFields_NilContext (0.00s)
=== RUN   TestApplyAutoRouteFields_WorkTypeOnly
--- PASS: TestApplyAutoRouteFields_WorkTypeOnly (0.00s)
PASS
ok  	github.com/kaixuan/llm-gateway-go/relay	0.584s
```

### 编译验证
```bash
$ go build -o bin/llm-gateway ./cmd/gateway
# 编译成功，无错误
```

## 本地测试步骤

### 步骤1: 应用数据库迁移
```bash
cd __DEV_HOME__/workspace/llm-gateway-go-2
./scripts/test_analytics_fix.sh
```

脚本会：
1. 检查当前 `is_auto_request` 字段分布
2. 应用数据库迁移修复 NULL 值
3. 验证修复后的数据分布
4. 测试 analytics 查询是否返回数据

### 步骤2: 启动本地服务
```bash
# 设置环境变量
export $(grep -v '^#' .env | xargs)

# 启动服务
./bin/llm-gateway
```

### 步骤3: 发送测试请求

**测试1: 发送 auto 请求**
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

**测试2: 发送指定模型请求**
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### 步骤4: 验证 request_logs
```bash
psql $LLM_GATEWAY_DATABASE_URL -c "
SELECT request_id, client_model, is_auto_request, task_type, ts 
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '5 minutes'
ORDER BY ts DESC
LIMIT 10;
"
```

**期望结果**：
- auto 请求: `is_auto_request = true`, `task_type` 有值
- 指定模型请求: `is_auto_request = false`, `task_type = NULL`

### 步骤5: 测试 analytics 接口

**测试 matrix 接口**:
```bash
curl -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/auto-route/analytics/matrix?window=7d&metric=count&row=task_type"
```

**测试 flow 接口**:
```bash
curl -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/auto-route/analytics/flow?window=7d"
```

**期望结果**：
- 返回 JSON 数据，包含 rows, cols, cells 等字段
- `__specified__` 行/列应该包含指定模型请求的统计
- 各种模型的统计数据应该正确显示

## 部署到服务器71

### 前置检查
```bash
# 1. 确认当前分支
git branch

# 2. 查看修改的文件
git status

# 3. 提交代码
git add relay/request_log_pipeline.go
git add relay/request_log_pipeline_auto_fix_test.go
git add relay/auto_route_pipeline_test.go
git add db/migrations/302_fix_is_auto_request_null.sql
git add scripts/test_analytics_fix.sh
git commit -m "fix(analytics): set is_auto_request=false explicitly for non-auto requests

Problem: Analytics endpoints returned empty data because is_auto_request
was NULL for specified-model requests, which didn't match the SQL condition
'is_auto_request = FALSE' due to SQL three-valued logic.

Solution:
1. Modified applyAutoRouteFields to explicitly set is_auto_request=false
   for non-auto requests instead of leaving it as nil
2. Added migration to fix historical NULL values in request_logs
3. Updated tests to verify the new behavior

Fixes: #routing-v2-analytics-no-data
"
```

### 部署步骤

**注意**: 用户要求先不部署到71，只在本地测试。部署命令备用：

```bash
# 推送代码
git push origin server-71

# SSH 到服务器71
ssh server-71

# 进入项目目录
cd /path/to/llm-gateway-go-2

# 拉取代码
git pull origin server-71

# 应用数据库迁移
psql $DATABASE_URL -f db/migrations/302_fix_is_auto_request_null.sql

# 重新编译
go build -o bin/llm-gateway ./cmd/gateway

# 重启服务（根据实际部署方式）
systemctl restart llm-gateway
# 或
supervisorctl restart llm-gateway

# 验证服务状态
curl http://localhost:8080/health
```

### 部署后验证

1. 检查服务日志
2. 调用 analytics 接口验证数据返回
3. 监控错误率和性能指标

## 影响范围

### 影响的表
- `request_logs` - 更新历史 NULL 值，新记录显式设置 false

### 影响的查询
- Analytics matrix 查询 - 现在能正确匹配非auto请求
- Analytics flow 查询 - 现在能正确匹配非auto请求
- 任何依赖 `is_auto_request` 字段的查询和统计

### 向后兼容性
- ✅ 完全向后兼容
- 查询逻辑不需要修改（SQL条件保持不变）
- 只是数据从 NULL 变为 FALSE，语义更清晰

## 总结

### 修改的文件
1. `relay/request_log_pipeline.go` - 核心修复
2. `relay/request_log_pipeline_auto_fix_test.go` - 新增测试
3. `relay/auto_route_pipeline_test.go` - 更新现有测试
4. `db/migrations/302_fix_is_auto_request_null.sql` - 数据修复
5. `scripts/test_analytics_fix.sh` - 测试脚本
6. `ANALYTICS_FIX_ANALYSIS.md` - 问题分析文档

### 预期效果
- ✅ Analytics 接口返回完整的统计数据
- ✅ 指定模型请求正确显示在 `__specified__` 分类中
- ✅ Auto 请求按 task_type 正确分类
- ✅ 所有历史数据（30天内）的 NULL 值被修复

### 风险评估
- **风险等级**: 低
- **影响范围**: 仅影响 telemetry 数据写入和 analytics 查询
- **回滚方案**: 如有问题可以直接回滚代码，数据库更新是幂等的

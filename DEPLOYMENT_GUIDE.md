# Analytics 统计数据缺失问题 - 修复完成报告

## 📋 执行摘要

**问题**: `/api/admin/auto-route/analytics/matrix` 和 `/api/admin/auto-route/analytics/flow` 两个接口返回空数据。

**根因**: 指定模型请求的 `is_auto_request` 字段为 `NULL`，导致 SQL 查询条件无法匹配（SQL 三值逻辑）。

**修复**: 显式设置非auto请求的 `is_auto_request = false`，并修复历史数据。

**状态**: ✅ 代码修复完成，✅ 测试通过，⏳ 等待本地验证和部署

---

## 🔍 问题深度分析

### 问题表现
```
用户报告: 有请求数据，但统计数据为空
├── request_logs 表有大量数据 ✓
├── routing_decision_log 表有决策记录 ✓
└── analytics 接口返回空数组 ✗
```

### 根本原因

**代码层面**：
```go
// relay/request_log_pipeline.go (修复前)
func applyAutoRouteFields(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
    if !c.IsAutoRequest {
        return  // ← 问题：没有设置 entry.IsAutoRequest
    }
    entry.IsAutoRequest = boolPtr(true)
}
```

**结果**：指定模型请求的 `is_auto_request` 字段为 `NULL`

**SQL层面**：
```sql
-- admin/analytics.go 中的查询条件
WHERE (
    is_auto_request = TRUE          -- 匹配auto请求 ✓
    OR (is_auto_request = FALSE AND ...)  -- 匹配指定模型请求
)

-- 但是当 is_auto_request 为 NULL 时：
-- NULL = FALSE 的结果是 NULL（而不是 TRUE 或 FALSE）
-- 导致整个 OR 条件失败，该行被过滤掉 ✗
```

**数据分布**（推测）：
- 总请求数: 100%
- Auto请求 (is_auto_request=TRUE): ~30%  → 能被统计 ✓
- 指定模型请求 (is_auto_request=NULL): ~70%  → 被错误过滤 ✗

---

## ✅ 修复方案

### 1. 代码修复

**文件**: `relay/request_log_pipeline.go` (第184-192行)

```go
func applyAutoRouteFields(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil {
		return
	}
	applyWorkTypeField(entry, c)
	if !c.IsAutoRequest {
		// Fix: explicitly set is_auto_request=false for non-auto requests
		// so analytics queries can distinguish them from NULL (未知状态).
		// Without this, SQL `is_auto_request = FALSE` won't match NULL rows.
		entry.IsAutoRequest = boolPtr(false)  // ← 关键修复
		return
	}
	entry.IsAutoRequest = boolPtr(true)
	// ... rest of auto-specific fields
}
```

**修改说明**：
- 非auto请求现在显式设置 `is_auto_request = false`
- SQL 查询条件 `is_auto_request = FALSE` 能正确匹配
- 数据语义更清晰：true=auto, false=指定模型, NULL=未知（不应存在）

### 2. 数据库迁移

**文件**: `db/migrations/302_fix_is_auto_request_null.sql`

修复历史数据（最近30天）：
```sql
-- 策略1: 根据字段特征判断
UPDATE request_logs
SET is_auto_request = FALSE
WHERE is_auto_request IS NULL
  AND client_model IS NOT NULL
  AND client_model <> ''
  AND (auto_profile IS NULL OR auto_profile = '')
  AND ts >= NOW() - INTERVAL '30 days';

-- 策略2: 从routing_decision_log交叉验证
UPDATE request_logs rl
SET is_auto_request = FALSE
FROM routing_decision_log rdl
WHERE rl.request_id = rdl.request_id
  AND rl.is_auto_request IS NULL
  AND rdl.client_model IS NOT NULL
  AND ts >= NOW() - INTERVAL '30 days';
```

### 3. 测试完善

**新增测试文件**: `relay/request_log_pipeline_auto_fix_test.go`
- `TestApplyAutoRouteFields_NonAutoRequest` - 验证非auto请求
- `TestApplyAutoRouteFields_AutoRequest` - 验证auto请求
- `TestApplyAutoRouteFields_NilContext` - 验证空指针安全
- `TestApplyAutoRouteFields_WorkTypeOnly` - 验证work_type独立性
- `TestBuildFailureEntry_NonAutoRequest` - 验证失败日志

**更新现有测试**: `relay/auto_route_pipeline_test.go`
- `TestApplyAutoRouteFields_SkipsWhenNotAutoRequest` - 更新期望值

**测试结果**: ✅ 所有9个测试通过

---

## 🧪 本地测试指南

### 前置条件

1. 确保数据库运行正常
2. 确认 `.env` 文件中的数据库连接配置
3. 有可用的 API Key 用于测试

### 测试步骤

#### 步骤1: 快速验证（无需数据库）

```bash
cd __DEV_HOME__/workspace/llm-gateway-go-2

# 运行快速测试脚本
./scripts/quick_test.sh
```

**期望输出**:
```
✓ All unit tests passed
✓ Code compiles successfully
✓ SQL test query created
```

#### 步骤2: 应用数据库迁移（需要数据库）

```bash
# 检查数据库连接
psql $LLM_GATEWAY_DATABASE_URL -c "SELECT version();"

# 查看当前is_auto_request分布
psql $LLM_GATEWAY_DATABASE_URL -c "
SELECT 
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE is_auto_request = TRUE) as auto_true,
    COUNT(*) FILTER (WHERE is_auto_request = FALSE) as auto_false,
    COUNT(*) FILTER (WHERE is_auto_request IS NULL) as auto_null
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '7 days';
"

# 应用迁移脚本
psql $LLM_GATEWAY_DATABASE_URL -f db/migrations/302_fix_is_auto_request_null.sql

# 验证迁移结果
psql $LLM_GATEWAY_DATABASE_URL -c "
SELECT 
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE is_auto_request = TRUE) as auto_true,
    COUNT(*) FILTER (WHERE is_auto_request = FALSE) as auto_false,
    COUNT(*) FILTER (WHERE is_auto_request IS NULL) as auto_null
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '7 days';
"
```

**期望结果**:
- `auto_null` 数量应该大幅减少或变为0
- `auto_false` 数量应该增加

#### 步骤3: 启动本地服务

```bash
# 设置环境变量
export $(grep -v '^#' .env | xargs)

# 启动服务
./bin/llm-gateway
```

#### 步骤4: 发送测试请求

**测试1: Auto请求**
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "写一个Python函数"}]
  }'
```

**测试2: 指定模型请求**
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

#### 步骤5: 验证request_logs

```bash
psql $LLM_GATEWAY_DATABASE_URL -c "
SELECT 
    request_id,
    client_model,
    outbound_model,
    is_auto_request,
    task_type,
    auto_profile,
    ts
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '5 minutes'
ORDER BY ts DESC
LIMIT 10;
"
```

**期望结果**:
- Auto请求: `is_auto_request = true`, `task_type` 有值
- 指定模型请求: `is_auto_request = false`, `task_type = NULL`

#### 步骤6: 测试Analytics接口

**测试Matrix接口**:
```bash
curl -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/auto-route/analytics/matrix?window=7d&metric=count&row=task_type" \
  | jq '.'
```

**测试Flow接口**:
```bash
curl -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  "http://localhost:8080/api/admin/auto-route/analytics/flow?window=7d" \
  | jq '.'
```

**期望结果**:
- 返回JSON数据（不是空数组）
- Matrix: `rows` 包含各种模型，`cols` 包含 task_type 和 `__specified__`
- Flow: `nodes` 和 `links` 数组有数据
- `__specified__` 分类包含指定模型请求的统计

#### 步骤7: 验证统计数据

```bash
# 验证matrix查询能找到数据
psql $LLM_GATEWAY_DATABASE_URL -c "
SELECT 
    COALESCE(NULLIF(outbound_model, ''), client_model) AS model,
    COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) AS category,
    COUNT(*) as count
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
  AND (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
  )
GROUP BY model, category
ORDER BY count DESC
LIMIT 20;
"
```

**期望结果**:
- 应该看到多行数据
- 包含 `__specified__` 分类的记录
- 各种模型都有统计数据

---

## 🚀 部署到服务器71

### 部署前检查

```bash
# 1. 确认所有修改
git status
git diff

# 2. 确认测试通过
./scripts/quick_test.sh

# 3. 确认分支正确
git branch
# 应该显示: * server-71
```

### 提交代码

```bash
git add relay/request_log_pipeline.go
git add relay/request_log_pipeline_auto_fix_test.go
git add relay/auto_route_pipeline_test.go
git add db/migrations/302_fix_is_auto_request_null.sql
git add scripts/test_analytics_fix.sh
git add scripts/quick_test.sh
git add *.md

git commit -m "fix(analytics): explicitly set is_auto_request=false for non-auto requests

Problem:
- Analytics endpoints returned empty data because is_auto_request was NULL
  for specified-model requests
- SQL condition 'is_auto_request = FALSE' didn't match NULL due to SQL
  three-valued logic

Solution:
1. Modified applyAutoRouteFields to explicitly set is_auto_request=false
   for non-auto requests instead of leaving it nil
2. Added migration 302_fix_is_auto_request_null.sql to fix historical data
3. Added comprehensive tests to verify the fix
4. Updated existing test expectations

Impact:
- Analytics queries now correctly include specified-model requests
- Data semantics are clearer (true=auto, false=specified, NULL=unknown)
- Fully backward compatible

Testing:
- All unit tests pass (9/9)
- Code compiles successfully
- Ready for local verification

Related: #routing-v2-analytics-no-data
"
```

### 推送代码

```bash
git push origin server-71
```

### 在服务器71上部署

```bash
# SSH 到服务器
ssh server-71  # 或使用实际的服务器地址

# 进入项目目录
cd /path/to/llm-gateway-go-2

# 拉取最新代码
git pull origin server-71

# 备份数据库（可选但推荐）
pg_dump $DATABASE_URL > backup_$(date +%Y%m%d_%H%M%S).sql

# 应用数据库迁移
psql $DATABASE_URL -f db/migrations/302_fix_is_auto_request_null.sql

# 重新编译
go build -o bin/llm-gateway ./cmd/gateway

# 重启服务（根据实际部署方式选择）
sudo systemctl restart llm-gateway
# 或
sudo supervisorctl restart llm-gateway
# 或停止旧进程并启动新进程

# 检查服务状态
sudo systemctl status llm-gateway
# 或
curl http://localhost:8080/health
```

### 部署后验证

```bash
# 1. 检查服务日志
tail -f /var/log/llm-gateway/app.log

# 2. 验证analytics接口
curl -H "Authorization: Bearer ADMIN_TOKEN" \
  "https://llmgateway.internal.example.com/api/admin/auto-route/analytics/matrix?window=7d&metric=count&row=task_type"

curl -H "Authorization: Bearer ADMIN_TOKEN" \
  "https://llmgateway.internal.example.com/api/admin/auto-route/analytics/flow?window=7d"

# 3. 检查数据库
psql $DATABASE_URL -c "
SELECT 
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE is_auto_request = TRUE) as auto_requests,
    COUNT(*) FILTER (WHERE is_auto_request = FALSE) as specified_requests,
    COUNT(*) FILTER (WHERE is_auto_request IS NULL) as null_values
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '1 hour';
"

# 4. 检查前端页面
# 访问 https://llmgateway.internal.example.com/routing-v2
# 应该能看到统计数据和图表
```

---

## 📊 预期效果

### 修复前
```json
{
  "rows": [],
  "cols": [],
  "cells": [],
  "meta": {
    "window": "7d",
    "metric": "count",
    "row": "task_type"
  }
}
```

### 修复后
```json
{
  "rows": ["gpt-4", "claude-3-opus", "gpt-3.5-turbo", ...],
  "cols": ["coding", "writing", "translation", "__specified__", ...],
  "cells": [
    [150, 80, 20, 300],  // gpt-4 的各类请求数
    [90, 120, 15, 450],  // claude-3-opus 的各类请求数
    ...
  ],
  "meta": {
    "window": "7d",
    "metric": "count",
    "row": "task_type",
    "row_aliases": {...}
  }
}
```

---

## 📝 修改文件清单

| 文件 | 类型 | 说明 |
|------|------|------|
| `relay/request_log_pipeline.go` | 修改 | 核心修复：显式设置is_auto_request=false |
| `relay/auto_route_pipeline_test.go` | 修改 | 更新测试期望值 |
| `relay/request_log_pipeline_auto_fix_test.go` | 新增 | 9个新测试用例 |
| `db/migrations/302_fix_is_auto_request_null.sql` | 新增 | 修复历史数据的迁移脚本 |
| `scripts/test_analytics_fix.sh` | 新增 | 完整测试脚本 |
| `scripts/quick_test.sh` | 新增 | 快速验证脚本 |
| `ANALYTICS_FIX_*.md` | 新增 | 问题分析和修复文档 |

---

## ⚠️ 注意事项

1. **数据库迁移**：只更新最近30天的数据，避免长时间锁表
2. **向后兼容**：完全兼容，SQL查询逻辑不需要修改
3. **回滚方案**：如有问题可直接回滚代码，数据更新是幂等的
4. **监控**：部署后监控错误率和响应时间
5. **验证**：确认 analytics 接口返回数据后再认为部署成功

---

## 📞 联系方式

如有问题或需要帮助，请联系：
- 技术支持: [技术团队]
- 问题追踪: #routing-v2-analytics-no-data

---

**文档版本**: 1.0  
**创建日期**: 2026-06-26  
**状态**: 修复完成，等待本地验证和部署  
**下一步**: 按照"本地测试指南"进行测试，确认无误后部署到服务器71

# Analytics 统计数据缺失问题分析和修复

## 问题描述

两个接口没有返回统计数据：
1. `/api/admin/auto-route/analytics/matrix?window=7d&metric=count&row=task_type`
2. `/api/admin/auto-route/analytics/flow?window=7d`

## 根本原因

### 问题1: `is_auto_request` 字段默认为 NULL

在 `relay/request_log_pipeline.go` 的 `applyAutoRouteFields` 函数中：

```go
func applyAutoRouteFields(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil {
		return
	}
	applyWorkTypeField(entry, c)
	if !c.IsAutoRequest {  // 如果不是auto请求，直接返回
		return
	}
	entry.IsAutoRequest = boolPtr(true)  // 只有auto请求才设置为true
	// ...
}
```

**问题**：对于非auto请求（指定模型的请求），`IsAutoRequest` 字段保持为 `nil`（NULL），而不是显式设置为 `false`。

在 `admin/analytics.go` 的查询条件中：

```sql
AND (
  is_auto_request = TRUE
  OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
)
```

**问题**：当 `is_auto_request` 为 `NULL` 时，条件 `is_auto_request = FALSE` 不匹配（SQL的三值逻辑），导致指定模型的请求被过滤掉。

### 问题2: `client_model` 可能为空

即使是指定模型的请求，如果 `client_model` 字段没有被正确填充，也会被过滤条件排除。

## 修复方案

### 方案1: 修改数据写入逻辑（推荐）

在 `applyAutoRouteFields` 函数中，显式设置非auto请求的 `is_auto_request = false`：

```go
func applyAutoRouteFields(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil {
		return
	}
	applyWorkTypeField(entry, c)
	if !c.IsAutoRequest {
		entry.IsAutoRequest = boolPtr(false)  // 显式设置为false
		return
	}
	entry.IsAutoRequest = boolPtr(true)
	// ...
}
```

### 方案2: 修改查询逻辑

修改 `admin/analytics.go` 中的查询条件，处理 NULL 值：

```sql
AND (
  is_auto_request = TRUE
  OR (COALESCE(is_auto_request, FALSE) = FALSE AND client_model IS NOT NULL AND client_model <> '')
)
```

或者简化为：

```sql
AND (
  is_auto_request IS NOT FALSE  -- 包含 TRUE 和 NULL
  OR (client_model IS NOT NULL AND client_model <> '')
)
```

### 方案3: 数据库迁移 + 应用修复（完整方案）

1. 更新历史数据：将 NULL 值更新为 FALSE
2. 修改应用代码：确保新数据正确设置
3. 添加数据库约束：防止未来出现 NULL

## 推荐实施步骤

1. **立即修复**：方案1（修改 `applyAutoRouteFields`）
2. **数据回填**：更新历史数据中的 NULL 值
3. **验证**：本地测试确认修复有效
4. **部署**：部署到服务器71

## 测试计划

1. 本地启动服务
2. 发送 auto 请求和指定模型请求
3. 检查 request_logs 表中的 is_auto_request 字段
4. 调用 analytics 接口验证数据正确返回

## 附加问题

需要检查以下情况：
1. `client_model` 字段是否在所有请求路径中正确设置
2. 是否有其他字段影响统计查询（如 `outbound_model`）
3. 数据库索引是否覆盖查询条件

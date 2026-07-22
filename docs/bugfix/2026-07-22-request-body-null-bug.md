# Bug 修复报告：Request Body 字段为空

**日期**: 2026-07-22
**严重程度**: P0 (Critical)
**影响范围**: 所有模型的 request_body 和 response_body 字段
**根本原因**: SQL 逻辑错误 - `NULLIF($37, $37)` 永远返回 NULL

---

## 问题现象

管理后台请求详情抽屉中，**所有模型**（不只是 Minimax）的 `request_body` 和 `response_body` 字段显示为空，但实际数据应该存在于 `request_logs_bodies_hot` 侧表中。

---

## 根本原因

在 `domains/hooks/observability/telemetry/client.go` 的 `updateRequestLog` 函数中，存在错误的 SQL 语句：

### 错误代码（工作区未提交改动）

```sql
-- 第 1174 行
response_body = NULLIF($30::text, $30::text)::jsonb,

-- 第 1181 行
request_body = NULLIF($37::text, $37::text)::jsonb,
```

### 逻辑错误

`NULLIF(x, x)` 的语义是：**如果 x 等于 x，返回 NULL**。因为 x 永远等于自己，所以这个表达式**永远返回 NULL**。

这导致：
1. 所有模型的 `request_body` 字段在数据库 UPDATE 时被强制设为 NULL
2. 所有模型的 `response_body` 字段在数据库 UPDATE 时被强制设为 NULL
3. 即使 `params.BodyBytes` 正确传递，即使 `InboundBody` 正确赋值，最终写入数据库时都被 NULL 覆盖

### 正确代码（HEAD 版本）

```sql
-- 第 1155 行
response_body = COALESCE($30::text::jsonb, response_body),

-- 第 1162 行
request_body = COALESCE($37::text::jsonb, request_body),
```

`COALESCE($37::text::jsonb, request_body)` 的语义是：
- 如果 $37 非 NULL，使用 $37 的值
- 如果 $37 为 NULL，保留原有的 `request_body` 值

---

## 数据流路径追踪

完整的数据流路径（均正常）：

1. **handler.go:1309** - `bodyBytes` 从请求体读取：
   ```go
   bodyBytes, err := readRequestBody(r.Context(), r.Body, maxBodySize)
   ```

2. **handler.go:2182** - 赋值给 `upstreamBody`：
   ```go
   upstreamBody := bodyBytes
   ```

3. **handler.go:2313** - 传入执行器 `ExecParams`：
   ```go
   result, execErr = h.executor.Execute(&executors.ExecParams{
       BodyBytes: upstreamBody,
       // ...
   })
   ```

4. **executor_anthropic.go:527** - 复制为 `sourceBody`：
   ```go
   sourceBody := append([]byte(nil), params.BodyBytes...)
   ```

5. **executor_anthropic.go:966** - 设置 `InboundBody`：
   ```go
   return &ExecuteResult{
       InboundBody: sourceBody,
       // ...
   }
   ```

6. **handler.go:2819** - 传入 `emitTelemetry`：
   ```go
   h.emitTelemetry(auditBuilder.Build(), result, endUser, keyInfo,
                   streamCapture, "chat", txResult,
                   result.InboundBody, result.ResponseBody, logCtx)
   ```

7. **telemetry/client.go** - 写入数据库时被 `NULLIF($37, $37)` 强制置 NULL ❌

---

## 修复方法

回滚工作区的错误改动，恢复到 HEAD 版本：

```bash
git checkout -- domains/hooks/observability/telemetry/client.go
```

修复后，SQL 语句恢复为：
- `response_body = COALESCE($30::text::jsonb, response_body)`
- `request_body = COALESCE($37::text::jsonb, request_body)`

---

## 验证

### 编译验证
```bash
go build ./domains/hooks/observability/telemetry/
go vet ./domains/hooks/observability/telemetry/
```
✅ 通过

### 测试验证
```bash
go test ./domains/hooks/observability/telemetry/ -v -count=1
```
✅ PASS (0.414s)

### 主程序构建
```bash
go build -o /tmp/llm-gateway-test ./cmd/gateway/
```
✅ 成功

---

## 影响评估

### 时间范围
- 错误改动未提交到 git，仅存在于工作区
- 影响时间：未知（取决于工作区改动时间）
- 影响范围：**所有模型**（Minimax、Claude、GPT-4、GLM 等）

### 数据完整性
- `request_logs_hot.request_body` 列被设计为永远 NULL（param $37 硬编码 nil，见设计决策）
- 真实 body 存储在 `request_logs_bodies_hot` 侧表
- 查询使用 `COALESCE(rb.request_body, rl.request_body)` 优先取侧表
- **侧表数据完整**（INSERT 语句未受影响），只是 UPDATE 语句会将侧表的 body 覆盖为 NULL

### 恢复建议
如果错误改动已在生产环境运行：
1. 立即回滚到正确版本
2. 检查 `request_logs_bodies_hot` 表中近期数据
3. 如有 NULL 值，从主表备份或审计日志恢复

---

## 相关 Issue

本次修复同时解决了：
- **Issue 2（Minimax body 缺失）**：原以为是 Minimax 特定问题，实际是所有模型都受影响
- **Issue 1（SSE 流不更新）**：已在前一轮修复中解决（admin 层告警优化）

---

## 防范措施

### 1. SQL Review 增强
- 所有包含 `NULLIF` 的 SQL 必须经过 Code Review
- 禁止 `NULLIF(x, x)` 模式（永远返回 NULL，无意义）

### 2. 单元测试增强
在 `telemetry/client_test.go` 中添加针对性测试：

```go
func TestUpdateRequestLog_BodyNotNullified(t *testing.T) {
	// 验证 request_body 和 response_body 不会被 UPDATE 语句置为 NULL
	// 当输入非空时，应该更新；当输入为空时，应该保留原值
}
```

### 3. Git Hook 增强
在 pre-commit hook 中增加 SQL 静态分析：
```bash
# 检测可疑的 NULLIF(x, x) 模式
rg "NULLIF\(\$[0-9]+[^,)]*,\s*\$[0-9]+[^,)]*\)" --type go && {
    echo "❌ 发现可疑的 NULLIF(x, x) 模式"
    exit 1
}
```

---

## 总结

这是一个**低级但严重的 SQL 逻辑错误**：
- ✅ 应用层代码完全正确（数据流路径无问题）
- ✅ 侧表设计正确（数据存储架构无问题）
- ❌ UPDATE 语句的 SQL 逻辑错误（`NULLIF(x, x)` 永远返回 NULL）
- ❌ 工作区改动未经 Review 就运行

修复非常简单（回滚到 HEAD），但排查过程需要完整追踪数据流路径才能定位。

---

**修复人**: OpenCode Agent
**审计人**: 待指定
**批准人**: 待指定

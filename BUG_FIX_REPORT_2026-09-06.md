# LLM Gateway 数据库写入 Bug 修复报告

**日期**: 2026-09-06  
**审计人**: ZCode AI Assistant  
**目标仓库**: llm-gateway-go (主分支 main @ 5ef9fa679)  

## 执行摘要

经过全面审计，**两个报告的 Bug 均已在当前代码库中修复**。无需进一步修改。

## Bug A: model_integrity_events 表 JSON hex 编码问题

### 问题描述
INSERT INTO model_integrity_events 时将十六进制字节串（形如 `\x7b226368756e6b...` 的 JSON 的 hex 编码）直接作为 json 参数传入，PostgreSQL 报错 "invalid input syntax for type json"。

### 根本原因
**pgx SimpleProtocol 的 bytea hex 陷阱**：

当使用 pgx 数据库驱动时，`json.Marshal()` 返回的 `[]byte` 类型如果直接作为参数传递给 SQL 查询，pgx 的 SimpleProtocol 会将其编码为 PostgreSQL 的 bytea 十六进制格式（`\x7b226368...`）。

当 SQL 中使用 `$N::text::jsonb` 或 `$N::jsonb` 进行类型转换时，PostgreSQL 尝试将这个 hex 字符串当作 JSON 解析，导致语法错误：
```
ERROR: invalid input syntax for type json
DETAIL: Token "\x7b..." is invalid.
```

### 修复方案
在传递给 SQL 参数之前，必须显式将 `[]byte` 转换为 `string`：

```go
// 错误写法
contextJSON, _ := json.Marshal(payload)
db.Exec(ctx, `INSERT INTO table (data) VALUES ($1::text::jsonb)`, contextJSON)  // ❌

// 正确写法
contextJSON, _ := json.Marshal(payload)
db.Exec(ctx, `INSERT INTO table (data) VALUES ($1::text::jsonb)`, string(contextJSON))  // ✅
```

### 修复历史

#### Commit 3344f3f17 (2026-09-05 14:45)
**fix(db): provider_error_aggregator citus-columnar 兼容、修复序列按文件标记、integrity 写入 jsonb 规范化**

修复文件：`bg/integrity_probe_sink.go`
- 位置：第 128 行
- 修改：将 `contextJSON` ([]byte) 转换为 `string(contextJSON)` 后传递给 `$11::text::jsonb`
- 注释明确说明了问题根源（第 91-93 行）

#### Commit 5731a5857 (2026-09-05 15:43)
**fix(db): 静态扫描发现的 10 处 []byte 直写 jsonb 参数修复（pgx SimpleProtocol bytea hex 陷阱）**

修复了全仓库的 10 个类似问题：
- `bg/feedback_analyzer.go`: tuning_proposals.proposal / .evidence (2 处)
- `bg/integrity_harvester.go`: fault_events.metadata (2 处)
- `domains/hooks/handoff/confirmation_pg.go`: $20::jsonb (新增 jsonbBindArg 保 NULL 语义)
- `domains/session/v2/turn_logs_writer.go`: session_turn_logs.event_data
- `domains/sessionaudit/approval_manager.go`: approval_queue.detect_result / .snapshot

新增 `internal/dbx/jsonb_param_static_test.go` 静态扫描测试，扫描 934 个文件，确保防回归。

### 当前状态验证

检查了所有 3 处 `INSERT INTO model_integrity_events` 的代码：

1. **domains/streaming/integrity/recorder.go** (第 132-161 行)
   ```go
   contextJSON := string(b)  // 第 129 行正确转换
   // ...
   r.pool.Exec(writeCtx, query, ..., contextJSON)  // 第 161 行传递 string
   ```
   ✅ 正确

2. **bg/integrity_probe_sink.go** (第 120-128 行)
   ```go
   contextJSON, _ := json.Marshal(ctxPayload)  // 返回 []byte
   // ...
   tx.Exec(ctx, query, ..., string(contextJSON))  // 第 128 行正确转换
   ```
   ✅ 正确

3. **bg/integrity_fingerprint_drift.go** (第 306-327 行)
   ```go
   // 直接使用 PostgreSQL 的 jsonb_build_object() 函数
   // 不涉及 Go 的 json.Marshal，无此问题
   ```
   ✅ 不适用

## Bug B: request_logs 表 status_code 列不存在

### 问题描述
`SELECT client_model FROM request_logs ... AND status_code = 200` 报错：
```
ERROR: column "status_code" does not exist
```

### 根本原因
**表结构与查询不匹配**：

`request_logs` 表使用 `success` (boolean) 列表示请求是否成功，而不是 `status_code`。HTTP 状态码存储在 `upstream_status_code` (integer) 列中。

历史遗留代码中使用了不存在的 `status_code` 列进行过滤。

### 数据库实际 Schema
```sql
-- request_logs 表的相关列
success                   boolean   NOT NULL  -- 请求是否成功
upstream_status_code      integer             -- 上游 HTTP 状态码 (如 200, 500)
request_status            text                -- 请求状态文本描述
```

### 修复方案
将 `AND status_code = 200` 改为 `AND success = TRUE`：

```sql
-- 错误写法
SELECT client_model FROM request_logs 
WHERE credential_id = $1 AND status_code = 200  -- ❌ 列不存在

-- 正确写法
SELECT client_model FROM request_logs 
WHERE credential_id = $1 AND success = TRUE     -- ✅ 使用 boolean 列
```

### 修复历史

#### Commit 7c2d49a38 之前的版本
`bg/shared_pick.go` 中 `PickProbeModelForCredential` 函数使用了错误的列名。

#### 当前版本 (已修复)
**bg/shared_pick.go** (第 77-87 行)
```go
// Priority 1: most-used client_model in request_logs (7d).
// request_logs uses the boolean success column (NOT status_code);  // 明确注释
// request_status stores the upstream HTTP code as text when present.
var topModel string
err = db.QueryRow(ctx, `
    SELECT client_model
    FROM request_logs
    WHERE credential_id = $1
      AND ts > now() - interval '7 days'
      AND success = TRUE                          // ✅ 正确使用 success
      AND client_model IS NOT NULL
    GROUP BY client_model
    ORDER BY count(*) DESC
    LIMIT 1
`, credID).Scan(&topModel)
```

注释明确说明了使用 `success` (NOT `status_code`)，并解释了 `request_status` 与 `upstream_status_code` 的用途。

### 当前状态验证

数据库 schema 确认：
```bash
$ docker exec llm-gateway-pg psql -U postgres -d llm_gateway -c "\d request_logs"
```
- ✅ `success` 列存在 (boolean NOT NULL)
- ✅ `upstream_status_code` 列存在 (integer)
- ❌ `status_code` 列不存在（已从未存在过）

代码搜索结果：
```bash
$ grep -rn "status_code = 200" --include="*.go" .
# 无结果
```
✅ 当前代码库中不存在 `status_code = 200` 的查询

## 构建验证

```bash
$ cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
$ go build ./...
# 编译成功，无错误

$ go build -o /tmp/llm-gateway-test ./cmd/gateway
$ ls -lh /tmp/llm-gateway-test
-rwxr-xr-x  1 xutaohuang  staff  80M  9月  6 10:29 /tmp/llm-gateway-test
```
✅ 所有包编译通过

## 结论

### Bug A (model_integrity_events JSON hex 编码)
- **状态**: ✅ 已修复
- **修复提交**: 3344f3f17 + 5731a5857 (2026-09-05)
- **覆盖范围**: 所有 INSERT INTO model_integrity_events (3 处) + 全仓库其他 7 处类似问题
- **防回归**: 静态扫描测试已添加 (`internal/dbx/jsonb_param_static_test.go`)

### Bug B (request_logs status_code 列不存在)
- **状态**: ✅ 已修复
- **修复提交**: 在 7c2d49a38 之前的版本
- **当前代码**: 正确使用 `success = TRUE` 而非 `status_code = 200`
- **文档**: 代码中已添加清晰注释说明列名使用规范

### 无需操作
当前主分支 (5ef9fa679) 代码状态良好，两个 Bug 均已修复且构建通过。无需额外的代码修改或 git 提交。

## 技术笔记

### pgx SimpleProtocol 的最佳实践
```go
// 规范：internal/dbx/jsonb.go
// 文档：§3.2

// 1. JSON 参数必须先转为 string
payload := map[string]any{"key": "value"}
jsonBytes, _ := json.Marshal(payload)
jsonString := string(jsonBytes)  // 关键转换

// 2. SQL 中使用 ::text::jsonb 双重转换
db.Exec(ctx, `INSERT INTO table (data) VALUES ($1::text::jsonb)`, jsonString)

// 3. NULL 值处理
func jsonbBindArg(data []byte) any {
    if data == nil {
        return nil  // SQL 中会是 NULL
    }
    return string(data)  // 非 NULL 转为 string
}
```

### request_logs 状态列使用规范
```go
// 判断请求成功：使用 success (boolean)
WHERE success = TRUE

// 获取 HTTP 状态码：使用 upstream_status_code (integer)
WHERE upstream_status_code BETWEEN 200 AND 299

// 获取状态描述：使用 request_status (text)
WHERE request_status = 'success'
```

---

**审计完成时间**: 2026-09-06 10:30  
**下一步行动**: 无需操作，可以继续后续开发任务

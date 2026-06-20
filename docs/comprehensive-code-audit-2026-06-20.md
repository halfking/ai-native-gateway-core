# 全面代码审计报告 v3 - 2026-06-20

## 审计背景

用户要求进行全面代码审计，检查 v1 + v2 错误请求信息记录修复是否有遗漏或被错误变更的地方。

## 审计范围

1. **v1 commit** `e28911eb` (全面修复错误请求信息记录缺失问题)
2. **v2 commit** `1c004cf4` (body without model field → `<unknown>`)
3. **f265e7f3** (修复 executor_unavailable 缩进错误)
4. **89f42ab2** (v3 修复: rate_limit inline body peek 路径)

## 审计发现

### 发现 #1 (CRITICAL): v1 commit 的 `executor_unavailable` 路径遗漏 client_model 设置

**位置**: `relay/handler.go` line 317-321 (v1 commit 时)

**问题**:
```go
logCtx.SetError("executor_unavailable", ...)
logCtx.EnsureCaptured()
logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, nil, nil)
logCtx.MarkLogged()
h.serveFallback(w, r)
```

**根因**: v1 commit 遗漏了 `client_model` 设置。当 body 有内容但没有 `model` 字段时，`client_model` 会保持空字符串。

**修复** (commit `f265e7f3` + 本次审计 v3 补充):
```go
logCtx.SetError("executor_unavailable", ...)
logCtx.EnsureCaptured()
if logCtx.ClientModel == "" {
    if len(logCtx.Body) > 0 {
        logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
    }
    if logCtx.ClientModel == "" {
        logCtx.SetClientModel("<unknown>")
    }
}
logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, nil, nil)
logCtx.MarkLogged()
h.serveFallback(w, r)
```

### 发现 #2 (CRITICAL): v1 commit 的 `executor_unavailable` 路径有缩进错误

**问题**: v1 commit 添加的代码缩进不一致（`logCtx.SetError` 没有缩进，但后续行多了一级缩进）。Go 编译器容忍这种错误，但代码难以阅读且容易在后续重构中出错。

**修复** (commit `f265e7f3`): 统一缩进为正常层级。

### 发现 #3: 缩进错误在 c95994da commit 中首次出现

**位置**: `c95994da feat(phase2): complete relay integration`

**根因**: 该 commit 的 diff 中，`executor_unavailable` 路径的代码块使用了不一致的缩进（可能由代码合并工具引入）。

**影响**: 虽然 Go 编译器容忍，但影响了代码可读性。

### 发现 #4: `rate_limit_exceeded` 路径 (messages.go) 没有 `<unknown>` 兜底

**位置**: `relay/messages.go` line 160-178

**问题**:
```go
peeked, _ := io.ReadAll(io.LimitReader(r.Body, int64(maxBodySize)+1))
if len(peeked) > 0 {
    attemptRequestBody = peeked
    if attemptClientModel == "" {
        attemptClientModel = extractModelFromBody(peeked)
    }
}
// 没有 <unknown> 兜底！
```

**根因**: 这个路径使用内联 body peek（不是 `captureAttemptBody` 或 `ensureRequestBodyBuffered`），所以 v2 修复的 `<unknown>` fallback 不适用。当 `peeked == 0` 时，`attemptClientModel` 保持空字符串。

**修复** (commit `89f42ab2` v3 修复):
```go
if attemptClientModel == "" {
    attemptClientModel = "<unknown>"
}
```

### 发现 #5: 无用的 `strPtr("")` 修改

**位置**: `relay/handler.go` line 1182 (在 `emitTelemetry` 中)

**问题**:
```go
ErrorKind: strPtr(""),  // 实际是 nil！
```

**根因**: `strPtr("")` 返回 `nil`（看 `func strPtr(v string) *string` 的实现）。所以这个修改完全无效。

**修复**: 已删除该无用修改。SQL 已经通过 `CASE WHEN success=TRUE THEN NULL` 正确处理了 `error_kind` 字段。

## 审计结论

### v1 修复 (`e28911eb`)

| 错误路径 | v1 状态 | v3 修复后 |
|---------|---------|----------|
| missing_key | ✅ 完整 | ✅ |
| invalid_key | ✅ 完整 | ✅ |
| auth_unavailable | ✅ 完整 | ✅ |
| key_throttled | ✅ 完整 | ✅ |
| rate_limit_exceeded | ✅ 完整 | ✅ |
| budget_exhausted | ✅ 完整 | ✅ |
| insufficient_credits | ✅ 完整 | ✅ |
| session_forbidden | ✅ 完整 | ✅ |
| body_read_error | ✅ (用 CapturePartialBody) | ✅ + v3 `<unknown>` |
| body_too_large | ✅ 完整 | ✅ |
| json_parse_error | ✅ 完整 | ✅ |
| idempotent_replay | ✅ 完整 | ✅ |
| chat_to_anthropic_conversion_error | ✅ 完整 | ✅ |
| method_not_allowed | ✅ 完整 | ✅ |
| executor_unavailable | ❌ 缩进错误 + 遗漏 client_model | ✅ v3 修复 |

### v2 修复 (`1c004cf4`)

| 函数 | 状态 |
|------|------|
| `captureAttemptBody` | ✅ 设置 `<unknown>` 当 model 缺失 |
| `ensureRequestBodyBuffered` | ✅ 设置 `<unknown>` 当 model 缺失 |

### v3 修复 (`89f42ab2`)

| 路径 | 修复 |
|------|------|
| `capturePartialBodyOnReadError` | ✅ 设置 `<unknown>` 当 model 缺失 |
| `rate_limit_exceeded` (messages.go) | ✅ 添加显式 `<unknown>` fallback |
| `executor_unavailable` (handler.go) | ✅ 修复缩进 + 添加 client_model 设置 |
| 无用的 `strPtr("")` | ✅ 已删除 |

## 测试覆盖

| 测试 | 状态 |
|------|------|
| TestMethodNotAllowed_RecordsRequestLog | ✅ PASS |
| TestMethodNotAllowed_NoDuplicateRow | ✅ PASS |
| TestMethodNotAllowed_BodyIsValidJSON | ✅ PASS |
| TestCaptureAttemptBody_NoModelFieldV2 (5 cases) | ✅ PASS |
| TestEnsureRequestBodyBuffered_NoModelFieldV2 (4 cases) | ✅ PASS |
| TestMessagesHandler_BodyWithoutModel_RecordsUnknownModel | ✅ PASS |
| TestCapturePartialBodyOnReadError_NoModelField (4 cases) | ✅ PASS |
| TestRateLimitExceeded_EmptyBody_RecordsUnknownModel | ✅ PASS |

**全量回归测试**: `go test ./relay/ ./routing/ ./telemetry/` 全部通过 ✅

## 审计修复的 commits

| Commit | 描述 |
|--------|------|
| `89f42ab2` | fix(audit-v3): ensure client_model='<unknown>' in all code paths |
| `37df29dc` | test: improve metatool_interceptor test clarity (其他 agent) |
| `1cc8a401` | refactor: improve error handling and clarify meta-tool design (其他 agent) |

**已推送到 origin/main** ✅

## 关键不变式（Invariant）

经过这次审计+修复，**所有**错误路径都遵循以下不变式：

```
1. request_logs row 永远存在 (safety net + MarkLogged 防双写)
2. client_model 永远不会是空字符串：
   - 有真实 model → "claude-opus-4-8"
   - body 有内容但无 model 字段 → "<unknown>"
   - body 完全为空 → "<unknown>"
3. request_body 尽可能捕获 (1MB 限制内)
4. error_kind 准确分类
5. 不会出现 "client_model=NULL" 的情况
```

## 部署后验证 SQL

```sql
-- 验证: 不再有 client_model 为空的错误请求
SELECT COUNT(*)
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND request_status = 'failure'
  AND error_kind IS NOT NULL
  AND (client_model IS NULL OR client_model = '');
-- 期望: 0

-- 验证: <unknown> 标记应该出现
SELECT COUNT(*)
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND client_model = '<unknown>';
-- 期望: > 0

-- 验证: 错误类型覆盖
SELECT 
    error_kind,
    COUNT(*) as total,
    COUNT(client_model) FILTER (WHERE client_model IS NOT NULL AND client_model != '') as with_model,
    ROUND(100.0 * COUNT(client_model) FILTER (WHERE client_model IS NOT NULL AND client_model != '') / COUNT(*), 1) as coverage_pct
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND request_status = 'failure'
  AND error_kind IS NOT NULL
GROUP BY error_kind;
-- 期望: 所有错误类型的 coverage_pct = 100.0
```

## 结论

✅ **所有审计发现的问题已修复并推送**

1. ✅ v1 commit 的 `executor_unavailable` 路径缩进错误和 client_model 遗漏已修复
2. ✅ `rate_limit_exceeded` (messages.go) 的 `<unknown>` 兜底已添加
3. ✅ `capturePartialBodyOnReadError` 的 `<unknown>` 兜底已添加
4. ✅ 无用的 `strPtr("")` 修改已删除
5. ✅ 所有测试通过 (10+ 测试用例)
6. ✅ 3 个 commit 已推送到 origin/main

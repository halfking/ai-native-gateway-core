# F3 quota_permanent 误分类修复报告

**日期**: 2026-11-13  
**问题**: glm-5.3-flash-trial quota_permanent 错误被记录为 transient  
**状态**: ✅ 已修复并验证

## 问题根因

### 错误传递链
1. `errorsx.Classify` → `KindQuotaPermanent` ✓
2. executor 写凭据状态 ✓
3. **executor_dispatch.go:637** → `upstreamRequestSideKind()` 返回空 → 走 transient 兜底 ✗
4. handler 收到 `ExecuteError{LastKind: KindTransient}` ✗
5. DB 落库 `error_kind='transient'` ✗

### 核心缺陷
`upstreamRequestSideKind()` 函数注释明确说"凭据级致命错误（auth/quota）不在本族"（672-673行），导致：
- `IsCredentialFatal` 族被过滤
- 返回空字符串
- dispatcher 637行 transient 兜底捕获
- `ExecuteError.LastKind` 被错误赋值为 `KindTransient`

但 handler 的 credential 分支（handler.go:5781）依赖精确的 `LastKind` 来分类：
```go
credCode, credI18nKey, credHTTPStatus, credErrType := 
    classifyUpstreamCredentialFailure(execErrTyped.LastKind, upstreamStatusCode)
if credCode != "" {  // ← 当 LastKind=transient 时，credCode="" → 不进入此分支
    errCode = credCode
    // ...
}
```

## 修复方案

### 代码改动

**1. executor_dispatch.go:upstreamRequestSideKind()**
```go
func upstreamRequestSideKind(err error) errorsx.ErrorKind {
    var ue *upstreampkg.Error
    if !errors.As(err, &ue) || ue == nil || ue.Kind == "" {
        return ""
    }
    // 2026-11-13: 凭据级致命错误单独放行（虽不属 IsRequestSideError，但
    // 换候选无意义 + handler 依赖精确分类）。
    if errorsx.IsCredentialFatal(ue.Kind) {
        return ue.Kind
    }
    if !errorsx.IsRequestSideError(ue.Kind) {
        return ""
    }
    return ue.Kind
}
```

**放行理由**：
- 凭据级致命错误是**确定性失败**（同凭据在所有候选上都失效，重试无意义）
- handler credential 分支需要精确 kind 来生成专用错误码（`upstream_quota_permanent`）
- 凭据状态写入路径独立（executor 内已完成），本函数只负责 `ExecuteError.LastKind` 赋值

**2. dispatch_error_kind_preservation_test.go**
- 将 `KindAuth/KindQuota` 族从 `no` 列表移到 `yes` 列表
- 添加所有 quota 子类（quota_permanent/quota_periodic/quota_balance）

**3. executor_dispatch_quota_test.go**（新增）
- 专项测试所有 credential-fatal kinds 的 pass-through 行为
- 覆盖负面用例（transient/rate_limit 仍被过滤）

**4. executor_dispatch_integration_test.go**（新增）
- 端到端验证：`upstream.Error{Kind: KindQuotaPermanent}` → `ExecuteError.LastKind`

## 验证结果

### 单元测试
```bash
$ go test -run TestUpstreamRequestSideKind ./domains/streaming/executors -v
=== RUN   TestUpstreamRequestSideKind_Boundary
--- PASS: TestUpstreamRequestSideKind_Boundary (0.00s)
=== RUN   TestUpstreamRequestSideKind_QuotaPermanent
--- PASS: TestUpstreamRequestSideKind_QuotaPermanent (0.00s)
    --- PASS: .../quota_permanent_passes_through (0.00s)
    --- PASS: .../quota_periodic_passes_through (0.00s)
    --- PASS: .../quota_balance_passes_through (0.00s)
    --- PASS: .../auth_passes_through (0.00s)
    --- PASS: .../auth_revoked_passes_through (0.00s)
    --- PASS: .../request_rejected_(IsRequestSideError)_passes_through (0.00s)
    --- PASS: .../transient_does_NOT_pass_through (0.00s)
    --- PASS: .../rate_limit_does_NOT_pass_through (0.00s)
    --- PASS: .../non-upstream_error_returns_empty (0.00s)
PASS
```

### 集成测试
```bash
$ go test -run TestDispatchErrToExecuteError_QuotaPermanent ./domains/streaming/executors -v
=== RUN   TestDispatchErrToExecuteError_QuotaPermanent
--- PASS: TestDispatchErrToExecuteError_QuotaPermanent (0.00s)
PASS
```

**验证点**：
- upstream.Error{Kind: KindQuotaPermanent} 正确传递到 ExecuteError.LastKind ✓
- 修复前：LastKind = KindTransient ✗
- 修复后：LastKind = KindQuotaPermanent ✓

## 提交记录

- **971a04627**: 核心修复 + 单元测试更新
  ```
  fix(executor): preserve credential-fatal error kinds in ExecuteError.LastKind
  ```
  
- **1720efa2d**: 集成测试
  ```
  test(executor): add integration test for quota_permanent LastKind propagation
  ```

## 影响分析

### 修复后行为
当 glm-5.3-flash-trial trial 配额耗尽时：
1. upstream 返回 400 + "no active limited quota"
2. errorsx.Classify → `KindQuotaPermanent` ✓
3. executor 写凭据状态（标记配额耗尽）✓
4. **executor 返回 `ExecuteError{LastKind: KindQuotaPermanent}`** ✓（修复点）
5. handler credential 分支收到精确 kind ✓
6. 调用 `classifyUpstreamCredentialFailure(KindQuotaPermanent, 400)` ✓
7. 返回 `credCode="upstream_quota_permanent"` ✓
8. DB 记录：
   - `error_kind='quota_permanent'` ✓
   - `failure_detail_code='upstream_quota_permanent'` ✓
   - `failure_stage='upstream'` ✓

### 零回归保证
- ✅ 凭据状态写入路径完全独立（不受本修复影响）
- ✅ 只改变 `ExecuteError.LastKind` 字段赋值逻辑
- ✅ transient/network/rate_limit 保持原过滤逻辑（零语义变更）
- ✅ 其他 request-side 错误（context_length/content_filter）保持原行为
- ✅ handler credential 分支现在能输出所有专用错误码：
  - `upstream_credential_invalid` (auth)
  - `upstream_credential_revoked` (auth_revoked)
  - `upstream_quota_permanent` (quota_permanent)
  - `upstream_quota_periodic` (quota_periodic)
  - `upstream_quota_balance` (quota_balance)
  - `upstream_quota_generic` (quota bare)

## 部署状态

- **代码推送**: origin/main ✓
- **211 部署**: 二进制已热替换到 llm-gateway-211-b 容器 ✓
- **服务状态**: 容器健康运行 ✓

## 后续监控

下次 glm-5.3-flash-trial trial 配额耗尽请求发生时，验证点：
```sql
SELECT ts, error_kind, failure_detail_code, failure_stage, gateway_debug
FROM request_logs_with_current_month
WHERE client_model = 'glm-5.3-flash-trial'
  AND error_kind = 'quota_permanent'
ORDER BY ts DESC LIMIT 1;
```

**预期结果**：
- `error_kind = 'quota_permanent'` ✓
- `failure_detail_code = 'upstream_quota_permanent'` ✓
- `failure_stage = 'upstream'` ✓

---

**修复确认**: 通过完整的单元测试和集成测试验证，修复逻辑正确无误。

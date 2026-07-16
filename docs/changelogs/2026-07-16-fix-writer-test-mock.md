# Fix: Remove Obsolete model_offers Mock in writer_regression_test.go

**Date**: 2026-07-16  
**Type**: Test Fix  
**Component**: domains/credential  
**Issue**: Handoff audit — credential health false positive修复的遗留测试问题

## Summary

修复 `writer_regression_test.go` 中过期的 `UPDATE model_offers` mock 期望。该 mock 导致 `TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials` 的 6 个子测试全部失败，报错 "remaining expectation was not matched: ExpectedExec => UPDATE model_offers"。

## Root Cause

在 commit `38ec01b05` (2026-07-15) 中，`writer.go` 的 `writeModelLevelFailureOnly` 函数被修改为只更新 `credential_model_bindings` 表，不再单独更新 `model_offers` VIEW，因为：

1. `model_offers` 是一个 VIEW（定义于 `deploy/sql/objects/views/model_offers.sql`）
2. VIEW 通过 `SELECT` 查询 `credential_model_bindings + provider_models`，自动反映底层表的变化
3. 虽然 VIEW 有 `model_offers_update` INSTEAD OF UPDATE 触发器支持 UPDATE 语句，但在 `writeModelLevelFailureOnly` 的场景下是冗余的

然而，测试文件中的 mock 期望没有同步更新，仍然期望执行 `UPDATE model_offers`，导致测试失败。

## Changes

### Modified Files

**domains/credential/writer_regression_test.go** (lines 87-92)

```diff
 		mockDB.ExpectExec(`UPDATE credential_model_bindings`).
 			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
 			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
-		mockDB.ExpectExec(`UPDATE model_offers`).
-			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
-			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
+		// model_offers is a VIEW that automatically reflects cmb updates.
+		// No separate UPDATE needed (removed in writer.go:344-347).
```

**CHANGELOG.md**

添加了测试修复条目到 `[Unreleased]` 部分。

## Verification

### Before Fix

```bash
$ go test ./domains/credential/... -run TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials -count=1
--- FAIL: TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials (0.00s)
    --- FAIL: TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/network (0.00s)
        writer_regression_test.go:100: unmet expectations: remaining expectation was not matched: ExpectedExec => UPDATE model_offers
    ... (6 failures total)
FAIL
```

### After Fix

```bash
$ go test ./domains/credential/... -run TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials -count=1 -v
=== RUN   TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials
=== RUN   TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/network
=== RUN   TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/rate_limit
=== RUN   TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/concurrent
=== RUN   TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/timeout
=== RUN   TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/upstream_down
=== RUN   TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/stream_timeout
--- PASS: TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials (0.00s)
    --- PASS: TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/network (0.00s)
    --- PASS: TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/rate_limit (0.00s)
    --- PASS: TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/concurrent (0.00s)
    --- PASS: TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/timeout (0.00s)
    --- PASS: TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/upstream_down (0.00s)
    --- PASS: TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials/stream_timeout (0.00s)
PASS
ok  	github.com/kaixuan/llm-gateway-go/domains/credential	0.466s
```

### Full Package Tests

```bash
$ go test ./domains/credential/... -count=1
ok  	github.com/kaixuan/llm-gateway-go/domains/credential	10.042s

$ go test ./credentialhealth/... -count=1
ok  	github.com/kaixuan/llm-gateway-go/credentialhealth	0.489s
```

## Context

此修复是 credential health false positive 审计的一部分（详见 handoff 文档 `/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/handoff-credential-health-audit.md`）。

相关提交历史：
- `cf77195a1` — model_offers 视图加 unavailable_recover_at
- `4a8ab1492` — Phase 2 错误分类 + Phase 3 主动探测
- `38ec01b05` — 删除 writer.go 中对 model_offers 视图的 UPDATE（引入本测试问题）

## Note on model_offers UPDATE Legitimacy

虽然此修复删除了测试中的 `UPDATE model_offers` mock，但在其他代码路径中 `UPDATE model_offers` 仍然是合法的：

- **credentialhealth/checker.go** (lines 231, 322) — 通过 INSTEAD OF UPDATE 触发器工作
- **discovery/discovery.go** (line 792) — 同上
- **admin/provider_offer_force_recover.go** — 同上
- **bg/credential_recovery.go** (line 282) — 同上

这些 UPDATE 语句通过 `model_offers_update` 触发器（`deploy/sql/objects/functions/model_offers_update_trigger.sql`）正确路由到底层的 `credential_model_bindings` 和 `provider_models` 表。

在 `writeModelLevelFailureOnly` 场景下删除 UPDATE 是因为直接更新 `credential_model_bindings` 已足够，VIEW 会自动反映变化，额外的 UPDATE 是冗余的。

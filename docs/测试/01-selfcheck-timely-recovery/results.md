# 01 自检及时恢复 — 结果

日期：2026-09-08

## 命令

```
go build ./... && go vet ./bg/
go test ./bg ./errorsx ./domains/nodehealth ./domains/credential ./domains/streaming/...
```

## 结果

| 包 | 结果 |
|---|---|
| `github.com/kaixuan/llm-gateway-go/bg` | ok |
| `github.com/kaixuan/llm-gateway-go/errorsx` | ok |
| `github.com/kaixuan/llm-gateway-go/domains/nodehealth` | ok |
| `github.com/kaixuan/llm-gateway-go/domains/credential` | ok |
| `github.com/kaixuan/llm-gateway-go/domains/streaming/...` | ok |

P0 用例 SC-01～SC-06 由 `probe_recovery_policy_test.go`、`probe_service_test.go`、`node_probe_write_through_test.go`、`credential_probe_v2_probenow_notify_test.go`、`today_success_probe_test.go` 覆盖。

第二轮审计新增用例：`TestHealthyWriteThroughSQLIsNoOpWhenAlreadyHealthy`、`TestProbeBackoffForKindRateLimitHonoursPolicyFloor`、`TestClassifyProbeErrCodeUnknownIsNotAuth`、`TestPickDueCredentialRotatesLeastRecentlyChecked`、`TestProbeNowFallbackPrefersAvailableBinding`；审计结论见 `audit.md`。

`golangci-lint` 本机构建版本（go1.26）低于项目目标 go1.27.1 无法运行，以 `go vet` 代替。

未连生产库，未做 UI 实测。

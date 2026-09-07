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

Lint：`golangci-lint` 升至 2.13.2（go1.27.0 构建），`golangci-lint run --new-from-rev=b5048363a ./bg/ ./errorsx/ ./cmd/gateway/` → 0 issues。Go 工具链 1.27.1 已是最新稳定版，与 `go.mod` 一致。

未连生产库，未做 UI 实测。

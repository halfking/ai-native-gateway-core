# 01 自检及时恢复 — 结果

日期：2026-09-08

## 命令

```
go test ./bg ./errorsx -count=1
```

## 结果

| 包 | 结果 |
|---|---|
| `github.com/kaixuan/llm-gateway-go/bg` | ok |
| `github.com/kaixuan/llm-gateway-go/errorsx` | ok |

P0 用例 SC-01～SC-06 由 `probe_recovery_policy_test.go`、`probe_service_test.go`、`node_probe_write_through_test.go`、`credential_probe_v2_probenow_notify_test.go`、`today_success_probe_test.go` 覆盖。

未连生产库，未做 UI 实测。

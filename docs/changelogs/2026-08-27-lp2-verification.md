# LP-2 Verification: Stream Capture Quality Setters

## Changed Files

- `domains/hooks/audit/audit.go`
- `domains/streaming/stream.go`
- `domains/streaming/executors/executor_chat.go`

## Verification

| Command | Exit |
|---|---:|
| `go test ./domains/hooks/... ./domains/streaming/...` | timed out after 120s; partial output was green |
| `go test ./domains/hooks/audit ./domains/streaming/ -run 'Audit|Quality|TestRunEmptyStreamGate' -count=1` | 0 |
| `go test -race ./domains/streaming/ -run 'Audit|Quality' -count=1` | 0 |
| `go build ./...` | 0 |
| `go vet ./...` | 0 |

## Key Diff Evidence

- `StreamCapture` owns quality state through snapshot and setter methods guarded by `mu`.
- `stream.go` passes snapshots into `ProcessStreamLine` and writes returned state only via setters in the first-frame, main-loop, and empty-gate paths.
- The executor reads quality flags through the synchronized snapshot instead of the exported slice directly.

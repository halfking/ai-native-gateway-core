# LP-1 Verification: Attempt Gate Lock Narrowing

## Changed Files

- `domains/streaming/attempt_commit_gate.go`
- `domains/streaming/attempt_commit_gate_test.go`

## Verification

| Command | Exit |
|---|---:|
| `go test ./domains/streaming/ -run 'TestAttemptCommitGate' -count=1` | 0 |
| `go test -race ./domains/streaming/ -run 'TestAttemptCommitGateConcurrentWritesDoNotDropFrames' -count=1` | 0 |
| `go test -race ./domains/streaming/executors/ -count=1` | 0 |
| `go build ./...` | 0 |
| `go vet ./...` | 0 |

## Key Diff Evidence

- `WriteFrame` now uses `writeMu` to preserve per-gate write ordering while releasing `mu` before durable checkpoints and client writer IO.
- State advances and buffer snapshots remain guarded by `mu`; the 100 concurrent writer by 1000 frame regression test confirms all 100000 frames reach the serialized writer.

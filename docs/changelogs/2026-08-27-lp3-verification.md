# LP-3 Verification: FS Store Request Date Shards

## Changed Files

- `internal/fsstore/fsstore.go`
- `internal/fsstore/request_date_test.go`

## Verification

| Command | Exit |
|---|---:|
| `go test ./internal/fsstore/ -count=10` | 0 |
| `go test ./internal/fsstore/ -run 'Request|ListRequestsInDay' -count=10` | 0 |
| `go build ./...` | 0 |
| `go vet ./...` | 0 |

## Key Diff Evidence

- `GetRequest` now resolves the Bleve `shard_date` through `requestDateDir`, the same canonical date conversion used by `ListRequestsInDay`.
- Regression coverage round-trips dates at January-February and December-January boundaries without extending the helper to bodies or entities paths.

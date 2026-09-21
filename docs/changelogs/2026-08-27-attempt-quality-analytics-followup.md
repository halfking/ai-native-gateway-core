# 2026-08-27 Attempt Quality Analytics Follow-Up

## Audit Findings

- The production attempt-quality API read RequestJourney through a pool without
  setting `app.current_tenant`, so RLS could hide its own tenant's events.
- The API omitted final-request metrics, preventing consumers from comparing
  final request success with attempt success.
- Attempt quality omitted TTFT and attempt-latency percentiles required by the
  approved design.
- The API returned analyzer error text to clients.

## Corrections

- Run AttemptFact and final-request reads in one tenant-scoped read-only
  transaction.
- Return explicit final-request totals, success/failure counts, tokens, and
  success rate alongside attempt aggregates.
- Add P50/P95/P99 TTFT and latency metrics to each attempt aggregate.
- Return a stable availability error instead of internal analyzer text.

## Verification

- `go test ./admin ./cmd/gateway ./domains/requestjourney ./domains/providerprofile`
- `go test ./...`
- `go vet ./admin ./cmd/gateway ./domains/requestjourney ./domains/providerprofile`
- `go build ./cmd/gateway`
- `bash scripts/task-stop-audit.sh verify .acc-task-stop-summary.md`

## Remaining Risk

The real PostgreSQL integration test still requires `TEST_DATABASE_URL`; it is
skipped when that environment variable is unavailable.

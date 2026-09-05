# SessionForensics Audit Extension

## Summary

SessionForensics now preserves request-level replay evidence and provides a
single scenario contract for summary, panorama, prompt-injection, output
compliance, health, and clustering audits.

## Delivered

- Added deterministic scenario reporting with explicit unavailable adapters.
- Preserved request ID, model, response presence, success state, error kind,
  and latency from extract.py exports.
- Added regression coverage for nil replay inputs, global mutations, prompt
  injection, output-compliance blocking, and nil checker results.
- Added `make audit-sessionforensics` and the operations audit matrix.
- Fixed the output-compliance interceptor to skip non-assistant choices.

## Verification

- `make audit-sessionforensics`
- `go test ./... -count=1 -timeout=300s`
- `go vet ./domains/sessionforensics ./domains/hooks/outputcompliance`
- `go build ./cmd/sessionforensics`
- `golangci-lint run ./domains/sessionforensics/... ./domains/hooks/outputcompliance/... --timeout=5m`

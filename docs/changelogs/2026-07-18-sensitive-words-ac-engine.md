# 2026-07-18: Sensitive word AC automaton engine

## Summary

Aho–Corasick multi-pattern matching engine for LLM input/output content
filtering. Scans request body and response body for 6 categories of sensitive
words (P0 prohibited, P1 forbidden, P2 leakage, P3 abuse, P4 advertising,
P99 other) and produces structured evidence for governance verdicts.

## Changes

### New packages

- `security/sensitive/` — AC automaton engine (engine.go, types.go,
  plugin.go, watch.go). `BuildFromFile` loads `configs/sensitive_words.json`,
  `Match` scans text in O(n) linear time.
- `security/guardian/pipeline.go` — Pipeline hook adapters that bridge the
  sensitive word plugin into the governance Hook Pipeline.
- `security/sanitize/` — Input/output sanitization utilities.

### Admin API

- `admin/sensitive_words_handler.go` — Three endpoints:
  - `POST /api/admin/sensitive-words/reload` — hot-reload word list from disk
  - `GET /api/admin/sensitive-words/status` — return categories + word count
  - `POST /api/admin/sensitive-words/match` — test-match arbitrary text
- `admin/handler.go` — `SetSensitiveWordEngine` + nil-safe route registration
- `cmd/gateway/main.go` — inject engine into admin handler via v2DispatchDeps
- `cmd/gateway/main_pipeline.go` — engine init, plugin registration, deps exposure

### Config

- `configs/sensitive_words.json` — 6 categories, ~60 words, production-ready

### Tests

- `cmd/gateway/sensitive_integration_test.go` — 7 integration tests:
  pass-through, P0/P1 block, output block, output pass, evidence, empty body

## Verification

- `go build ./...` — zero errors
- Integration tests pass (7/7)

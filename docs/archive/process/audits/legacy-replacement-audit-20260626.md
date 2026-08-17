# Legacy Replacement Audit 2026-06-26

> Scope: old code under `_to-be-deprecated/`, plus top-level legacy candidates still outside that directory.
> Goal: verify whether old logic is represented in the new domain architecture before deletion.

## Summary

`_to-be-deprecated/` is already the project-defined pending deletion directory. Most legacy packages are now inside it and preserve their original directory structure. The main deletion blocker is not file location; it is remaining direct or indirect dependency on old packages.

Key findings:
- `go test ./...` does not cover `_to-be-deprecated/` because Go skips wildcard traversal into underscore-prefixed directories.
- Explicit tests for all first-level `_to-be-deprecated` Go packages pass.
- `telemetry/` and `transport/` inside `_to-be-deprecated/` currently have no external imports and no internal `_to-be-deprecated` reverse imports.
- New domain code no longer imports `_to-be-deprecated/relay`, `_to-be-deprecated/transform`, or `_to-be-deprecated/transport`; remaining domain-level legacy dependency is `_to-be-deprecated/memora` only.
- `cache/` remains top-level and has no external imports, but it is not fully replaced by `domains/hooks/cache`; old `cache/prefix` and `cache/semantic` contain concrete prefix/semantic cache logic while the new domain package is a Hook abstraction and in-memory store.
- `security/armor` remains top-level and is still imported by `cmd/gateway/main.go` and `domains/streaming/handler.go`; it is not safe to move.

## Current Status Table

| Legacy path | Location | External refs outside `_to-be-deprecated` | Internal old reverse refs | New architecture status | Action |
|---|---:|---:|---:|---|---|
| `audit/` | `_to-be-deprecated/audit` | 0 | 26 | New audit domain exists | Keep; old relay/routing still depend on it |
| `auth/` | `_to-be-deprecated/auth` | 0 | 12 | New authentication domain exists | Keep; old relay/sessions still depend on it |
| `circuit/` | `_to-be-deprecated/circuit` | 0 | 13 | New credential breaker exists | Keep; old relay/routing still depend on it |
| `compressor/` | `_to-be-deprecated/compressor` | 0 | 2 | New compression domain exists | Keep; old relay/routing still depend on it |
| `credentialstate/` | `_to-be-deprecated/credentialstate` | 0 | 2 | New credential writer exists | Keep; old routing still depends on it |
| `identity/` | `_to-be-deprecated/identity` | 0 | 9 | New identity domain exists | Keep; old relay/routing still depend on it |
| `limiter/` | `_to-be-deprecated/limiter` | 0 | 15 | New credential limiter exists | Keep; old relay/routing still depend on it |
| `memora/` | `_to-be-deprecated/memora` | 0 | 5 | Partially replaced | Keep; still used by old compressor/routing |
| `relay/` | `_to-be-deprecated/relay` | 0 | 1 | New streaming/transformation domains exist | Keep until old transport/relay chain is removed |
| `routing/` | `_to-be-deprecated/routing` | 0 | 6 | New streaming/routing domains exist | Keep until old relay chain is removed |
| `sessions/` | `_to-be-deprecated/sessions` | 0 | 12 | New session domain exists | Keep; old relay/routing still depend on it |
| `telemetry/` | `_to-be-deprecated/telemetry` | 0 | 0 | New observability telemetry domain exists | Ready-to-delete candidate after final owner approval |
| `transform/` | `_to-be-deprecated/transform` | 0 | 12 | New transformation domain exists | Keep; old compressor/relay/routing still depend on it |
| `transport/` | `_to-be-deprecated/transport` | 0 | 0 | New transformation transport domain exists | Ready-to-delete candidate after final owner approval |
| `cache/` | top-level `cache/` | 0 | 0 | Not fully equivalent | Do not move/delete; prefix/semantic logic is not in domain cache |
| `security/armor` | top-level `security/armor` | 2 | 0 | Not fully equivalent | Do not move/delete; still imported by production code |

## Remaining Direct Legacy Imports

After this round of cleanup, direct legacy imports are narrowed to:

- `cmd/gateway/main.go` → `_to-be-deprecated/relay`, `_to-be-deprecated/transform`
- `domains/hooks/compression/compaction.go` → `_to-be-deprecated/memora`
- `domains/streaming/executors/executor.go` → `_to-be-deprecated/memora`
- `domains/streaming/executors/context_summarize.go` → `_to-be-deprecated/memora`

That means `transport` has been fully removed from the production gateway entrypoint, and `relay/transform` are no longer imported anywhere under `domains/`.

## Tests Run

```bash
go test ./cache/... ./security/... ./domains/hooks/cache ./domains/hooks/security ./domains/hooks/observability ./domains/hooks/observability/telemetry
```

Result: PASS.

```bash
for d in _to-be-deprecated/*; do
  if [ -d "$d" ] && [ "$(find "$d" -maxdepth 1 -name '*.go' | wc -l | tr -d ' ')" != "0" ]; then
    go test "./$d"
  fi
done
```

Result: PASS for all first-level `_to-be-deprecated` Go packages; `orphan-tests` has no test files.

```bash
go test ./domains/... ./credentialfpslot ./_to-be-deprecated/routing
go build ./cmd/gateway
```

Result: PASS.

## Verification Notes

- `telemetry` and `transport` are the only current first-level `_to-be-deprecated` packages with zero non-test external refs and zero internal old reverse refs.
- Deleting or further nesting these packages would reduce testable historical reference material. Since `_to-be-deprecated/` already marks pending deletion, this audit does not move them again.
- `cache/` is not a deletion candidate despite zero imports. It contains prefix stabilization and semantic cache logic that is not present in `domains/hooks/cache`.
- `security/armor` is still live code and is not a deletion candidate.

## Required Follow-Ups

1. Decide whether to introduce a second-stage directory such as `_to-be-deprecated/_ready-to-delete/`. Current project documentation treats `_to-be-deprecated/` itself as the pending deletion directory, so no second-stage move was performed.
2. If owner approves hard deletion candidates, start with `_to-be-deprecated/telemetry` and `_to-be-deprecated/transport` only.
3. Before deleting any other old package, remove old internal dependency chains rooted in `_to-be-deprecated/relay` and `_to-be-deprecated/routing`.
4. Add an explicit CI target for `_to-be-deprecated` packages if they must remain testable, because `go test ./...` will not include them.

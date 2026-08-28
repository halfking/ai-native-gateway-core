# 2026-08-29 Post-Merge Audit

## Scope

Multi-branch integration on `main` covering:

1. `5ba33d9e3` — gated native Responses SSE capability (new)
2. `e55f71305` — unified legacy stream contract + Gemini writer hardening
3. `f627ee24e` — URSM apply_decision TOCTOU
4. `924523f04` — URSM migration ledger fsync
5. `be887f814` — invalidation subscriber lifecycle
6. `c0719c699` — XFF trusted-proxy allowlist
7. `e6d634644` — deploy-lib unlock-remote.sh hardening
8. `63e27ab49` — Dockerfile non-root hardening
9. `1be46e902` — streaming/URSM audit closeout (planner / attempt_commit_gate /
   serialized_stream_writer / survival_coordinator / migration copy)

Approach: one lead review + four parallel read-only sub-agent audits (URSM/Redis,
XFF+deploy+Docker, streaming/anthropic, closeout batch). `go build ./...`,
`go vet`, and the affected package test suites were run locally and are green.

## Key findings and resolutions

| Sev | Area | Issue | Status |
|---|---|---|---|
| P1 | Gemini writer | `failClosed` retained up to 16 MiB pending buffer for the rest of the request | **Fixed** (`w.pending = nil` + regression assert) |
| P1 | XFF trust list | `request_meta.go` and `domains/identity` still called `telemetry.ExtractClientIP` directly, bypassing the new allowlist | **Fixed** (both now prefer `middleware.ContextClientIP`, fall back to raw headers only when the middleware never ran) |
| P2 | Native Responses stream | Duplicate `response.completed` after `response.failed` could flip an interrupted capture back to done | **Fixed** (`MarkDone` guarded by terminal flag) |
| P2 | Dockerfile | Comment promised `BASE_IMAGE_DIGEST` but the builder stage never consumed it | **Fixed** (`GO_IMAGE_DIGEST` declared and wired into `FROM`) |
| P2 | unlock-remote.sh | Unparseable `started_at` made `--detect-stale` report "fresh" and exit 0 | **Fixed** (unknown age now exits 75) |
| P3 | unlock-remote.sh | Audit-log JSONL fields are not escaped; holder metadata containing `"` or newlines would corrupt the audit line | Open (documented) |
| P3 | URSM manager | `Manager.Close` reads `invalidationStop` without the mutex; fine today (single start path) but a latent race if start is ever made concurrent | Open (documented) |
| P3 | survival_coordinator | `res.History` is consumed only by the aggregation layer; `Refresh` does not prune exhausted nodes from the candidate set | Open (documented; bounded by `MaxRetries`) |
| P3 | planner | `ActionWaitRecovery`'s `RetryAfter` is dropped when the decision falls through to the switch ladder | Open (documented) |
| P3 | apply_decision.lua | ARGV[6] is now a dead parameter kept for ABI parity | Open (documented) |

Items marked Fixed are in the working tree; the rest are recorded here for the
next cleanup pass and intentionally left unchanged to keep this merge review
narrow.

## Code-loss check

All `stash@{0..4}` entries (created between 04:04 and 04:58 to preserve
intermediate native-responses/provider/SQL state) were diffed against HEAD.
Every change they contain is either byte-identical to HEAD or an older snapshot
that HEAD supersedes (migration 611 narrowly in HEAD now opens the constraint
for both keys; migration 612 is present on HEAD). No code was lost.

## Verification

- `go build ./...` — clean
- `go vet ./domains/... ./provider/... ./middleware/...` — clean
- `go test -count=1` on: `domains/streaming`, `domains/streaming/executors`,
  `domains/transformation/anthropic`, `provider`, `middleware`, `domains/ursm/...`
  — all pass
- `bash -n scripts/deploy-lib/unlock-remote.sh` — clean (bash 3.2 compatible)

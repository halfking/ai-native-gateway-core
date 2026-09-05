# Distributed Dispatch Controls Integration

## Background

After the dispatch governor and probe worker audit fixes, the follow-up track
implemented the remaining architecture items called out in the 2026-08-24
handoff:

1. Distributed RPM/TPM Redis governor backed by atomic Lua.
2. Bounded total execution queue (1000 deep) sitting between the dispatcher and
   per-credential forwarders.
3. Priority credential clusters with session-affinity preservation through
   credential/provider/model failover.
4. Atomic minute-bucket metrics keyed by provider, model, credential, and
   outcome.
5. Tentative restore with delayed-rollback revert window (`bg/probe_rollback.go`).

The work landed on `feat/dispatch-selfcheck-followup-20260824` and was merged
into `main` as commit `e20dd089a merge: integrate distributed dispatch controls`
(origin/main parent: `9feb50728`).

## Files

- `domains/dispatch/minute_stats.go` — minute-bucket aggregation primitives.
- `domains/dispatch/minute_stats_test.go` + `minute_stats_pipeline_test.go`.
- `domains/dispatch/priority_affinity.go` + `priority_affinity_test.go` —
  priority cluster + session affinity updates through failover.
- `domains/dispatch/total_queue.go` + `total_queue_test.go` — bounded FIFO
  total execution queue.
- `domains/dispatch/redis_backend.go` — extended with distributed Lua-backed
  rate governance.
- `domains/dispatch/redis_rate_governor_test.go` — Redis governor unit tests.
- `cmd/gateway/dispatch_session_affinity.go` — wiring for session affinity
  state propagation.
- `cmd/gateway/main.go`, `cmd/gateway/main_dispatch.go` — composition-root
  integration.
- `bg/probe_rollback.go` + `bg/probe_rollback_contract_test.go` — delayed
  rollback worker for tentative probe restores.
- `bg/balance_quota_probe.go`, `bg/credential_autoheal.go`,
  `bg/credential_recovery.go`, `bg/periodic_quota_probe.go` — probe integration
  updates.

## Verification

- `e20dd089a` resolves `domains/dispatch/forwarder.go` merge conflict between
  the fix/stream-terminal-probe-attribution release ordering and the
  feat-side reorder. The mirror ordering fix (publish in-flight zero before
  waking Submit) is preserved on the merged branch.
- origin/main = local main = `e20dd089a` (0 ahead / 0 behind after
  `git fetch origin`).
- `go build ./cmd/gateway` PASS on local.
- The earlier verification gate (govulncheck reachable 0, focused dispatch
  tests 100/100, race 25/25) still applies to the merged tree.

## Follow-up

- 245 `llm-gateway-go.service` remains inactive while an independent gateway
  process owns 8781; service ownership/startup failure must be investigated
  before the next 245 deployment.
- CHANGELOG.md `[Unreleased]` should be updated to reference this integration
  once the next release cut happens.
- Tentative restore confirmation probe gating (`confirmProbe` decision) is
  implemented but should be observed in staging before promoting.

## Follow-up Update (2026-08-24, later session)

- The 245 service-ownership question above is resolved: the controller on 245
  is `llmgo-245.service` (enabled + active, owning port 8781), and the
  `llm-gateway-go.service` unit observed as inactive is the deprecated
  leftover unit — see
  `docs/session-logs/2026/08/2026-08-19-245-restart-loop-followup.md` for the
  double-unit drift analysis. Live re-probe (2026-08-24) confirmed
  `llmgo-245.service` active with `/opt/llm-gateway-go/gateway` (PID 237156)
  listening on 8781, healthz 200. The deploy-gate runner's 245 expected
  service has been aligned to `llmgo-245.service` to match
  `scripts/deploy.sh plan 245`; no second service name is introduced.

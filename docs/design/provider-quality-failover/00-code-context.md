# Provider Quality And Failover Code Context

## Entry Points

- Candidate query: `provider/client.go:GetCandidatesByModality`.
- Stream failover: `domains/streaming/executors/executor_chat.go`.
- Error taxonomy: `errorsx/classify.go`.
- Node probes: `bg/node_probe.go:ProbeSync`.
- Routing diagnostics: `admin/diagnostics_routing.go`.

## Runtime Facts (245, 2026-08-21)

- `gpt-5.6-terra` has configured nodes 10, 37, and 40.
- Node 10 is blocked by a provider-level manual disable and must not be auto-enabled.
- Node 37 is ready and directly reachable but was removed by the recent-success hard gate.
- Node 40 is ready but has slow, timeout, and quota-failure history.
- Historical gateway-probe 401s are a probe authentication/path fault, not supplier unavailability.

## Scope

- Preserve manual, permanent quota, auth, model, and probe-state hard exclusions.
- Preserve degraded live siblings for failover and rank them by quality.
- Add read-only diagnostics from existing request and probe audit data.
- Validate on 245 before any 154 production change.

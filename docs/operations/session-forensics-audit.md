# SessionForensics Audit Matrix

This runbook defines the repeatable validation contract for session replay and
the six operational analysis scenarios. It uses deterministic synthetic packs
for CI and optionally replays locally exported session JSON files.

## Command

Run the complete focused audit from the repository root:

```bash
make audit-sessionforensics
```

Set `ABS_SESSIONS_DIR` when the exported session directory is outside the
repository. Real-data tests skip only when no local manifest is available;
synthetic coverage always runs.

```bash
ABS_SESSIONS_DIR=/secure/local/session-export make audit-sessionforensics
```

## Scenario Matrix

| Scenario | Offline assertion | Mutation or fixture | Production evidence |
| --- | --- | --- | --- |
| Summary | A non-empty fallback title and summary are generated without an LLM. | Empty and long first-user-message fixtures. | Compare fallback and configured LLM result source; do not store raw prompt text in reports. |
| Panorama | Exported turn count equals replayed turn count and turn evidence remains correlated. | Empty or malformed request body fixture. | Verify request ID, timestamp, model, and timeline fields against the tenant-scoped analytics API. |
| Prompt injection | Each user turn is checked by the production prompt-injection plugin. | M3 cut-and-append with an injection payload. | Review blocked verdict count and decision trace metadata; redact original prompt content. |
| Output compliance | The pipeline preserves output on checker failure, redacts configured output, and blocks policy failures. | Fake checker outputs for clean, redact, block, error, and nil result. | Compare issue count, block/redact action, and content hashes only. |
| Health | Score penalties, grades, and outcome classification are deterministic. | Error-dominated, abandoned, high-latency, PII, toxic, and injection summary fixtures. | Compare the score to the tenant-scoped session health API for the same summary snapshot. |
| Cluster | Rule and vector clustering retain tenant boundaries and preserve member scores. | Same-topic, dissimilar-topic, invalid vector, and missing-summary fixtures. | Run against sampled tenant data, then verify every member belongs to exactly one expected cluster. |

## Required Dataset Coverage

CI must retain the synthetic fixtures. For a scheduled operational audit, use
a sampled and redacted local export containing at least:

- 100 sessions across more than one model and outcome.
- 10 multi-turn sessions with at least 10 requests each.
- 10 error or compliance finding sessions.
- 20 sessions sharing an intent/topic for cluster stability checks.
- At least one long-context session and one streaming-response session.

Never commit raw production session exports. Keep exports in an ignored local
directory with tenant access controls, and record only aggregate counts,
hashes, and redacted reports in CI artifacts.

## Acceptance Criteria

- `make audit-sessionforensics` passes.
- Every `ScenarioName` has either a passing adapter result or an explicit
  unavailable result; a partial run is not a passing audit.
- Mutation tests cover global and turn-scoped transformations without panics.
- Replay reports preserve request ID, timestamp, model, success/error state,
  latency, and response-presence metadata where the source export contains it.
- Output compliance failures and nil checker results degrade safely without
  leaking output or crashing the request pipeline.

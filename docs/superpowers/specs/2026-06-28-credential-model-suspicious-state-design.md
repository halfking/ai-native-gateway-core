# Credential × Model Suspicious State — Design

> **Status:** Approved (2026-06-28)
> **Owner:** llm-gateway-go health-check team
> **Related:** [`bg/model_probe.go`](../../../bg/model_probe.go), [`bg/credential_probe_v2.go`](../../../bg/credential_probe_v2.go), [`db/migrations/011_model_probe_state.sql`](../../../db/migrations/011_model_probe_state.sql)

## 1. Motivation

The current per-(credential, model) state machine in `model_probe_state` has 4 values:
`unknown`, `recovering`, `healthy_confirmed`, `broken_confirmed`. A binding that
has reached `healthy_confirmed` only gets re-verified when its 2-hour watchdog
fires on the next 10-min cycle — and only if the cycle happens to pick it up.
Between the 2h mark and the actual re-probe (which can be delayed by backoff
or cycle contention), the binding is **treated as healthy with no fresh
evidence**. Similarly, a `broken_confirmed` binding's 7-day stop is correct
for "permanently broken" but wrong for "transiently broken — should retry in
2h".

We need an explicit **suspicious** state for bindings whose last successful
verification is older than 2h. Suspicious bindings are still routable (we
have no positive evidence they're broken), but the system actively re-pings
them in the background so a stale "healthy" never lingers undetected.

## 2. Goals

1. Make "verification age > 2h" a first-class state (`suspicious`), not an
   implicit gap between watchdog ticks.
2. Background ping **only** suspicious bindings (don't waste cycles on
   already-healthy or already-broken pairs).
3. Per-credential concurrency cap of **2** for these pings — a single
   high-cardinality credential must not let 50 concurrent probes hit the
   upstream.
4. When production routing actually selects a suspicious binding, exit
   suspicious immediately (state flip only — no synchronous probe) so the
   next background tick picks it up.
5. Every probe outcome goes to the existing `model_probe_runs` table for
   audit + admin UI.
6. Backwards compatible: no breaking change to `v_routable_credential_models`,
   no breaking change to `model_probe_state` consumers.

## 3. Non-Goals (YAGNI)

- Admin UI rendering for suspicious state (API is enough; existing
  providers-page "auto-test" tab already surfaces `model_probe_state`).
- Per-tenant overrides of the 2h threshold.
- Deeper "LLM-content" verification (e.g. "actually answer a math question").
  A ping-style call (`probeWithRetry` with `max_tokens=1`) is the spec.
- Adding `suspicious` to `credentials.availability_state` enum — we keep
  this layer in `model_probe_state` only.

## 4. State Machine

```
            ┌─────────────────────────────────┐
            ▼                                 │
[unknown] ──probe──► [recovering]              │
            ▲           │    ▲                │
            │           ▼    │                │
        first│       3 OK │ 3 FAIL            │
        probe│           │    │                │
            │           ▼    ▼                │
            │   [healthy_confirmed]  [broken_confirmed]
            │           │    │                │
            │  no activity│    │ manual nudge  │
            │   for 2h    │    │               │
            │           ▼    │                │
            └─── [suspicious] ◄───────────────┘
                      │
            2h 触发 │  │ 被生产路由层选中
                 ▼  │  ▼ (只切状态)
            next-retry 切到 [recovering]
            worker picks up
```

| state               | binding.available | routable? | next_retry_at formula                  |
|---------------------|-------------------|-----------|----------------------------------------|
| `unknown`           | TRUE              | yes       | `NOW()`                                |
| `recovering`        | depends           | partial   | `NOW() + model_probe_backoff_v2(N)`    |
| `healthy_confirmed` | TRUE              | yes       | `NOW() + INTERVAL '2 hours'` (watchdog — *deprecated by this spec; replaced by suspicious transition*) |
| `suspicious`        | TRUE              | yes       | `NOW() + INTERVAL '30 seconds'` (so pinger picks it up within 30s) |
| `broken_confirmed`  | FALSE             | no        | `NOW() + INTERVAL '7 days'` (manual nudge required) |

**Important**: `healthy_confirmed` no longer writes the 2h watchdog. Instead,
the `SuspiciousStateTransitor` worker transitions `healthy_confirmed` →
`suspicious` after 2h of no `last_attempt_at` update. This makes the
"verification age" loop first-class and observable.

## 5. Component Design

### 5.1 `bg/suspicious_state_transitor.go` — `SuspiciousStateTransitor`

- Tick every **60s** (cheap; mostly no-op).
- One SQL UPDATE that flips `healthy_confirmed` → `suspicious` if
  `last_attempt_at < NOW() - INTERVAL '2 hours'` (configurable via
  `LLM_GATEWAY_SUSPICIOUS_AFTER_SECONDS` env, default `7200`).
- Guards: `state='healthy_confirmed'`, `lifecycle_status='active'`,
  `manual_disabled=FALSE`, `availability_state NOT IN ('suspended')`,
  `quota_state NOT IN ('permanently_exhausted', 'balance_exhausted')`.
- Sets `next_retry_at = NOW() + INTERVAL '30 seconds'` so the pinger picks
  it up on the next tick.
- Updates `last_state_change_at = NOW()`.
- Logs a single `slog.Info` per cycle with the count transitioned.
- Does **not** touch `credential_model_bindings.available` (stays TRUE).

### 5.2 `bg/suspicious_pinger.go` — `SuspiciousPinger`

- Tick every **30s**.
- Selects up to `MaxSuspiciousPerCycle` (default 50) rows where
  `state='suspicious' AND next_retry_at <= NOW()`, ordered by
  `last_attempt_at NULLS FIRST` (oldest first).
- **Concurrency control**: per-credential semaphore of 2 (from
  `internal/probe/concurrency.go`). Uses `golang.org/x/sync/errgroup` to
  parallelize across different credentials while throttling within a
  credential.
- For each (cred, model) target:
  1. Decrypt secret
  2. Call existing `probeWithRetry(ctx, desc, t, ProbeModeChatPing)`
  3. Compute consensus via existing `computeConsensus(...)`
  4. `recordRun` (existing) — populates `model_probe_runs`
  5. `applyResult` (existing) — updates `model_probe_state` + flips
     `credential_model_bindings.available` on the consensus transition
- Cycle timeouts: 5 minutes total, 30s per individual probe (via
  `probeChatClient.Timeout`).
- Logs cycle summary: `picked`, `pinged`, `recovered_to_healthy`,
  `recovered_to_broken`, `kept_suspicious`, `errors`, `concurrent_peak`.

### 5.3 `internal/probe/concurrency.go` — `PerCredentialSemaphore`

- Bounded map of `int64 (credential_id) → *semaphore.Weighted (capacity=2)`.
- `Acquire(ctx, credID)` blocks if 2 are already in-flight for that cred.
- `Release(credID)` decrements.
- `InFlight(credID) int` for admin API + tests.
- **Scoped use**: only `SuspiciousPinger` uses this. `ModelProbeRunner.cycle`
  and `CredentialProbeV2.cycleAll` are unaffected — they probe already-broken
  bindings and existing concurrency is controlled by their batch size + DB
  locking.

### 5.4 Call-exit hook (in routing layer)

- Location: where `v_routable_credential_models` is read, before returning
  candidates to the executor.
- Logic: for each candidate, if
  `model_probe_state.state = 'suspicious'`, fire-and-forget
  `UPDATE model_probe_state SET state='recovering', next_retry_at=NOW() + INTERVAL '30 seconds', consecutive_successes=0, consecutive_failures=0, total_attempts=total_attempts+1, last_state_change_at=NOW() WHERE credential_id=$1 AND raw_model_name=$2 AND state='suspicious'`.
- The UPDATE is on a fresh 1s context (independent of the request context)
  to keep the request hot path completely unblocked. Failures are logged
  but never returned to the caller.
- **Idempotent**: the `state='suspicious'` guard means re-entry is safe.

## 6. Data Flow

### 6.1 2h decay → suspicious

```sql
-- Every 60s, runs in SuspiciousStateTransitor.cycle
UPDATE model_probe_state mps
SET state                = 'suspicious',
    next_retry_at        = NOW() + INTERVAL '30 seconds',
    last_state_change_at = NOW()
FROM credential_model_bindings cmb
JOIN provider_models pm  ON pm.id = cmb.provider_model_id
JOIN credentials c       ON c.id = cmb.credential_id
JOIN providers p         ON p.id = c.provider_id
WHERE mps.credential_id  = cmb.credential_id
  AND mps.raw_model_name = pm.raw_model_name
  AND mps.state          = 'healthy_confirmed'
  AND mps.last_attempt_at < NOW() - make_interval(secs => $1)
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
  AND COALESCE(p.manual_disabled, FALSE) = FALSE
  AND COALESCE(c.availability_state, 'ready') NOT IN ('suspended')
  AND COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
RETURNING mps.credential_id, mps.raw_model_name;
```

### 6.2 Background ping

```sql
-- Every 30s, runs in SuspiciousPinger.cycle
SELECT cmb.credential_id,
       pm.raw_model_name,
       COALESCE(pm.outbound_model_name, '') AS outbound_model,
       COALESCE(p.base_url, '')            AS base_url,
       COALESCE(p.protocol, 'openai-completions') AS protocol,
       c.secret_ciphertext,
       COALESCE(c.manual_disabled, FALSE)  AS manual_disabled
FROM model_probe_state mps
JOIN credential_model_bindings cmb ON cmb.credential_id = mps.credential_id
JOIN provider_models pm            ON pm.id = cmb.provider_model_id
                                   AND pm.raw_model_name = mps.raw_model_name
JOIN credentials c                 ON c.id = mps.credential_id
JOIN providers p                   ON p.id = c.provider_id
WHERE mps.state         = 'suspicious'
  AND mps.next_retry_at <= NOW()
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
  AND COALESCE(c.availability_state, 'ready') NOT IN ('suspended')
  AND COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
  AND cmb.available = TRUE
ORDER BY mps.last_attempt_at NULLS FIRST, mps.credential_id
LIMIT $1;
```

Per-credential semaphore limits in-flight probes to 2. Each successful
probe writes to `model_probe_runs` (existing `recordRun`).

### 6.3 Call exit

```sql
-- Inside call-exit hook, on candidate resolution
UPDATE model_probe_state
SET state                = 'recovering',
    next_retry_at        = NOW() + INTERVAL '30 seconds',
    consecutive_successes = 0,
    consecutive_failures  = 0,
    total_attempts        = total_attempts + 1,
    last_state_change_at  = NOW()
WHERE credential_id = $1
  AND raw_model_name = $2
  AND state = 'suspicious';
```

The UPDATE runs on a `context.WithTimeout(context.Background(), 1*time.Second)`
to keep the request hot path completely off the DB critical path. Failures
are logged at WARN and never surfaced to the caller.

## 7. Configurability

| Env var                              | Default | Component                  | Notes |
|--------------------------------------|---------|----------------------------|-------|
| `LLM_GATEWAY_SUSPICIOUS_AFTER_SECONDS` | 7200   | `SuspiciousStateTransitor` | Age threshold for healthy → suspicious |
| `LLM_GATEWAY_SUSPICIOUS_PING_INTERVAL` | 30s    | `SuspiciousPinger`         | Worker tick |
| `LLM_GATEWAY_SUSPICIOUS_TRANSITION_INTERVAL` | 60s | `SuspiciousStateTransitor` | Worker tick |
| `LLM_GATEWAY_SUSPICIOUS_MAX_BATCH`   | 50      | `SuspiciousPinger`         | Max rows per cycle |
| `LLM_GATEWAY_SUSPICIOUS_MAX_CONCURRENCY` | 2   | `SuspiciousPinger`         | Per-credential cap |
| `LLM_GATEWAY_SUSPICIOUS_ENABLE`      | true    | both workers               | Master kill-switch |

All env reads use the existing `os.Getenv` + `strconv.Atoi` / `time.ParseDuration`
pattern (see `mnfCoolingRecoveryMinutes` for reference).

## 8. Observability

### 8.1 Structured logging

Every state transition logs:
- `from_state`, `to_state`, `credential_id`, `raw_model_name`,
  `trigger` (`decay` | `call_exit` | `ping_success` | `ping_failure` |
  `ping_consensus_healthy` | `ping_consensus_broken`),
  `latency_ms`, `consecutive_successes`, `consecutive_failures`.

### 8.2 `model_probe_runs` (existing)

Each ping writes one row with `triggered_by='suspicious_pinger'` (new value)
or `triggered_by='suspicious_call_exit'` (no actual probe, but a record
that a call flipped the state — useful for the timeline view). The
existing columns are sufficient.

### 8.3 Admin API (3 new endpoints in `admin/handlers_probe.go`)

| Method | Path | Purpose |
|--------|------|---------|
| `GET`  | `/api/admin/probe/state-summary` | Counts by state, with `suspicious` listed explicitly. Filter: `?state=suspicious&since=2h` |
| `GET`  | `/api/admin/probe/credential/{id}/timeline?since=24h` | Merged view: state rows + probe_runs rows for the (cred, all_models) in the last 24h |
| `GET`  | `/api/admin/probe/concurrency?credential_id=X` | Returns `{credential_id, in_flight: N, max: 2}` for the given cred. Useful when debugging "why isn't this ping firing". |

Auth: reuse the existing `admin` JWT middleware (`requireAdmin`).

## 9. Migration Plan

### 9.1 Schema

**No new tables.** `model_probe_state.state` is a `TEXT` column with no
CHECK constraint (verified — only `model_probe_state` with the new value
`apihub_assets.health_state` has one). Adding the new value `suspicious`
is a code-only change.

**Optional defensive migration** (`db/migrations/300_state_suspicious.sql`):
a comment + a function to list valid states for ops. Not load-bearing.

### 9.2 Code changes

| File | Change |
|------|--------|
| `bg/suspicious_state_transitor.go` | NEW — `SuspiciousStateTransitor` worker |
| `bg/suspicious_state_transitor_test.go` | NEW — unit tests |
| `bg/suspicious_pinger.go` | NEW — `SuspiciousPinger` worker |
| `bg/suspicious_pinger_test.go` | NEW — unit tests |
| `internal/probe/concurrency.go` | NEW — `PerCredentialSemaphore` |
| `internal/probe/concurrency_test.go` | NEW — concurrency tests |
| `bg/model_probe.go` | MODIFY — `applyResult` for `healthy_confirmed` writes `next_retry_at = NOW() + INTERVAL '5 minutes'` (not 2h). This is a safe shortening — the transitor will pick it up at 2h regardless, and the shorter interval ensures the watchdog fires for a binding whose `last_attempt_at` was never updated. |
| `cmd/gateway/main.go` | MODIFY — wire both workers in `bg services enabled block` |
| `admin/handlers_probe.go` | MODIFY — 3 new endpoints |
| `bg/probe_http.go` | UNCHANGED (we reuse `probeWithRetry`) |
| `bg/probe_http_test.go` | UNCHANGED |

### 9.3 Deployment

1. Deploy with `LLM_GATEWAY_SUSPICIOUS_ENABLE=false` → confirm boot is clean
2. After 1 cycle (60s), flip to `true` → confirm transitor starts and no
   spike in DB load
3. Monitor `slog` for `suspicious_pinger` summary lines
4. Roll back via the env flag if needed (no schema rollback needed)

## 10. Testing Strategy

### 10.1 Unit tests (table-driven)

- `TestSuspiciousStateTransitor_Decays` — `last_attempt_at = NOW() - 3h`
  → state flips to `suspicious`
- `TestSuspiciousStateTransitor_KeepsRecent` — `last_attempt_at = NOW() - 30m`
  → no change
- `TestSuspiciousStateTransitor_SkipsManualDisabled` — guard works
- `TestSuspiciousStateTransitor_SkipsSuspended` — guard works
- `TestSuspiciousPinger_RespectsConcurrencyCap` — 10 same-cred targets,
  `InFlight(cred)` never exceeds 2
- `TestSuspiciousPinger_DifferentCredsUncapped` — 5 different creds, all
  probe in parallel
- `TestSuspiciousPinger_SuccessToRecovering` — 1 success → state=
  `recovering`, `consecutive_successes=1`
- `TestSuspiciousPinger_3SuccessToHealthy` — 3 successes → `healthy_confirmed`
- `TestSuspiciousPinger_3FailureToBroken` — 3 failures → `broken_confirmed`
  + `cmb.available = FALSE`
- `TestSuspiciousPinger_RecordsRun` — `model_probe_runs` row written with
  `triggered_by='suspicious_pinger'`
- `TestCallExitSuspiciousHook_FlipsState` — UPDATE happens, returns nil
- `TestCallExitSuspiciousHook_NoOpOnHealthy` — UPDATE skipped
- `TestCallExitSuspiciousHook_NoOpOnBroken` — UPDATE skipped
- `TestPerCredentialSemaphore_Blocks3rd` — 3rd goroutine blocks
- `TestPerCredentialSemaphore_Release` — after release, next Acquire
  succeeds
- `TestPerCredentialSemaphore_DifferentCredsIndependent`

### 10.2 Integration test (1 file, can run on 71 staging)

- `e2e-suspicious-cycle.sh`:
  1. Start local mock upstream that returns 200 on `/v1/chat/completions`
  2. Insert 1 (cred, model) binding pointing at mock
  3. Insert `model_probe_state` row with `state='suspicious'`
  4. Set `LLM_GATEWAY_SUSPICIOUS_AFTER_SECONDS=2` for fast iteration
  5. Run `SuspiciousPinger.cycle` directly (bypass ticker)
  6. Assert: `model_probe_state.state = 'recovering'`,
     `consecutive_successes = 1`, 1 row in `model_probe_runs`

### 10.3 Manual verification

- After deploy: `psql -c "SELECT state, count(*) FROM model_probe_state GROUP BY state"`
  should show a non-zero `suspicious` count within 2h of any binding's
  last verification.
- Admin UI: `/api/admin/probe/state-summary?state=suspicious` returns
  the count + the 3 new admin endpoints work.

## 11. Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Transitor SQL too broad (e.g. flips healthy bindings in suspended creds) | Defensive WHERE: `availability_state NOT IN ('suspended')`, `manual_disabled=FALSE`, `lifecycle_status='active'` |
| Concurrency cap leaks goroutines if Release is missed | Use `defer Release()` in a wrapper; recover() from panics |
| Ping hammering a single credential | Per-cred semaphore (the whole point) + 30s backoff if a probe fails |
| 2h decay is too aggressive for some providers | Env override `LLM_GATEWAY_SUSPICIOUS_AFTER_SECONDS`; admin can dial up to 24h |
| Routing layer hook adds DB round-trip to hot path | 1s timeout + `state='suspicious'` guard means UPDATE is a single row, ~1-2ms typical, no-op on cache hit |
| `applyResult` consensus conflict with `SuspiciousPinger` | They both use the same `computeConsensus` + `applyResult` — single source of truth. The transitor only writes state, not consensus counters. |

## 12. Open Questions

None at design approval time. All 3 design questions (state location,
call-exit behavior, healthy_confirmed coexistence) were resolved in the
brainstorming phase (see chat history).

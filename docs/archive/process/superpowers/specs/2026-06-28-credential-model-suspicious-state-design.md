# Credential × Model Availability — Unified Design (v2)

> **Status:** v2 Approved (2026-06-28)
> **Supersedes:** v1 (`23663504`) — v1's suspicious state machine is preserved; v2 adds Redis state cache, unification of 5+ existing probe workers, and configurable per-worker intervals.
> **Owner:** llm-gateway-go health-check team
> **Related files:** [`bg/model_probe.go`](../../../bg/model_probe.go), [`bg/credential_probe_v2.go`](../../../bg/credential_probe_v2.go), [`bg/passive_probe_listener.go`](../../../bg/passive_probe_listener.go), [`bg/credential_recovery.go`](../../../bg/credential_recovery.go), [`bg/health_auto_recover.go`](../../../bg/health_auto_recover.go), [`db/migrations/011_model_probe_state.sql`](../../../db/migrations/011_model_probe_state.sql)

---

## 1. Motivation

The current llm-gateway-go deployment runs **5+ independent probe workers** that
each maintain their own HTTP client, decryption path, retry logic, and DB
write path:

| Worker | File | Cadence | Purpose |
|--------|------|---------|---------|
| `CredentialProbeV2` | `bg/credential_probe_v2.go` | hourly + 5min fast-reprobe | Auth sanity + URL liveness + balance |
| `ModelProbeRunner` (cycle) | `bg/model_probe.go` | every 10min | Per-model consensus recovery |
| `ModelProbeRunner` (featuredCycle) | `bg/model_probe.go` | every 30min | Featured models deep probe |
| `PassiveProbeListener` | `bg/passive_probe_listener.go` | every 30s | React to production failures |
| `CredentialCycler` | `bg/credential_cycler.go` | hourly | Sticky rotation (kept separate, not in scope) |
| `HealthAutoRecover` | `bg/health_auto_recover.go` | every 1min | State hygiene sweep |
| `CredentialRecovery` | `bg/credential_recovery.go` | every 1min | Cooldown expiry + circuit breaker reset |

Plus the **new requirement** (v1 design, commit `23663504`) for an explicit
`suspicious` state with 2h decay, background ping, and call-exit hook.

**Problems**:
1. **Duplicated infrastructure**: each worker re-implements secret decryption,
   HTTP dispatch with retry, and state DB writes. ~1200 lines of overlap.
2. **Inconsistent state**: `model_probe_state` is the canonical "verification"
   state but multiple workers write it via different code paths with
   different consensus rules.
3. **DB hot path**: routing layer reads `model_probe_state` only via
   `v_routable_credential_models` (cmb-derived). Adding 7 probes creates
   extra DB load; routing layer can't read fresh verification state cheaply.
4. **Time gaps**: between `healthy_confirmed`'s 2h watchdog firing and the
   actual re-probe, a binding may sit "verified" with no fresh evidence.
5. **No fast availability query**: any consumer (admin UI, audit, another
   service) wanting "is cred X model Y currently working?" has to query
   the DB with joins.

## 2. Goals (v2)

1. **Unify** all probe-and-state workers behind one `ProbeOrchestrator`
   that exposes them as pluggable `Scheduler`s sharing a common
   `ProbeExecutor` (HTTP + concurrency) and `StateWriter` (DB + Redis).
2. **Add an explicit `suspicious` state** (v1) with 2h decay, background
   ping, and call-exit hook — implemented as 2 new Schedulers
   (`DecayScheduler`, `SuspiciousPingScheduler`) plus a routing hook.
3. **Cache all (credential, model) verification state in Redis** under
   `llmgw:avail:{credential_id}:{model_name}` (Hash, TTL 4h). Routing layer
   and admin API read from Redis first, fall back to DB on miss.
4. **Per-credential concurrency cap of 2** enforced by the shared executor
   across all schedulers (a single high-cardinality credential cannot
   trigger 50 concurrent probes from any combination of schedulers).
5. **Per-scheduler configurable interval** via env (default values match
   existing behaviour).
6. **No state interruption**: TTL (4h) > max decay (2h) + worst-case
   scheduler lag, so Redis always has a fresh entry when DB has one.
   Recovery: if Redis dies, routing falls back to DB and warms the cache
   on read.
7. **Backwards compatible**: existing `v_routable_credential_models`,
   `model_probe_state`, and `model_probe_runs` schemas untouched. New code
   writes to the same tables from the same write paths (now unified).

## 3. Non-Goals (YAGNI)

- Replacing `credential_model_bindings.available` as the routing source
  of truth. It remains the production router's source; the Redis cache
  is a fast projection of `model_probe_state` only.
- Admin UI rendering for new state. API endpoints only — the providers-page
  "auto-test" tab already reads `model_probe_state` and will pick up
  `suspicious` automatically via existing JSON serialization.
- Replacing `CredentialCycler` — sticky rotation is a different concern.
- Per-tenant overrides of intervals. Operators tune globally; tenants
  don't get knobs.
- Replacing `CredentialRecovery`/`HealthAutoRecover` SQL with Redis-only
  recovery. State hygiene still uses SQL because it operates on cross-table
  invariants (cmb ↔ availability_state ↔ model_probe_state).

## 4. Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│              UnifiedProbeOrchestrator (单实例)                   │
│                                                                   │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │                Scheduler Layer (7 schedulers)              │ │
│  │                                                              │ │
│  │  ┌───────────────┐ ┌───────────────┐ ┌───────────────┐      │ │
│  │  │ Credential    │ │ Model         │ │ Suspicious    │      │ │
│  │  │ Probe         │ │ Recovery      │ │ Ping          │      │ │
│  │  │ (was V2)      │ │ (was MP.cycle)│ │ (NEW v1)      │      │ │
│  │  │ 60min         │ │ 10min         │ │ 30s           │      │ │
│  │  └───────┬───────┘ └───────┬───────┘ └───────┬───────┘      │ │
│  │          └─────────────────┬─┴─────────────────┘            │ │
│  │  ┌───────────────┐ ┌───────────────┐ ┌───────────────┐      │ │
│  │  │ Featured      │ │ Decay         │ │ Passive       │      │ │
│  │  │ (was MP.FC)   │ │ (NEW v1)      │ │ Observer      │      │ │
│  │  │ 30min         │ │ 60s (state    │ │ (was PPL)     │      │ │
│  │  │               │ │  only)        │ │ 30s (state    │      │ │
│  │  └───────┬───────┘ └───────┬───────┘  only)          │      │ │
│  │          └─────────────────┴──────────────────┘            │ │
│  │  ┌───────────────┐                                         │ │
│  │  │ Recover       │                                         │ │
│  │  │ (was HR+CRec) │                                         │ │
│  │  │ 60s (state    │                                         │ │
│  │  │  only)        │                                         │ │
│  │  └───────┬───────┘                                         │ │
│  │          └──────────────────────────────────────┐          │ │
│  └──────────────────────────────────────────────────┼──────────┘ │
│                                                      ▼            │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │                ProbeExecutor (shared)                       │ │
│  │   - Acquire per-credential semaphore (capacity 2)          │ │
│  │   - Decrypt secret (keyring + Fernet fallback)             │ │
│  │   - Call probeWithRetry (existing logic, reused)           │ │
│  │   - Compute consensus (existing logic, reused)             │ │
│  └────────────────────────────────────────────────────────────┘ │
│                                  │                                │
│                                  ▼                                │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │                StateWriter (shared)                         │ │
│  │   1. DB: model_probe_state UPSERT                          │ │
│  │   2. DB: model_probe_runs INSERT                           │ │
│  │   3. DB: credential_model_bindings UPDATE (on transition)  │ │
│  │   4. Redis: llmgw:avail:{cred}:{model} HSET + EXPIRE 4h   │ │
│  │   Failure policy: Redis failure logs WARN; DB stays correct│ │
│  └────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
                              │
                              │ (read)
                              ▼
              ┌────────────────────────────────┐
              │   HealthReader (consumed by:    │
              │    - routing layer (hot path)   │
              │    - admin API (cold path)      │
              │    - CallExitSuspiciousHook)    │
              │                                  │
              │   Redis HGETALL → DB fallback  │
              │   → write-through cache warming │
              └────────────────────────────────┘
```

### 4.1 Scheduler interface

```go
type Scheduler interface {
    Name() string                                                            // for slog + metrics
    Interval() time.Duration                                                 // ticker
    IsEnabled() bool                                                         // gate via env
    PickTargets(ctx context.Context, batch int) ([]probeTarget, error)       // read DB/Redis
    ShouldEmitProbe() bool                                                   // some schedulers only update state, no HTTP
}
```

### 4.2 ProbeExecutor (shared)

```go
type ProbeExecutor struct {
    sem     *PerCredentialSemaphore  // capacity 2 per cred, shared across ALL schedulers
    encKey  []byte
    keyring *secret.Keyring
}

func (e *ProbeExecutor) Run(ctx context.Context, t probeTarget, sw *StateWriter) Result
```

- Acquires `sem.Acquire(ctx, t.CredentialID)` — blocks if 2 already in flight for that cred.
- Decrypts secret.
- Calls existing `probeWithRetry`.
- Calls existing `computeConsensus`.
- Calls `recordRun` (writes `model_probe_runs`).
- Calls `sw.WriteState` (writes `model_probe_state` + Redis + cmb transitions).

### 4.3 StateWriter (shared)

```go
type StateWriter struct {
    db    *pgxpool.Pool
    redis *redis.Client
    ttl   time.Duration  // default 4h, env: LLM_GATEWAY_PROBE_REDIS_TTL
}

type StateUpdate struct {
    State                string
    ConsecutiveSuccesses int
    ConsecutiveFailures  int
    LastAttemptAt        time.Time
    NextRetryAt          time.Time
    LastStateChangeAt    time.Time
    LastStatus           string
    TotalAttempts        int
    TransitionToBroken   bool  // if true: also UPDATE cmb.available=FALSE
    TransitionToHealthy  bool  // if true: also UPDATE cmb.available=TRUE
    Trigger              string // "credential_probe" | "model_recovery" | ...
}

func (w *StateWriter) WriteState(ctx context.Context, t probeTarget, u StateUpdate) error
```

**Atomicity**: each step is its own SQL/Redis call. We do NOT wrap in a DB
transaction because:
- The 3 DB writes are idempotent and target different tables.
- The Redis write is independent and best-effort.
- A partial failure (e.g. Redis down) doesn't corrupt DB state; the next
  scheduler cycle will re-write Redis from the DB.

### 4.4 HealthReader (consumed by routing layer)

```go
type HealthReader struct {
    db    *pgxpool.Pool
    redis *redis.Client
}

type HealthSnapshot struct {
    State                string
    ConsecutiveSuccesses int
    ConsecutiveFailures  int
    LastAttemptAt        time.Time
    LoadedFromCache      bool
}

func (r *HealthReader) ReadState(ctx, credID, model) (*HealthSnapshot, error)
func (r *HealthReader) MaybeExitSuspicious(ctx, credID, model) error  // fire-and-forget state flip
```

Read order:
1. Redis `HGETALL llmgw:avail:{cred}:{model}` — if hit and not expired, return.
2. DB `SELECT FROM model_probe_state WHERE credential_id=$1 AND raw_model_name=$2`.
3. If found, write-through to Redis with TTL 4h.
4. If not found in DB either, return `nil` (binding has never been probed).

`MaybeExitSuspicious`:
- Read state.
- If `state != "suspicious"` → no-op.
- Otherwise, fire goroutine with `context.WithTimeout(context.Background(), 1*time.Second)` that:
  - UPDATEs `model_probe_state SET state='recovering', next_retry_at=NOW()+INTERVAL '30 seconds', consecutive_successes=0, consecutive_failures=0, total_attempts=total_attempts+1, last_state_change_at=NOW()`.
  - HSETs Redis state field to `recovering`.
  - Failures are logged at WARN; never returned to the caller.

## 5. State Machine (preserved from v1)

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

| state               | cmb.available | routable | Redis TTL behaviour |
|---------------------|---------------|----------|---------------------|
| `unknown`           | TRUE          | yes      | 4h, refreshed on write |
| `recovering`        | depends       | partial  | 4h, refreshed on write |
| `healthy_confirmed` | TRUE          | yes      | 4h, refreshed on write |
| `suspicious`        | TRUE          | yes      | 4h, refreshed on write (set by DecayScheduler) |
| `broken_confirmed`  | FALSE         | no       | 4h, refreshed on write |

## 6. Redis Schema

```redis
# Per (credential, model) — single most fine-grained key
HSET llmgw:avail:{credential_id}:{raw_model_name} \
    state                  "healthy_confirmed" \
    consecutive_successes  "3" \
    consecutive_failures   "0" \
    last_attempt_at        "1748736000000" \
    last_state_change_at   "1748736000000" \
    last_status            "ok" \
    total_attempts         "42" \
    trigger                "credential_probe"
EXPIRE llmgw:avail:42:minimax-m3 14400  # 4h
```

Fields:
- `state` (string): one of 5 values above
- `consecutive_successes` (int as string): 0-3
- `consecutive_failures` (int as string): 0-3
- `last_attempt_at` (int as string): unix milliseconds
- `last_state_change_at` (int as string): unix milliseconds
- `last_status` (string): "ok" | "http_4xx" | "http_5xx" | "network" | "auth" | "skipped" | "rate_limit" | "quota_exhausted"
- `total_attempts` (int as string): cumulative count
- `trigger` (string): which scheduler wrote this row

**TTL**: 4h default. Refreshed on every write. Worst-case: DecayScheduler
runs every 60s, so the gap between last probe and decay is at most ~60s +
probe RTT. Even if Redis loses a write, the entry survives 4h, well past
the 2h decay window.

**Failure handling**:
- Redis miss: read from DB, write-through to Redis.
- Redis write fails: log WARN, leave DB authoritative. Next cycle retries.
- Redis dead: routing falls back to DB for every read (slower but correct).
- DB dead: orchestrator logs error, skips cycle. Health remains stale but
  routing keeps working from `v_routable_credential_models` (cmb.available).

## 7. Configuration (per-scheduler env vars)

| Env | Default | Component | Purpose |
|-----|---------|-----------|---------|
| `LLM_GATEWAY_PROBE_ENABLE` | `true` | Orchestrator | Master kill-switch |
| `LLM_GATEWAY_PROBE_PER_CRED_CONCURRENCY` | `2` | ProbeExecutor | Per-credential cap (shared) |
| `LLM_GATEWAY_PROBE_REDIS_TTL` | `4h` | StateWriter | Cache TTL |
| `LLM_GATEWAY_PROBE_INTERVAL_CREDENTIAL` | `60min` | CredentialProbeScheduler | Replaces CredentialProbeV2 ticker |
| `LLM_GATEWAY_PROBE_INTERVAL_MODEL_RECOVERY` | `10min` | ModelRecoveryScheduler | Replaces ModelProbeRunner.cycle |
| `LLM_GATEWAY_PROBE_INTERVAL_FEATURED` | `30min` | FeaturedScheduler | Replaces featuredCycle |
| `LLM_GATEWAY_PROBE_INTERVAL_SUSPICIOUS_PING` | `30s` | SuspiciousPingScheduler | NEW (v1) |
| `LLM_GATEWAY_PROBE_INTERVAL_DECAY` | `60s` | DecayScheduler | NEW (v1) |
| `LLM_GATEWAY_PROBE_INTERVAL_PASSIVE_OBSERVER` | `30s` | PassiveObserver | Replaces PassiveProbeListener |
| `LLM_GATEWAY_PROBE_INTERVAL_RECOVER` | `60s` | RecoverScheduler | Replaces HealthAutoRecover + CredentialRecovery |
| `LLM_GATEWAY_SUSPICIOUS_AFTER_SECONDS` | `7200` | DecayScheduler | Threshold for healthy→suspicious |
| `LLM_GATEWAY_SUSPICIOUS_MAX_BATCH` | `50` | SuspiciousPingScheduler | Max rows per cycle |
| `LLM_GATEWAY_REDIS_URL` | (existing) | StateWriter + HealthReader | Standard redis URL env |

Each scheduler also gets an explicit `LLM_GATEWAY_PROBE_ENABLE_<NAME>` env
(e.g. `LLM_GATEWAY_PROBE_ENABLE_SUSPICIOUS_PING`) for fine-grained disable.

## 8. Scheduler Specifications

### 8.1 CredentialProbeScheduler (was `CredentialProbeV2`)

- **Interval**: `LLM_GATEWAY_PROBE_INTERVAL_CREDENTIAL` (60min)
- **PickTargets**: every active credential × its `default_probe_model`
- **Action**: full credential verification (auth + URL + chat "hi" + balance)
- **HTTP mode**: `probeWithRetry(..., ProbeModeChatPing)` then balance probe

### 8.2 ModelRecoveryScheduler (was `ModelProbeRunner.cycle`)

- **Interval**: `LLM_GATEWAY_PROBE_INTERVAL_MODEL_RECOVERY` (10min)
- **PickTargets**: bindings with `state IN ('recovering', 'unknown') AND
  next_retry_at <= NOW()` and NOT `broken_confirmed`, ordered by urgency
- **Action**: consensus-based probe; updates `consecutive_successes`/`failures`
- **HTTP mode**: `probeWithRetry(..., ProbeModeModelsList)` (free tier)

### 8.3 FeaturedScheduler (was `featuredCycle`)

- **Interval**: `LLM_GATEWAY_PROBE_INTERVAL_FEATURED` (30min)
- **PickTargets**: bindings whose model appears in `routing_policy.featured_models`
- **Action**: deep probe; same state machine as ModelRecoveryScheduler
- **HTTP mode**: `probeWithRetry(..., ProbeModeChatPing)` (chat ping for accuracy)

### 8.4 SuspiciousPingScheduler (NEW v1)

- **Interval**: `LLM_GATEWAY_PROBE_INTERVAL_SUSPICIOUS_PING` (30s)
- **PickTargets**: `state='suspicious' AND next_retry_at <= NOW()`, oldest first
- **Action**: ping + consensus; transitions to `healthy_confirmed`,
  `broken_confirmed`, or back to `recovering`
- **HTTP mode**: `probeWithRetry(..., ProbeModeChatPing)` (chat ping)
- **Shared semaphore**: yes (inherited from ProbeExecutor)

### 8.5 DecayScheduler (NEW v1)

- **Interval**: `LLM_GATEWAY_PROBE_INTERVAL_DECAY` (60s)
- **PickTargets**: `state='healthy_confirmed' AND
  last_attempt_at < NOW() - make_interval(secs => $suspicious_after_seconds)`
- **Action**: pure state transition to `suspicious`. No HTTP. No consensus update.
  Resets `next_retry_at = NOW() + INTERVAL '30 seconds'` so the
  SuspiciousPingScheduler picks it up on the next tick.
- **Trigger**: `"decay"` (recorded in Redis and `model_probe_runs`)

### 8.6 PassiveObserver (was `PassiveProbeListener`)

- **Interval**: `LLM_GATEWAY_PROBE_INTERVAL_PASSIVE_OBSERVER` (30s)
- **PickTargets**: derived from `candidate_failure_logs` (recent spikes)
- **Action**: pulls `next_retry_at` forward via `model_probe_passive_boost`,
  promotes to `recovering` if persistent
- **HTTP mode**: no direct probe (the production traffic is the probe)

### 8.7 RecoverScheduler (was `HealthAutoRecover` + `CredentialRecovery`)

- **Interval**: `LLM_GATEWAY_PROBE_INTERVAL_RECOVER` (60s)
- **Action**: pure SQL hygiene
  - Recover expired `cmb.unavailable_recover_at` rows
  - Reset stale `credentials.health_status`
  - Recover expired `availability_recover_at`
  - Reset stale `consecutive_failures`
  - Close expired circuit breakers
- **HTTP mode**: none
- **Redis write**: not needed (recovery only touches `cmb` / `credentials`,
  not `model_probe_state`)

## 9. Concurrent Control

### 9.1 Per-credential semaphore (shared)

```go
// internal/probe/concurrency.go
type PerCredentialSemaphore struct {
    mu  sync.Mutex
    sem map[int64]*semaphore.Weighted  // capacity 2
}

func (p *PerCredentialSemaphore) Acquire(ctx, credID) error {
    s := p.getOrCreate(credID)  // lazy init
    return s.Acquire(ctx, 1)
}
```

The semaphore is held for the duration of ONE probe (HTTP RTT + DB writes).
For a typical probe taking ~500ms-15s, a high-cardinality credential can
have at most 2 in-flight at any time, regardless of how many schedulers
race to probe it.

### 9.2 Cross-scheduler contention

If `CredentialProbeScheduler` and `SuspiciousPingScheduler` both pick the
same (cred, model) target on the same tick, the second one's
`sem.Acquire` will block until the first completes. No additional locking
needed — the existing per-cred semaphore is the single point of coordination.

## 10. Observability

### 10.1 Structured logging

Every scheduler logs once per cycle:
```
slog.Info("probe_orchestrator: cycle complete",
    "scheduler", "credential_probe",
    "picked", 42,
    "pinged", 38,
    "recovered_to_healthy", 30,
    "recovered_to_broken", 0,
    "kept_in_state", 8,
    "errors", 4,
    "concurrent_peak", 2,
    "duration_ms", 4521,
)
```

### 10.2 `model_probe_runs` (existing)

Already captures all probe attempts. The `triggered_by` column is updated
to include:
- `"credential_probe"` (was `"scheduler"`)
- `"model_recovery"` (was `"scheduler"`)
- `"suspicious_pinger"` (NEW v1)
- `"featured_probe"` (was `"scheduler"`)
- `"passive_observer"` (NEW — for state transitions, no actual HTTP)
- `"call_exit"` (NEW v1 — for routing-hook state flips, no HTTP)
- `"decay"` (NEW v1 — for state-only transitions, no HTTP)

### 10.3 Admin API (3 new endpoints in `admin/handlers_probe.go`)

| Method | Path | Purpose |
|--------|------|---------|
| `GET`  | `/api/admin/probe/state-summary?since=2h` | Counts by state, suspicious highlighted |
| `GET`  | `/api/admin/probe/credential/{id}/timeline?since=24h` | Merged view of state rows + probe_runs |
| `GET`  | `/api/admin/probe/concurrency?credential_id=X` | Current in-flight count for the cred |

## 11. Migration Plan

### 11.1 Schema

**No new tables or columns.** `model_probe_state.state` is `TEXT` with no
CHECK constraint (verified). Adding the value `suspicious` is code-only.
`model_probe_runs.triggered_by` is also `TEXT` with no CHECK — extending
the value set is safe.

### 11.2 Code changes

| File | Change |
|------|--------|
| `internal/probe/concurrency.go` | NEW — `PerCredentialSemaphore` |
| `internal/probe/concurrency_test.go` | NEW |
| `internal/probe/executor.go` | NEW — `ProbeExecutor` (HTTP+semaphore) |
| `internal/probe/executor_test.go` | NEW |
| `internal/probe/state_writer.go` | NEW — `StateWriter` (DB+Redis) |
| `internal/probe/state_writer_test.go` | NEW |
| `internal/probe/health_reader.go` | NEW — `HealthReader` (Redis+DB read, call-exit hook) |
| `internal/probe/health_reader_test.go` | NEW |
| `bg/probe_orchestrator.go` | NEW — `ProbeOrchestrator` (lifecycle, ticker loop) |
| `bg/probe_orchestrator_test.go` | NEW |
| `bg/schedulers.go` | NEW — 7 scheduler implementations |
| `bg/schedulers_test.go` | NEW — unit tests per scheduler |
| `bg/probe_http.go` | UNCHANGED — `probeWithRetry` reused by ProbeExecutor |
| `bg/probe_http_test.go` | UNCHANGED |
| `bg/credential_probe_v2.go` | **DELETE** — logic moved to CredentialProbeScheduler |
| `bg/credential_probe_v2_test.go` | **DELETE** — tests moved to schedulers_test.go |
| `bg/model_probe.go` | TRIM — keep `computeConsensus` + `recordRun` +
  `applyResult` helpers (used by ProbeExecutor). Delete cycle/featuredCycle.
| `bg/model_probe_test.go` | TRIM |
| `bg/passive_probe_listener.go` | **DELETE** — logic moved to PassiveObserver |
| `bg/passive_probe_listener_test.go` | **DELETE** |
| `bg/credential_recovery.go` | **DELETE** — logic moved to RecoverScheduler |
| `bg/health_auto_recover.go` | **DELETE** — logic moved to RecoverScheduler |
| `bg/credential_recovery_test.go` | **DELETE** |
| `bg/health_auto_recover_test.go` | **DELETE** |
| `cmd/gateway/main.go` | MODIFY — replace 5 worker.Start() calls with
  `probeOrchestrator.Start(ctx)` |
| `provider/client.go` | MODIFY — add `MaybeExitSuspicious` call after
  candidate resolution |
| `admin/handlers_probe.go` | MODIFY — 3 new admin endpoints |
| `internal/probeutil/endpoint_id.go` | UNCHANGED |

### 11.3 Deployment

1. Deploy with `LLM_GATEWAY_PROBE_ENABLE=false` → boot with workers disabled
   (orchestrator exists but all schedulers return IsEnabled()=false).
2. Verify boot is clean, no missing-import errors from the deletions.
3. Flip `LLM_GATEWAY_PROBE_ENABLE=true`. Orchestrator takes over from
   the 5 old workers (also flipped off via env).
4. Monitor `slog` for `probe_orchestrator: cycle complete` lines from
   each of the 7 schedulers.
5. Roll back by toggling `LLM_GATEWAY_PROBE_ENABLE=false`; no schema
   rollback needed.

## 12. Testing Strategy

### 12.1 Unit tests

| Test | Asserts |
|------|---------|
| `TestPerCredentialSemaphore_Blocks3rd` | 3rd goroutine blocks until 1st releases |
| `TestPerCredentialSemaphore_DifferentCredsIndependent` | 5 different creds, all probe in parallel |
| `TestPerCredentialSemaphore_Release` | After Release, next Acquire succeeds |
| `TestProbeExecutor_AcquiresBeforeHTTP` | sem.Acquire called before httpClient.Do |
| `TestProbeExecutor_ReleasesOnPanic` | defer Release runs even on panic |
| `TestStateWriter_DBWinsOnRedisFail` | DB writes succeed; Redis failure logs WARN |
| `TestStateWriter_RedisFailureDoesNotRollbackDB` | DB state correct after Redis timeout |
| `TestStateWriter_TransitionToBroken_UpdatesCmb` | `cmb.available=FALSE` written |
| `TestStateWriter_TransitionToHealthy_RestoresCmb` | `cmb.available=TRUE` written |
| `TestStateWriter_TTL4h` | Redis TTL between 3h and 5h after write |
| `TestHealthReader_RedisHit` | Returns Redis value, `LoadedFromCache=true` |
| `TestHealthReader_RedisMissFallbackToDB` | Reads DB, writes back to Redis |
| `TestHealthReader_RedisMissDBMiss` | Returns `nil, nil` |
| `TestHealthReader_MaybeExitSuspicious_OnlyFlipsState` | state=recovering, no HTTP call |
| `TestHealthReader_MaybeExitSuspicious_NoOpIfHealthy` | no UPDATE |
| `TestHealthReader_MaybeExitSuspicious_NoOpIfBroken` | no UPDATE |
| `TestDecayScheduler_HealthyToSuspicious` | `last_attempt_at = NOW() - 3h` → flips |
| `TestDecayScheduler_KeepsRecent` | `last_attempt_at = NOW() - 30m` → no change |
| `TestDecayScheduler_SkipsManualDisabled` | guard works |
| `TestDecayScheduler_SkipsSuspended` | guard works |
| `TestDecayScheduler_RespectsThresholdEnv` | LLM_GATEWAY_SUSPICIOUS_AFTER_SECONDS=60 flips 1h-old row |
| `TestSuspiciousPingScheduler_RespectsConcurrencyCap` | 10 same-cred targets, in-flight ≤ 2 |
| `TestSuspiciousPingScheduler_DifferentCredsUncapped` | 5 different creds, all parallel |
| `TestSuspiciousPingScheduler_SuccessToRecovering` | 1 success → state=recovering, succ=1 |
| `TestSuspiciousPingScheduler_3SuccessToHealthy` | 3 successes → state=healthy_confirmed |
| `TestSuspiciousPingScheduler_3FailureToBroken` | 3 failures → state=broken_confirmed + cmb=FALSE |
| `TestSuspiciousPingScheduler_RecordsRun` | model_probe_runs row with `triggered_by='suspicious_pinger'` |
| `TestModelRecoveryScheduler_PicksRecoveringAndUnknown` | query only those states |
| `TestModelRecoveryScheduler_SkipsBrokenConfirmed` | broken_confirmed excluded |
| `TestCredentialProbeScheduler_PicksAllActiveCreds` | 3 creds → 3 targets |
| `TestCredentialProbeScheduler_SkipsSuspended` | suspended creds excluded |
| `TestFeaturedScheduler_PicksFromRoutingPolicy` | featured_models list respected |
| `TestPassiveObserver_BoostsNextRetry` | candidate_failure_logs pulls next_retry_at forward |
| `TestRecoverScheduler_RecoversExpiredCmb` | unavailable_recover_at < now() → restored |
| `TestRecoverScheduler_ResetsStaleHealthStatus` | > 2min stale → reset to 'unknown' |
| `TestProbeOrchestrator_StartsAll7Schedulers` | 7 tickers spawned on Start |
| `TestProbeOrchestrator_StopsCleanly` | all tickers cancelled on Stop |

### 12.2 Integration test

`scripts/e2e-probe-orchestrator.sh`:
1. Start local mock upstream that returns 200 on `/v1/chat/completions`
2. Insert 3 (cred, model) bindings with mixed states:
   - one `healthy_confirmed`, last_attempt_at = NOW() - 3h (should decay)
   - one `suspicious`, next_retry_at = NOW() (should be picked up)
   - one `recovering` with consecutive_failures=2 (one more fail → broken)
3. Run orchestrator for 5 minutes with shortened intervals
4. Assert:
   - `model_probe_state` reflects expected transitions
   - Redis HGETALL for all 3 bindings returns the new states
   - `model_probe_runs` has rows from `suspicious_pinger`, `decay`, `model_recovery` triggers
   - `cmb.available` matches `state` (`broken_confirmed` → FALSE)

### 12.3 Regression tests

All existing tests in `credential_probe_v2_test.go`,
`model_probe_test.go`, `passive_probe_listener_test.go`,
`credential_recovery_test.go`, `health_auto_recover_test.go` are moved
to `schedulers_test.go` and adapted to the new Scheduler interface.
**No test logic is dropped** — this is a structural move, not a coverage
change.

## 13. Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| 5-worker deletion breaks untested code paths | Migration preserves all SQL strings and consensus rules verbatim. Only structural changes. |
| Per-cred semaphore held during long HTTP probe | 15s timeout on HTTP client; if exceeded, Release runs in defer |
| Redis flap causes thrash | StateWriter logs WARN on Redis failure; next cycle retries from DB. Routing falls back to DB. |
| Orchestrator dies mid-cycle | ctx cancellation propagates to all schedulers + executor; defer Release runs |
| DecayScheduler races with RecoverScheduler | RecoverScheduler runs SQL hygiene only; DecayScheduler only writes state. No overlap on same row. |
| HealthReader MaybeExitSuspicious adds DB write to hot path | 1s timeout, fire-and-forget goroutine, never blocks request |
| 7 schedulers all hit DB on same tick | Each has different interval (30s to 60min); DB load is bounded by MaxBatch per scheduler |
| Backward compat with `bg/model_probe.go` callers | `computeConsensus`, `recordRun`, `applyResult` keep their signatures; orchestrator uses them via the existing `probeTarget` type |

## 14. Open Questions

None. All design decisions resolved in the brainstorming phase (chat history).
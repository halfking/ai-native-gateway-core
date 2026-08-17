# Connection-pool self-check & live swim-lane reset hook

> **Status**: draft (2026-07-20) — design review pending
> **Author**: LLM Gateway team
> **Trigger**: operator note: "定时检查从网关出去到不同供应商的 tcp 连接数多少，与当前的请求数进行对比，确认是否需要重置这个供应商的连接池。防止连接泄漏或未及时回收的情况。"
> **Sibling docs**: `2026-07-20-latency-aware-routing.md` (routing/selection)
> — `2026-07-20-pre-request-validation-hook.md` (request-time guards)

## 1. Background

`net/http.Transport` keeps an idle-connection pool per host. For the LLM
gateway this pool has two quirks:

1. **Per upstream, mixed responsibility.** A single `http.Transport` is
   reused across every credential that hits `api.minimaxi.com`. When one
   credential misbehaves (e.g. a credential whose key is rotated but whose
   old idle connections remain pointed at a now-revoked origin), the pool
   retains stale sockets that no request path can drain.
2. **No "leak" visibility.** Go's stdlib gives us
   `Transport.CloseIdleConnections()` but no way to ask "how many are open,
   how old, how idle?" operators have to inspect `netstat` outputs and
   guess.

Twice in the last month an upstream LDAP-style auth token was rotated
without telling gateway, and the gateway kept hammering the *old* token
because:
- the *idle* pool returned a stale socket,
- that socket's first re-use triggered a 401,
- the failure logged as a credential-level error instead of a "TCP pool
  re-use of a stale conn" error.

The symptom surfaces as a "401 spike after key rotation" which the latency-
aware router cannot diagnose (the latency is fine, the upstream returns
401 in tens of milliseconds, the diff is that those requests **were already
authenticated minutes ago**).

This feature is the missing canary: it watches **transport-level** state
and **proposes** a pool reset when the count of currently-open outbound
sockets no longer matches the live workload.

### 1.1 Goals

- **Periodic self-check** (default every 30s) that compares
  *open outbound TCP connections per (provider_id, credential_id)* against
  *active request count* from the same window.
- **Heuristic** that flags a provider as **leaking** when
  `open_conns > inflight * LEAK_RATIO` AND `open_conns > MIN_LEAK_ABS`,
  AND at least one connection is *idle long enough* to be safely closed.
- **Safe-close criteria**: never close a connection that is *in-flight*, less
  than `MIN_CONN_AGE_SEC` old, or has been *idle less than `MIN_CONN_IDLE_SEC`*.
  These three rules guarantee a reset cannot desynchronise a healthy TCP
  connection mid-stream.
- **Manual reset hook** on every provider swim lane, surfaced in the live
  dashboard next to latency and connection counts.
- **All reset events are auditable** — the SRE pattern "I reset provider X
  at 14:33 because dashboard said so" must be reconstructable from logs +
  the audit table.
- **No automatic silent reset under steady-state.** When the heuristic
  fires the gateway must *announce* (SSE incident, dashboard indicator,
  alertmanager page) and **only auto-close** idle connections that have
  been idle for longer than `AUTO_IDLE_THRESHOLD_SEC` (default 120s).
  Anything less idle requires a human click.

### 1.2 Non-goals

- Per-connection retry/back-off. (Other teams, separate tickets.)
- A general HTTP transport rewrite. We **observe** existing `*http.Transport`
  and **call** `CloseIdleConnections()` on it; we do not change its
  semantics.
- Replacing the live-stream dashboard. This feature **adds** columns and a
  reset button on top of the existing swim lane component.

## 2. Concepts

### 2.1 Connection record

The gateway instruments **every outbound dial** by wrapping
`http.Transport.DialContext`. Every TCP connection is represented by:

```go
type connRecord struct {
    ProviderID     int           // credentials.provider_id (== destination)
    CredentialID   int           // credentials.id              (== app-side id)
    RemoteAddr     string        // "host:port" of the upstream
    EstablishedAt  time.Time     // when DialContext returned nil err
    FirstUsedAt    time.Time     // first time the conn served a request
    LastUsedAt     time.Time     // last byte through the conn
    LastInFlightAt time.Time     // when RoundTrip last started on this conn
    Active         bool          // true while a request is mid-roundtrip
    BytesInOut     int64         // for debug display only
    // Synthetic fields (computed at query time):
    AgeSeconds           int    // now - EstablishedAt
    IdleSeconds          int    // now - LastUsedAt  (only when !Active)
    ActiveSeconds        int    // now - LastInFlightAt (only when Active)
}
```

Connections are kept in a per-(provider, credential) slice inside a single
`shardedConnMap`:

```go
type shardedConnMap struct {
    n         int                // 64 shards, hash by provider_id
    shards    [64]sync.RWMutex
    records   [][]connRecord     // per-shard slice (small + grows linearly)
    sizeHint  int                // for capacity reservations
}
```

Sharding by provider_id (not credential_id) is intentional: providers have
many credentials and we want the **provider-level** count (what the dashboard
asks for) to be O(1), while per-credential lists are still O(creds).

### 2.2 Self-check thresholds

The self-check **does not stream every connection**. It summarises. Four
counters per (provider, credential):

| metric | definition | unit |
|---|---|---|
| `open_count` | total live conn records | int |
| `active_count` | records with `Active=true` | int |
| `idle_count` | `open_count − active_count` | int |
| `oldest_age_sec` | `max(now − EstablishedAt)` | int |
| `oldest_idle_sec` | `max(now − LastUsedAt)` over the *idle* subset | int |
| `inflight_now` | active gateway requests currently using this cred (from limiter) | int |

From those the **leak ratio** is:

```
leak_ratio     = (open_count − inflight_now) / max(inflight_now, 1)
leaked?        = leak_ratio > LEAK_RATIO
              AND open_count     > MIN_LEAK_ABS
              AND oldest_idle_sec > AUTO_IDLE_THRESHOLD_SEC
sealed?        = (provider is in cooldown for next LEAK_COOLDOWN_SEC after last reset)
```

`leaked?` and `sealed?` are evaluated at dashboard render time so the user
always sees *current* numbers; the same predicate drives the auto-close path.

### 2.3 What "leak" means here

A leak in this context is **strictly** defined as:

> The connection pool is holding connections that are *not* serving a request
> AND have been idle longer than the auto-close threshold.

A connection that:
- is currently serving a request (`Active=true`), or
- was created in the last 5s (TLS handshake may still be in flight), or
- has been used in the last 30s (might be picked up by the next request)

is **not** a leak by this definition, regardless of how many other
connections exist. The dashboard reflects this distinction: only
`oldest_idle_sec > 60s` candidates show a `[reset]` button.

## 3. Architecture

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  gateway process                                                            │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────┐    │
│  │ upstream/client.go                                                  │    │
│  │   newClient()  wraps http.Transport.DialContext                     │    │
│  │                  → on dial: track_record{EstablishedAt: now}        │    │
│  │                  → on conn close: remove record                     │    │
│  │                  → on GetConn (idle reuse): LastUsedAt = now       │    │
│  │                  → on RoundTrip:  Active=true; LastInFlightAt=now │    │
│  │                                Active=false after body close        │    │
│  └────────────────────────────────────────────────────────────────────┘    │
│                          ▲                                                   │
│                          │ http.Transport                                    │
│                          ▼                                                   │
│  ┌────────────────────────────────────────────────────────────────────┐    │
│  │ internal/liveconn/tracker.go  ← NEW                                │    │
│  │   • connRecord store (per-provider, per-credential)                │    │
│  │   • selfcheck.Run(ctx, period=30s):                                │    │
│  │       - sweep stale records (> MAX_CONN_AGE_HOURS, default 6h)    │    │
│  │       - summarise counts into rolling histograms (60s / 5m / 1h)  │    │
│  │       - feed ProviderConnEvent into live-stream SSE               │    │
│  │   • ResetProviderConnPool(credID, mode):                          │    │
│  │       - safe-close pass: close all conns.idle > MIN_IDLE_SEC;     │    │
│  │                            conns.age < MIN_AGE_SEC skipped;       │    │
│  │                            conns.Active skipped;                    │    │
│  │       - audit log row (provider_id, mode, before, after)           │    │
│  │       - close all idle from underlying http.Transport              │    │
│  └────────────────────────────────────────────────────────────────────┘    │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
                ▲                                            ▲
                │ SSE                                        │ admin REST
                │ (live-stream)                             │ (reset trigger)
                ▼                                            ▼
       ┌────────────────────┐                    ┌──────────────────────┐
       │ SwimLane.vue        │                    │ admin/connpool_*.go   │
       │  ▸ latency badge    │ ◄──SSE updates── │  ▸ GET /conn-pool/    │
       │  ▸ conn-count badge  │                    │     stats             │
       │  ▸ [↻ reset] button  │ ◄──POST click──── │  ▸ POST /conn-pool/   │
       └────────────────────┘                    │     reset             │
                                                   └──────────────────────┘
```

### 3.1 Connection tracker placement

`internal/liveconn/tracker.go` lives in a new package to keep the rest of
the codebase unaware of the bookkeeping. The package exposes:

```go
// internal/liveconn/tracker.go
package liveconn

type Tracker interface {
    OnDial(ctx, providerID, credentialID, remoteAddr string) ConnHandle
    OnGetConn(handle ConnHandle)                          // idle reuse
    OnRoundTripStart(handle ConnHandle)                   // Active=true
    OnRoundTripEnd(handle ConnHandle, bytesInOut int64)   // Active=false; LastUsedAt
    OnClose(handle ConnHandle)                            // remove
    Summary(providerID int) []ProviderConnSummary         // for SSE + admin
    Reset(providerID, credentialID int, mode ResetMode) (ResetReport, error)
    Stop()                                                // drain on shutdown
}

type ResetMode int
const (
    ResetAuto       ResetMode = iota  // close only conns.idle > AUTO_IDLE_SEC
    ResetManual                        // close all idle > MIN_IDLE_SEC (SRE click)
)
```

### 3.2 Self-check loop

`tracker.Summary()` is the only function the dashboard needs to call.
A separate `tracker.SelfCheckLoop()` runs in its own goroutine:

```
every 30s:
    for provider, credential in lru(seen_providers):
        s := Summary(credential)
        if s.Leaked:
            emit ProviderConnEvent{provider, leake=true, p99_idle=...}
            if AutoResetArmed:     # set by env
                Reset(provider, credential, ResetAuto)
                emit ProviderConnResetEvent{provider, count=..., before=..., after=...}
    evict records age > MAX_CONN_AGE_HOURS (default 6h, prevents unbounded growth)
```

The SelfCheckLoop is the **only** path that emits `ProviderConnEvent`; the
admin reset path emits `ProviderConnResetEvent`.

### 3.3 Safe-close criteria

The reset path uses the connector's own state. We can't peek into `*http.Conn`
but the wrapper gives us the metadata we need:

```go
func (t *tracker) canSafelyClose(r connRecord, now time.Time) bool {
    if r.Active { return false }                       // in-flight: never
    if now.Sub(r.EstablishedAt) < MIN_AGE_SEC { return false }   // handshake?
    if now.Sub(r.LastUsedAt) < MIN_IDLE_SEC { return false }     // hot warm pool
    return true
}
```

After the safe-close pass, we additionally call
`http.Transport.CloseIdleConnections()` so Go's pool confirms our intent.
This is the same level of safety as `Client.CloseIdleConnections()`.

If the safe-close pass yields 0 closes, the admin endpoint reports
`reset_effective: 0` and the SSE event shows "no closed-at-safe conns".
Manual mode does NOT escalate to force-close — that would be a separate
`ResetForce` mode with its own SRE runbook (out of scope for v1).

### 3.4 Reset action surface

Two paths converge on `Tracker.Reset`:

| trigger | source | mode | cooldown |
|---|---|---|---|
| Auto-reset from self-check | background loop, threshold met | `ResetAuto` | `LEAK_COOLDOWN_SEC` (default 300s) |
| Manual SRE click | dashboard `[↻ reset]` button | `ResetManual` | none per-mode (a human can always retry) |

Both paths run the same safe-close algorithm. Both write audit rows.

## 4. Data model

### 4.1 In-memory only

The conn-record store lives in the gateway process. Persisting *every*
TCP connection to the DB would dwarf request traffic; we only persist
**summaries and events**:

```sql
-- Migration: 2026-07-20-002-conn-pool-selfcheck.sql
SET search_path = public;

CREATE TABLE IF NOT EXISTS conn_pool_reset_log (
    id              bigserial PRIMARY KEY,
    ts              timestamptz NOT NULL DEFAULT now(),
    provider_id     integer NOT NULL,
    credential_id   integer NOT NULL,
    mode            text NOT NULL,                            -- 'auto' | 'manual'
    triggered_by    text NOT NULL,                            -- 'self_check' | 'admin' | username
    snapshot_open   integer NOT NULL,                        -- open_count before
    snapshot_active integer NOT NULL,                        -- active_count before
    snapshot_idle   integer NOT NULL,                        -- idle_count before
    closed_count    integer NOT NULL,                        -- how many actually closed
    skipped_young   integer NOT NULL,                        -- age<MIN_AGE_SEC
    skipped_active  integer NOT NULL,                        -- Active=true
    skipped_hot     integer NOT NULL,                        -- idle<MIN_IDLE_SEC
    closed_durations_ms integer[],                          -- how long each closed conn lived
    closed_idle_ms  integer[],                              -- how long each was idle
    related_request text,                                    -- optional admin action id
    notes           text
);

CREATE INDEX IF NOT EXISTS idx_conn_pool_reset_log_provider_ts
    ON conn_pool_reset_log (provider_id, ts DESC);

ALTER TABLE provider_models
    ADD COLUMN IF NOT EXISTS last_conn_pool_reset_ts         timestamptz,
    ADD COLUMN IF NOT EXISTS last_conn_pool_reset_mode      text;     -- 'auto' | 'manual'
```

A future audit query: "did the auto-reset ever help?" becomes
`SELECT avg(closed_count) WHERE mode='auto' AND ts > now()-7d`.

### 4.2 SSE event types

Live-stream SSE carries three new event types:

```ts
type ConnPoolEventType =
  | "conn_pool_summary"        // every 30s (low rate), per provider
  | "conn_pool_leaked"          // when leak_ratio crosses threshold (incident)
  | "conn_pool_reset"           // after a reset completes (any source)

interface ConnPoolSummary {
  ts:                 string
  provider_id:        number
  provider_code:      string                       // 'minimax' | 'nvidia' | ...
  open_count:         number
  active_count:       number
  idle_count:         number
  inflight_now:       number                       // from limiter
  leak_ratio:         number                       // (open - inflight) / max(inflight,1)
  oldest_age_sec:     number
  oldest_idle_sec:    number
  leaked:             boolean
}

interface ConnPoolResetEvent {
  ts:                 string
  provider_id:        number
  credential_id:      number
  mode:               'auto' | 'manual'
  triggered_by:       string
  snapshot_open:      number
  closed_count:       number
}
```

The LiveStream SSE handler picks these up under the existing
`/api/admin/live-stream` channel — no protocol change needed; payloads
just contain new fields.

## 5. Configuration

```
LLM_GATEWAY_CONNPOOL_SELFCHECK_PERIOD_SEC=30   # how often to scan
LLM_GATEWAY_CONNPOOL_MIN_AGE_SEC=5            # safe-close age minimum
LLM_GATEWAY_CONNPOOL_MIN_IDLE_SEC=30          # safe-close idle minimum
LLM_GATEWAY_CONNPOOL_MAX_AGE_HOURS=6          # evict records older than this
LLM_GATEWAY_CONNPOOL_LEAK_RATIO=2.0            # (open - inflight) / inflight > 2.0 = leaked
LLM_GATEWAY_CONNPOOL_MIN_LEAK_ABS=50          # ignore providers with open<50
LLM_GATEWAY_CONNPOOL_AUTO_IDLE_SEC=120        # auto-close only when idle > this
LLM_GATEWAY_CONNPOOL_LEAK_COOLDOWN_SEC=300    # suppress repeat auto-reset on same provider
LLM_GATEWAY_CONNPOOL_AUTO_RESET_ENABLED=false  # safe-default: manual only
LLM_GATEWAY_CONNPOOL_SSE_BATCH_MS=500         # SSE merge window for fast bursts
```

`AUTO_RESET_ENABLED=false` is the **safe default** — operators opt in after
they see the dashboard reflect reality. Until then, `leak_ratio` still
shows up on the swim lane and the manual button is always available.

## 6. Admin API

Three endpoints registered alongside the existing live-stream admin surface:

| method | path | purpose |
|---|---|---|
| `GET`    | `/api/admin/conn-pool/summary`             | full snapshot of every provider's pool summary |
| `GET`    | `/api/admin/conn-pool/reset-log?since=…`  | audit log listing (most recent N) |
| `POST`   | `/api/admin/conn-pool/reset/{provider_id}` | manual SRE trigger; body `{credential_id?: int, force?: bool}` |

Body contract for the reset endpoint:

```json
{
  "credential_id": 23,            // optional; default = all credentials of the provider
  "force": false,                  // true ⇒ even young/idle conns are closed (DANGER)
  "reason": "dashboard action"    // free-form text written to audit log
}
```

Response:

```json
{
  "provider_id": 18,
  "credential_id": 23,
  "mode": "manual",
  "triggered_by": "sre:halfking",
  "snapshot_open": 142,
  "snapshot_active": 6,
  "snapshot_idle": 136,
  "closed_count": 124,
  "skipped": {
    "young": 0,
    "active": 6,
    "hot": 10          // recently used idle conns that we left alone
  },
  "audit_log_id": 8842117
}
```

`force: true` is **only** available to admin role and emits
`mode='manual_force'` in the audit log + a separate alertmanager page.
v1 feature flag keeps it off by default.

## 7. Frontend wiring

### 7.1 Swim-lane layout change

Current `SwimLane.vue` `swim-lane__stats` area:

```
✓{success}  ✗{failure}  [诊断]
```

New layout (per operator requirement "在...泳道最左侧显示延迟与连接数的数据"):

```
[p99 latency] [open conns / N in-flight] [✓{success} ✗{failure}] [↻ reset] [诊断]
```

CSS reference (Tailwind / scoped `<style>`):

```
.swim-lane__stats {
  display: flex; gap: 6px; font-variant-numeric: tabular-nums;
  flex-wrap: wrap; align-items: center;
}
.swim-lane__stat-pill {                 /* new: latency/conns pills */
  display: inline-flex; align-items: center;
  gap: 4px; padding: 2px 6px; border-radius: 6px;
  background: var(--kx-bg-elevated);
  font-size: 11px;
}
.swim-lane__connpill--warn   { color: var(--kx-orange-600); border: 1px solid currentColor; }
.swim-lane__connpill--leak   { color: var(--kx-red-600);     border: 1px solid currentColor; animation: pulse 1.5s infinite; }

.swim-lane__reset-btn {
  border: 1px solid var(--kx-blue-600); color: var(--kx-blue-600);
  background: transparent; padding: 2px 8px; border-radius: 6px;
  cursor: pointer; font-size: 11px;
}
.swim-lane__reset-btn:hover { background: var(--kx-blue-50); }
.swim-lane__reset-btn[disabled] {
  cursor: not-allowed; opacity: 0.4;
}
```

### 7.2 Vue component changes

A single utility hook `useConnPool(providerId)` fetches the summary live from
the SSE channel and exposes it:

```ts
// web/src/composables/useConnPool.ts
export function useConnPool(providerId: number) {
  const store = useLiveStreamStore();
  return computed(() => {
    const last = store.lastByKey(`conn_pool:${providerId}`);
    return last as ConnPoolSummary | null;
  });
}

export function canReset(s: ConnPoolSummary): boolean {
  if (!s) return false;
  // Manual reset only ever fires for conns that *can* be safely closed.
  return s.open_count >= 10 && s.oldest_idle_sec >= 30;
}
```

Inside `SwimLane.vue`:

```vue
<script setup lang="ts">
import { useConnPool, canReset } from '../composables/useConnPool'
const props = defineProps<...>();
const emit = defineEmits<{ reset: [providerId: number]; ... }>();

const connPool = computed(() => useConnPool(props.lane.providerId));
const resetDisabled = computed(() =>
  !canReset(connPool.value)
);
async function handleReset() {
  if (resetDisabled.value) return;
  emit('reset', props.lane.providerId);
}
</script>

<template>
  <span class="swim-lane__stat-pill"
        :title="`p99 latency ${connPool?.oldest_idle_sec ?? '—'}ms idle`">
    ⏱ {{ formatMs(props.lane.stats.p99_latency_ms) }}
  </span>
  <span :class="[
    'swim-lane__stat-pill',
    connPool?.leaked && 'swim-lane__connpill--leak',
    !connPool?.leaked && (connPool && connPool.leak_ratio > 1.5) && 'swim-lane__connpill--warn',
  ]"
        :title="connPoolTooltip">
    ⛓ {{ connPool?.open_count ?? '—' }} / {{ connPool?.inflight_now ?? '—' }}
  </span>
  ...
  <button class="swim-lane__reset-btn" :disabled="resetDisabled"
          @click.stop="handleReset" title="重置连接池（关闭空闲 >=30s 的连接）">
    ↻ reset
  </button>
</template>
```

The parent component (`LiveRequestStream.vue`) handles the click:

```ts
async function handleReset(providerId: number) {
  const ok = await adminApi.post(`/api/admin/conn-pool/reset/${providerId}`, { reason: 'dashboard' });
  if (!ok) snackbar('Reset failed');
}
```

### 7.3 What the dashboard shows in steady state

Normal: open_count ≈ 1.05 × inflight (one warm conn per credential). Pill
shows plain "⛓ N / N", latency pill shows ⏱ `~1500ms`. Reset button enabled
but unobtrusive.

Warning: leak_ratio > 1.5 → pill turns **amber**, button still enabled.
Self-check hasn't fired *leaked?* yet (need ratio > 2.0 AND idle > 120s).

Leaked: leak_ratio > 2.0, open > 50, oldest_idle > 120s. Pill turns **red
with pulse**, button still enabled. If `LLM_GATEWAY_CONNPOOL_AUTO_RESET_ENABLED=true`
the auto-reset already fired and the pill would briefly read "↻ auto-closed 124";
this is observable in audit log + reset-log SSE event.

## 8. Migration & feature flag

| Phase | Duration | What ships |
|---|---|---|
| **P0** | 1 day | migration `2026-07-20-002`. `tracker` package + admin endpoints. **No SSE, no UI.** All conn-pool admin endpoints return `[]`. |
| **P1** | 1 day | SelfcheckLoop populating summary into SSE `conn_pool_summary` every 30s. SwimLane renders pills + button. **Auto-reset is OFF by default.** |
| **P2** | 1 week | After SRE has run with manual reset for a week, flip `LLM_GATEWAY_CONNPOOL_AUTO_RESET_ENABLED=true` on staging. |
| **P3** | 2 weeks | Flip `AUTO_RESET_ENABLED=true` on 245 (pre-prod), then 154 (prod) after another week of metrics confirming resets help. |

`force: true` on the admin endpoint stays off until P4 (or never — separate
debate).

### 8.1 Rollback

If the SSE payload overwhelming the stream under heavy load:

```
LLM_GATEWAY_CONNPOOL_SSE_BATCH_MS=0       # disable batching, emit immediately
                                     # if still too noisy, set to 5000
```

To switch *off* the whole feature:

```
LLM_GATEWAY_CONNPOOL_SELFCHECK_PERIOD_SEC=86400   # scan once per day
```

To roll back the entire feature: drop the SSE handler + leave the admin
endpoints in place (they return empty arrays). Migration rolls back the
table without affecting request flow.

## 9. Testing

### 9.1 Unit tests

| test | proves |
|---|---|
| `tracker_test.go::TestTracker_DialClose` | every dial produces a record, close removes it; counts stay consistent under concurrent dials |
| `tracker_test.go::TestTracker_ActivevsIdle` | RoundTrip pin sets `Active=true`; idle reuse updates `LastUsedAt` without losing `Active` |
| `tracker_test.go::TestTracker_SafeClose_Young` | conn with `age < MIN_AGE_SEC` is **never** closed, even under manual reset |
| `tracker_test.go::TestTracker_SafeClose_Active` | in-flight conn is **never** closed; reducer sees closed_count=0 |
| `tracker_test.go::TestTracker_SafeClose_HotIdle` | conn with `idle < MIN_IDLE_SEC` is **never** closed even when leak_ratio > threshold |
| `tracker_test.go::TestTracker_LeakHeuristic` | `leaked?` fires only when **all three** predicates are true (ratio > LEAK_RATIO AND open > MIN_LEAK_ABS AND oldest_idle > AUTO_IDLE_SEC) |
| `tracker_test.go::TestTracker_LeakHeuristic_TableDriven` | boundary tests on all three predicates (off-by-one) |
| `tracker_test.go::TestTracker_Cooldown` | after a manual reset on provider P, auto-reset on P is suppressed for `LEAK_COOLDOWN_SEC` |
| `tracker_test.go::TestTracker_AuditLogFields` | ResetMode → audit row stores `mode`, `triggered_by`, `snapshot_*`, `closed_count`, `skipped_*` correctly |
| `admin/connpool_test.go::TestReset_ManualScope` | `POST /api/admin/conn-pool/reset/{provider_id}` with `credential_id` only closes that credential; without it all |
| `admin/connpool_test.go::TestReset_ForbiddenPath` | non-admin role gets 403; audit log records the rejected attempt |

### 9.2 Integration / chaos

- **Fake socket leak**: instrument a test that creates N=200 dummy `connRecord`s
  for a credential and verifies leak_ratio == 200/1 == 200 and that auto-reset
  with `MIN_LEAK_ABS=50` actually closes N-1 (skipping the most-recently-used
  record).
- **Concurrency during reset**: 50 goroutines each call `OnRoundTripStart` while
  another goroutine triggers a manual reset. Assert: no record is closed while
  `Active=true`. (Use sync.WaitGroup + atomic counters per record.)
- **Live SSE throughput**: 100 RPS open-close churn; verify no SSE message
  arrives later than 1 s after the corresponding DB row was inserted.
- **False alarm**: with `LLM_GATEWAY_CONNPOOL_LEAK_RATIO=10` and a normal
  workload (open=10, inflight=5), the swim-lane pill must NOT show red and the
  reset button must be disabled.
- **Operator insight regression**: replay the 2026-07-14 auth-rotation event
  where stale auth headers caused 401s. With this feature, within 30s of
  the rotation the dashboard must show `oldest_idle_sec > 60` AND
  `leak_ratio > 2.0` for the affected provider — even if no request has
  re-tried yet.

### 9.3 Frontend tests

- `SwimLane.test.ts::TestLatencyPill_Format` — formats 1234 ms as `1.23s`,
  1500 ms as `1.5s`, 12345 ms as `12.3s`.
- `SwimLane.test.ts::TestConnPill_ColorState` — green / amber / red class
  rules match the documented thresholds.
- `useConnPool.test.ts::TestResetDisabled_Defaults` — manual button
  disabled when `s.leaked=false`.
- `LiveRequestStream.test.ts::TestResetEmitsToast` — happy-path SRE click
  shows toast on success, red banner on failure.

## 10. Risks & trade-offs

| Risk | Likelihood | Mitigation |
|---|---|---|
| **Wrapper leaks** if `RoundTrip` panics and skips defer → record stuck in `Active=true` | medium | every record has a `LastInFlightAt` timestamp; `selfcheck` marks records stale if `Active=true && now − LastInFlightAt > 2× max_idle_sec` → resets to false. Logged as a "stuck-active" incident. |
| **SSE storms** during leak events (every dashboard user polls) | low | `LLM_GATEWAY_CONNPOOL_SSE_BATCH_MS` coalesces emits; alert event only sent once per (provider, leak_start) |
| **Transport.Inner Transport config** (e.g. `MaxIdleConnsPerHost=32`) caps auto-close effectiveness | low | admin endpoint reports `closed_count` and `skipped_hot`; if `skipped_hot > closed_count * 3` the dashboard suggests flipping `MaxIdleConnsPerHost` for that provider |
| **Reset storm during a spike** (provider dies mid-flight, all in-flight conns reopen, idle jumps → auto-reset fires) | medium | auto-reset only closes `idle > AUTO_IDLE_SEC`; verified safe under that constraint. `LEAK_COOLDOWN_SEC` further dampens. |
| **Manual button click on a hot provider** → kills enough sockets that the next batch of requests establishes fresh ones, adding 200-500 ms TLS handshake latency | low–medium | the dashboard shows live "open count closed: N" feedback so SRE sees the impact. Reset endpoint returns `snapshot_open` + `closed_count` so the operator can decide if rollback is needed. |
| **Privacy of credential_id in audit log rows** | none | `conn_pool_reset_log` is admin-only; existing RBAC middleware is reused |
| **Memory growth** if process never evicts old conn records | medium | `MaxConnAgeHours=6` is a hard eviction; periodic sweep removes tombstones; LRU cap on per-shard slices (10K records) |
| **Forcing close of an in-flight TCP** | not in scope | safe-close criteria explicitly forbid; unit + chaos tests assert this with concurrent goroutines |

## 11. Acceptance criteria

- [ ] `/api/admin/conn-pool/summary` returns a row for every provider that the
      gateway has dialed in the past hour (within MIN_LEAK_ABS threshold).
- [ ] Swim-lane pill colour matches the documented thresholds on synthetic data
      (green / amber / red).
- [ ] Manual SRE click on a real-world "leaked" provider closes ≥ 80 % of
      eligible conns within 500 ms (hot-idle & young-age skip window).
- [ ] Safe-close tests assert: zero in-flight connections are closed across
      1k runs of the reset path.
- [ ] During a 1-hour staging soak at 100 RPS, auto-reset events ≥ 0 (default
      `AUTO_RESET_ENABLED=false`) and the audit table grows by exactly the
      number of manual SRE clicks.
- [ ] Replay test for "stale auth header after rotation" surfaces a leaked
      provider within 30 s of the rotation event.
- [ ] Frontend swim lane loads with the new columns without CLS shift (CSS
      verification with `lighthouse` `cumulative-layout-shift < 0.05`).
- [ ] No regression: existing swim-lane diagnostic button still works and the
      new reset button does NOT trigger the diagnose flow on click.

## 12. Open questions

1. **Should we expose `ResetForce`** (closes young conns too)? Tradeoff
   against accidental "I just killed a TLS handshake" damage. Leaning
   *don't ship v1* — manual reset is enough for the leak we're worried
   about, and force-reset would mostly fire during deploy/cert-rollover,
   not during runtime issues.
2. **Per-credential vs per-provider reset UI** — operator note suggests
   "供应商" dimension. We're shipping per-provider; per-credential reset
   is a follow-up. Confirm.
3. **Should the auto-reset cooldown share state across pods?** A single Redis
   key (`connpool:cooldown:{provider_id}`) prevents two pods from racing.
   Recommend yes — keyed on `provider_id`, TTL `LEAK_COOLDOWN_SEC`.
4. **`MaxConnAgeHours`** — 6 h is arbitrary. Should it be `idle > hours_run`
   (don't close a fresh conn ever, only evict records) instead? Recommend
   keep age-based eviction as a record-cleanup pass, separate from
   safe-close (which is purely on `idle`).

---

## Appendix A — self-check algorithm

```
func (s *SelfCheckLoop) tick(ctx context.Context) {
    now := time.Now()
    for providerID, creds := range s.providers.Snapshot() {       // LRU-bounded
        for credID, limit := range creds {
            conns   := s.tracker.SnapshotConns(providerID, credID)
            active  := limit.Used()        // in-flight requests on this cred
            opens   := len(conns)
            idle    := opens - active

            // 1. evict records older than MAX_CONN_AGE_HOURS
            s.tracker.EvictOlderThan(now.Add(-s.cfg.MaxConnAgeHours))

            // 2. compute leak predicate
            ratio      := float64(idle) / math.Max(float64(active), 1.0)
            oldestIdle := s.oldestIdle(conns, now)
            leaked := ratio > s.cfg.LeakRatio
                  && opens > s.cfg.MinLeakAbs
                  && oldestIdle > s.cfg.AutoIdleSec

            // 3. emit summary every tick (always)
            emit ConnPoolSummary{
                provider_id: providerID,
                open_count: opens,
                active_count: active,
                idle_count: idle,
                inflight_now: active,
                leak_ratio: ratio,
                oldest_age_sec: oldestAge(conns, now),
                oldest_idle_sec: oldestIdle,
                leaked: leaked,
            }

            // 4. auto-reset
            if leaked && s.cfg.AutoResetEnabled && !s.cooldownActive(providerID) {
                ResetReport r := s.tracker.Reset(providerID, credID, ResetAuto)
                emit ConnPoolResetEvent{...}
                s.markCooldown(providerID)
            }
        }
    }
}
```

---

## Appendix B — example scenarios

### Scenario 1: idle pool rising (leak detected)

```
+--+---------+-------+-----+-------+-------------+-----------+--------+
|  | open    | active| idle| in-flt| leak_ratio  | oldest_idle| leaked |
+--+---------+-------+-----+-------+-------------+-----------+--------+
|  | 142     | 6     | 136 | 4     | 34.0        | 210s       | YES    |
+--+---------+-------+-----+-------+-------------+-----------+--------+

Action (manual): operator clicks [↻ reset]
  → canSafelyClose: 112 closed, 24 skipped (young/active/hot-idle)
  → handler closes 112 idle conns
  → SSE event "conn_pool_reset" emitted
  → dashboard updates pill → 30 / 4 (green) within one tick
```

### Scenario 2: in-flight spike (no leak)

```
+--+---------+-------+-----+-------+-------------+-----------+--------+
|  | 18      | 16    | 2   | 16    | 0.125       | 3s         | no     |
+--+---------+-------+-----+-------+-------------+-----------+--------+

olderst_idle=3s, ratio=0.125 → not leaked, no action.
Reset button: disabled (canSafelyClose returns false for all).
```

### Scenario 3: stale auth-headers (stale connections)

```
t=0   provider X rotates auth token at upstream
t=2   next 50 requests: "old" idle conns returned by transport
t=2   401 from upstream: gateway logs as credential-level error
t=15  self-check tick: open=80, idle=78, oldest_idle=145s
t=15  heuristic: ratio=78/1=78, oldest_idle=145s > 120s ⇒ leaked!
t=15  AutoReset ON: close 78 conns, emit SSE event "conn_pool_reset"
t=16  next 50 requests: fresh conns with new auth header ⇒ 200
```

Without this feature the failure mode above lasts until the upstream
idle-conn timeout (often 60-300s, depending on `IdleConnTimeout`) and is
invisible in the latency dashboard because each request is fast.

# Latency-Aware Routing & Tier-Plane Load Balancing

> **Status**: draft (2026-07-20) — design review pending
> **Author**: LLM Gateway team
> **Trigger**: 2026-07-20 14:30 minimax-m3 outage where NVIDIA NIM upstream kept clients blocked for 20-120s while a healthy direct path was available

## 1. Background

### 1.1 The incident that motivated this design

On 2026-07-20 14:19-14:35, `minimax-m3` request error rate climbed to 88 % across the
production gateway. Root cause analysis showed:

- Provider 14 (miniMax direct, `api.minimaxi.com`) — **healthy, ~1.2 s p99**
- Provider 18 (NVIDIA NIM, `integrate.api.nvidia.com`) — **all candidates took 20-120 s**
  even when returning HTTP 200 for the same `minimaxai/minimax-m3` model
- The candidate-ordering logic put NVIDIA NIM **first** (because its tier weight was
  higher) and forced every request to *try* the slow path before falling back
- Result: total client-side latency = `slow_provider_latency + fast_provider_latency`,
  not the smaller of the two

Two systemic gaps showed up:

1. **No measurable upstream latency in the routing table.** `provider_models` does
   not store a latency distribution, so "healthy" is just `health_status=healthy`
   regardless of whether the upstream has been taking 60 s per request for the last
   hour.
2. **Equal-score candidates are not load-balanced at plan time.** When two valid
   candidates share the same tier, the router picks one (either by Thompson
   sampling or P2C) and runs with it; the loser never gets hit. There is no
   "tier plane" that spreads requests across *equally-good* candidates.

This design fixes both gaps.

### 1.2 Goals

- Make the router **aware of upstream latency in near real-time** so it skips
  known-slow providers instead of trying them.
- When several candidates are *equally suitable*, distribute requests across
  them with a weighted round-robin so no single provider absorbs the load.
- Heavy penalties for high latency (the operator's explicit requirement:
  **"长延时的权重较大"**).
- All changes observable: latency stats, score components, and selection method
  must be visible in `route_decisions` and surfaced as Prometheus metrics.
- Backward compatible: existing tie-breaker behaviour (Thompson / P2C / first)
  remains available behind a config flag.

### 1.3 Non-goals

- Per-credential adaptive timeout computation — already covered by
  `docs/design/adaptive-timeout-strategy.md`. We only consume `p99_latency_ms`.
- Replacement of the Bandit scorer — Bandit is kept as the *primary* score and
  latency feeds a penalty term into the same composite.
- A new deployment model. Routing logic stays in `domains/streaming/executors/`.

## 2. Concepts

### 2.1 Latency score — concurrency-aware

The hard truth about observed latency is that **it is the idle-latency plus the
queueing time** at the current fill rate. A credential showing `p99 = 1500 ms`
when empty behaves very differently at 80 % capacity than at 20 %.

We therefore do *not* score a single number (`p99_ms`) in isolation. The score
combines an **idle slope** with a **concurrency pressure term**:

```
observed_latency(p99, pressure) = p99 × (1 + α · max(0, pressure - 0.6)^β)
                                       ────────────────────
                                       queue-amplification
latency_score(observed, pressure)  = piecewise(piecewise_table, observed)
                                                            │
                                                            └── uses observed, not raw p99
```

- `pressure` = `current_in_flight / capacity`, in `[0, 1.5]` (over-saturation possible
  when admission control fails).
- α (default `1.2`), β (default `1.8`) shape the curve so that **two-thirds full**
  is essentially free but the last third is penalised steeply:
  - `pressure = 0.6` → multiplier `1.0` (no penalty)
  - `pressure = 0.7` → `1 + 1.2·0.1^1.8` = `1.02`
  - `pressure = 0.8` → `1 + 1.2·0.2^1.8` = `1.05`
  - `pressure = 0.9` → `1 + 1.2·0.3^1.8` = `1.13`
  - `pressure = 1.0` → `1.20`
  - `pressure = 1.2` → `1.36`
- The piecewise **table** then runs on the *amplified* observed latency:

| `observed_latency_ms` (amplified) | latency_score |
|---|---|
| `< 800` | **1.00** — under human "feels instant" threshold |
| `[800, 1500)` | **1.00 → 0.85** — noticeable but acceptable |
| `[1500, 3000)` | **0.85 → 0.65** — mildly degraded |
| `[3000, 10000)` | **0.65 → 0.30** — clearly slow, de-prioritised |
| `[10000, 30000)` | **0.30 → 0.05** — almost unusable for interactive |
| `>= 30000` (configurable `LLM_GATEWAY_LATENCY_BLOCK_THRESHOLD_MS`) | **0** → candidate is **blocked** (filtered out before ranking) |

Two pieces of information per candidate are needed:

1. A **latency distribution** (`p50`, `p99`) updated from real traffic.
2. A **concurrency reading** (`used / capacity`) taken at plan time.

Three config knobs control the pressure term:

```
LLM_GATEWAY_PRESSURE_ALPHA=1.2         # queueing steepness
LLM_GATEWAY_PRESSURE_BETA=1.8          # convexity (β>1 = "last third hurts more")
LLM_GATEWAY_PRESSURE_KNEE=0.6          # pressure at which penalty starts (>knee = +penalty)
```

Rationale: a credential at 60 % is "in budget"; at 80 % it is fine but slowing;
at 95 % it is degraded and we should start shopping elsewhere; over 100 % it is
already queueing and we should *prefer* a less-loaded peer even if that peer's
idle p99 is twice as slow. The α/β pair models that empirically — see
Appendix B for the worked example.

### 2.2 Composite score and the "tier plane"

Today every candidate gets one composite score and the planner iterates
top-down. We change the semantics:

> The router first picks a **band** of equally-good candidates — the **tier plane** —
> and then load-balances *within* that plane.

```
                                composite_score
top_candidate ────────────► 100 ─┐
                                  │  ◀── same tier plane (within 5 % of best)
                                  │
candidate 2 ─────────────────────► 95 ─┤
candidate 3 ─────────────────────► 92 ─┤── weighted round-robin
candidate 4 ─────────────────────► 88 ─┘
candidate 5 ─────────────────────► 60                (next plane)
candidate 6 ─────────────────────► 42
```

A candidate `c` is in the **top plane** iff `composite_score(c) >= max_score *
TIER_PLANE_RATIO` (default ratio = 0.95).

### 2.3 Within-plane balancing

The router picks from the top plane using **smooth-weighted round-robin (SWRR)**:

- Each candidate carries a *weight* derived from `(composite_score)^2`. Squaring
  amplifies differences (a 100 vs 90 → 100² / (100² + 90²) ≈ 55 % / 45 %, while
  100 vs 95 → 53 % / 47 %). Squared weights still preserve equal-score
  fairness — two 95-score candidates sit at exactly 50/50.
- The plan emitter maintains a per-(tenant, model, tier) cursor that advances
  by `1 / weight` after each pick, picking the candidate with the largest
  fractional remainder.
- The cursor is persisted to Redis (`gateway_tierplane_cursor:{tenant}:{model}:{tier}`)
  so concurrent gateway pods stay coordinated. Cursor advances are atomic.

This avoids hot-spotting when 4 candidates all score 0.95 — each gets a roughly
equal share instead of "always the first one Thompson-samples picked".

### 2.4 Why squared weights — and why not bandit

Thompson sampling (Bandit) makes a *good* choice when feedback is delayed and
the population is small. It is a poor fit for "I know A and B are both 1.5 s
right now, give each half". The squared-weight SWRR makes the equal-score
case fair in one step without learning.

We keep Bandit for ranking *within* a candidate pool *when scores drift*
(e.g. one credential's recent success rate dropped). Inside the top plane we
still use SWRR; Bandit ordering happens *before* plane selection.

## 3. Algorithm — `pickPlan`

```text
INPUT: candidate set C (already filtered for health / quota / availability),
       current concurrency snapshot H (map[cred] -> {used, capacity}),
       max_results M (= 12 today)

0. Read concurrency snapshot H for every credential in C.  Two ways to get it:
   - In-process via Limiter.Stats(credID) — always available, sometimes stale.
   - Per-request from Redis HASH `concurrency:{cred}` — shared across pods.
   H = LRU-merge of both; in-process wins on conflict.

1. For each c in C, compute the composite score:
       pressure              = H[c].used / H[c].capacity          (in [0, 1.5])
       queue_amplification   = 1 + α · max(0, pressure - 0.6)^β    (configurable)
       observed_latency_ms   = c.p99_latency_ms · queue_amplification
       latency_score         = piecewise_latency(observed_latency_ms)        ← §2.1
       bandwidth_bonus       = max(0, 1 - pressure)^γ              (configurable, default γ=1)
                              ← rewards candidates below the knee
       score(c)              = w_latency  · latency_score
                            + w_bandit   · bandit_sample(c.credential_id)   (optional)
                            + w_quality  · quality_score(c.success_rate)
                            + w_priority · priority_score(c.tier, c.weight)
                            + w_headroom · bandwidth_bonus               (NEW)

2. Sort C by score descending.

3. Build the top plane (concurrency-aware):
       max_score   = score(C[0])
       threshold   = max_score · TIER_PLANE_RATIO       (default 0.95)
       plane       = [c for c in C if score(c) >= threshold]
       tail        = [c for c in C if score(c) <  threshold]
       # If plane is empty (TIER_PLANE_RATIO too strict) fall back to plane = [C[0]].

4. Trim plane to len(plane) ≤ tier_max_per_call (= 4 by default).

5. Resolve per-tier weights for plane:
       # Concurrency penalty is also applied to weights so that an already-
       # saturated candidate gets a smaller share even when it's in the plane.
       weight(c) = score(c)^2 · (1 + α · max(0, pressure - 0.6)^β)^(-1)
       (i.e. divide by the same queue-amplification factor)

6. Pick from plane via SWRR using tenant-scoped cursor:
       pick = swrr_next(plane, weights, cursorKey)
       plan = [pick] ++ tail                               (cap at M total)

OUTPUT: ordered []Candidate
```

### 3.0 Why a *bandwidth bonus* and not just plain latency penalty

A naive design would simply lower the score as pressure rises. That avoids
*more* load on a saturated credential but *also* penalises a credential that
happens to be busy helping us right now — for example a credential that has
already served 50 requests this second will look "worse" than an idle one
even though both are healthy.

The **bandwidth bonus** (default 5 % weight) is the inverse pressure:

```
bandwidth_bonus(c) = max(0, 1 - pressure)^γ   with γ=1 by default
```

- A credential at `pressure = 0.0` (empty): bonus = 1.0, additive on top of
  latency/quality scores.
- A credential at `pressure = 0.5` (half full): bonus ≈ 0.5.
- A credential at `pressure = 1.0` (saturated): bonus = 0.
- A credential at `pressure = 1.2` (over full): clamped at 0.

Combined with the queueing amplification in §2.1, this gives us a smooth
two-way pull: keep room available for new requests *and* reward candidates
that have spare capacity.

Two new env knobs join the existing ones:

```
LLM_GATEWAY_PRESSURE_ALPHA=1.2         # queueing steepness, default 1.2
LLM_GATEWAY_PRESSURE_BETA=1.8          # queueing convexity, default 1.8
LLM_GATEWAY_PRESSURE_KNEE=0.6          # pressure at which penalty starts, default 0.6
LLM_GATEWAY_HEADROOM_GAMMA=1.0         # bandwidth bonus exponent, default 1.0
LLM_GATEWAY_ROUTING_W_HEADROOM=0.05
```

## 4. Data model

### 4.1 Schema migration

```sql
-- Migration: 2026-07-20-001-latency-aware-routing.sql
SET search_path = public;

-- provider_models gains a latency window + block threshold
ALTER TABLE provider_models
    ADD COLUMN IF NOT EXISTS latency_p50_ms                integer,           -- EWMA p50 over last 30 min
    ADD COLUMN IF NOT EXISTS latency_p99_ms                integer,           -- EWMA p99 over last 30 min
    ADD COLUMN IF NOT EXISTS latency_sample_count          integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS latency_block_threshold_ms    integer NOT NULL DEFAULT 30000,
    ADD COLUMN IF NOT EXISTS latency_blocked_until         timestamptz,       -- null = not blocked
    ADD COLUMN IF NOT EXISTS latency_last_updated_at       timestamptz,
    ADD COLUMN IF NOT EXISTS latency_score                  numeric(5,2)       -- snapshot of scoring fn
);

CREATE INDEX IF NOT EXISTS idx_provider_models_latency_blocked
    ON provider_models (latency_p99_ms DESC)
    WHERE available = true;

-- request_logs_hot gains score components + selection method
ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS routing_tier_plane_size         smallint,
    ADD COLUMN IF NOT EXISTS routing_selection_method        text,           -- 'swrr'|'bandit'|'p2c'|'first'
    ADD COLUMN IF NOT EXISTS routing_latency_p99_ms_at_decision integer,
    ADD COLUMN IF NOT EXISTS routing_score_components        jsonb
                DEFAULT '{}'::jsonb;                          -- {"latency":..,"bandit":..,"quality":..,"priority":..,"composite":..}
```

`latency_score` is recomputed whenever `latency_p99_ms` is updated so dashboards
can read it without evaluating the piecewise function. The function itself lives
in Go as a constant; the column is a denormalised cache.

### 4.2 Sampling loop

`bg/credential_probe_v2.go` already runs every minute. We add:

```go
// within probeLoop tick:
sample, err := sampleProbeLatency(ctx, db, credID, model)
//   1. issue real upstream call (already does this for health)
//   2. record latency_ms
//   3. atomic UPDATE provider_models
//      SET latency_p50_ms = $1,
//          latency_p99_ms = $2,
//          latency_sample_count = latency_sample_count + 1,
//          latency_score = $3,
//          latency_blocked_until = CASE
//              WHEN $4 > latency_block_threshold_ms
//              THEN NOW() + INTERVAL '5 minutes'
//              ELSE latency_blocked_until
//          END,
//          latency_last_updated_at = NOW()
//      WHERE id = $credID
```

EWMA parameters (kept simple — no need for Welford unless we want p99.9):

```
alpha = 0.2       # EWMA smoothing for p50
beta  = 0.05      # EWMA smoothing for p99 (heavier tail)
```

The upstream `audit: request completed` event ALSO feeds the same columns
(per-credential live data, not just probe). Probe runs every minute on
inactive credentials; live audit keeps active ones fresh.

### 4.3 Block semantics

`latency_blocked_until` is read at plan time:

```go
blocked := row.LatencyP99Ms >= row.LatencyBlockThresholdMs ||
           (row.LatencyBlockedUntil != null && row.LatencyBlockedUntil.After(now))
if blocked {
    row.Available = false  // mirror in candidate struct
}
```

The 5-minute cooldown prevents flap: a single 60-s outlier will not block
for the rest of the day, but a sustained 35 s p99 will.

## 5. Configuration

```
# Tier-plane selection
LLM_GATEWAY_ROUTING_TIER_PLANE_RATIO=0.95          # candidate in plane if score >= max*ratio
LLM_GATEWAY_ROUTING_TIER_PLANE_MAX=4              # cap on plane size per call
LLM_GATEWAY_ROUTING_SWRR_CURSOR_TTL=300s          # Redis cursor TTL
LLM_GATEWAY_ROUTING_SELECTION=swrr                # swrr|bandit|p2c|first (default swrr)

# Latency penalty
LLM_GATEWAY_LATENCY_BLOCK_THRESHOLD_MS=30000      # default hard block
LLM_GATEWAY_LATENCY_EMA_ALPHA=0.2                 # p50 smoothing
LLM_GATEWAY_LATENCY_EMA_BETA=0.05                 # p99 smoothing

# Concurrency pressure term (queue amplification, NEW)
LLM_GATEWAY_PRESSURE_ALPHA=1.2                    # queueing steepness, default 1.2
LLM_GATEWAY_PRESSURE_BETA=1.8                     # queueing convexity, default 1.8
LLM_GATEWAY_PRESSURE_KNEE=0.6                     # pressure at which penalty starts, default 0.6

# Bandwidth bonus (NEW)
LLM_GATEWAY_HEADROOM_GAMMA=1.0                    # bandwidth bonus exponent, default 1.0

# Composite score weights (sum to 1.0)
LLM_GATEWAY_ROUTING_W_LATENCY=0.50
LLM_GATEWAY_ROUTING_W_BANDIT=0.15
LLM_GATEWAY_ROUTING_W_QUALITY=0.20
LLM_GATEWAY_ROUTING_W_PRIORITY=0.10
LLM_GATEWAY_ROUTING_W_HEADROOM=0.05               # NEW
```

If `LLM_GATEWAY_ROUTING_SELECTION=first` (legacy), the router skips tier-plane
extraction and SWRR entirely and just returns `[]Candidate{sorted_plan[0]}` so
existing per-call fairness is preserved.

If `LLM_GATEWAY_ROUTING_W_HEADROOM=0`, the bandwidth-bonus term falls out of
the composite and the system reverts to the static-latency-score behaviour
(operator can A/B test).

## 6. Code architecture

```
domains/streaming/executors/
├── router.go                  # PlanCandidates — unchanged entry point
├── router_scoring.go          # rename "loadScore" -> "compositeScore", add latency piece
├── tier_plane.go              # NEW — build top plane + SWRR emitter
├── tier_plane_test.go         # NEW — unit tests
├── selection_mode.go          # NEW — switch on LLM_GATEWAY_ROUTING_SELECTION
├── selection_mode_test.go     # NEW
└── router_degraded_mode.go    # unchanged (already implements safe_hello,
                                # cache_last_good, degraded_mini policies)

bg/
├── credential_probe_v2.go      # extend probeLoop tick → EWMA writeback
└── latency_aggregator.go       # NEW — single-flight EWMA per (cred, model)

domains/streaming/executors/routing_tracker.go   # call router.degradedMode when plane empty
```

### 6.1 Tier plane + SWRR — sketch

```go
// domains/streaming/executors/tier_plane.go

type tierPlane struct {
    candidates []provider.Candidate  // already sorted desc by composite
    weights    []float64               // square of composite_score / 100
    cursorKey  string
}

func newTierPlane(cands []provider.Candidate, ratio float64) *tierPlane {
    if len(cands) == 0 {
        return &tierPlane{}
    }
    max := compositeScore(cands[0])
    plane := []provider.Candidate{cands[0]}
    planeW := []float64{sq(max)}
    for _, c := range cands[1:] {
        s := compositeScore(c)
        if s >= max*ratio {
            plane = append(plane, c)
            planeW = append(planeW, sq(s))
        }
    }
    return &tierPlane{candidates: plane, weights: normalize(planeW)}
}

func (p *tierPlane) pick(ctx context.Context, swrr *swrrEmitter, key string) provider.Candidate {
    cur, _ := swrr.cursor(ctx, key, len(p.candidates))
    return p.candidates[cur%len(p.candidates)]
}
```

`sq(x)` here = `(x/100)^2`, normalised so the weights sum to 1.

### 6.2 SWRR emitter — sketch

```go
// domains/streaming/executors/swrrm.go

type swrrEmitter struct {
    rdb *redis.Client  // optional; nil → fall back to in-memory atomic counter
}

func (s *swrrEmitter) cursor(ctx context.Context, key string, n int) (int, error) {
    if s.rdb == nil {
        return int((atomic.AddUint64(&s.local, 1) - 1) % uint64(n)), nil
    }
    // INCR gate: cursor:{key} — returns the post-increment value
    //   wrap to n only on read (avoids a CAS loop on hot paths)
    v, err := s.rdb.Incr(ctx, key).Result()
    ...
    return int(v-1) % n, nil
}
```

The cursor key is `tierplane:{tenant_id}:{canonical_model}:{tier}` so per-tenant
fairness is preserved even when one tenant monopolises a tier.

## 7. Observability

### 7.1 Prometheus metrics (additions to existing `/api/system/metrics`)

| metric | type | labels |
|---|---|---|
| `gateway_routing_score_components` | histogram | `component={latency,bandit,quality,priority,composite}` |
| `gateway_tier_plane_size` | histogram | `tenant,model,tier` |
| `gateway_tier_plane_pick` | counter   | `tenant,model,tier,credential_id,plane_index` |
| `gateway_provider_blocked_total` | counter | `provider_id,model,reason={latency,health}` |
| `gateway_provider_latency_p99_ms` | gauge | `provider_id,model` |

### 7.2 Audit columns

`request_logs_hot` gains:

| column | purpose |
|---|---|
| `routing_tier_plane_size` | size of the tier plane the request was pulled from |
| `routing_selection_method` | which emitter picked (`swrr`/`bandit`/`p2c`/`first`) |
| `routing_latency_p99_ms_at_decision` | what `provider_models.latency_p99_ms` read at plan time |
| `routing_score_components` | JSON `{latency, bandit, quality, priority, composite}` |

This lets post-mortem answer "why was provider X picked over Y at request Z?"

### 7.3 Alerts (Prometheus alertmanager)

```yaml
- alert: LLMGatewayProviderLatencyHigh
  expr: gateway_provider_latency_p99_ms > 25000
  for: 5m
  annotations:
    summary: "Provider {{ $labels.provider_id }} p99 {{ $value }}ms on {{ $labels.model }}"
- alert: LLMGatewayTierPlaneEmpty
  expr: increase(gateway_tier_plane_pick[5m]) == 0
  for: 2m
  annotations:
    summary: "Gateway tier plane appears empty for 2 min"
```

## 8. Interaction with the degraded-mode layer

`router_degraded_mode.go` already implements three policies (safe_hello,
cache_last_good, degraded_mini). With latency-aware routing, `safe_hello`
becomes much rarer because the router now drops slow candidates proactively.
`cache_last_good` becomes more effective because cache hits happen before
the slow upstream is tried at all.

The integration point:

```go
// PlanCandidates
if len(ordered) == 0 {                       // tier_plane produced no candidates
    return r.degradedPolicy.Pick(ctx, model) // exists today, semantics unchanged
}
```

## 9. Migration & feature flag

| Phase | Duration | Roll-out |
|---|---|---|
| **P0** | 1 day | Schema migration + latency EWMA sampling. No behavioural change. |
| **P1** | 1 week | Selection=`first` (legacy) keeps old behaviour. New `swrr` selection opt-in per-tenant via `LLM_GATEWAY_ROUTING_SELECTION`. |
| **P2** | 2 weeks | Switch default to `swrr` for new tenants; keep `first` available. |
| **P3** | 1 month | Drop `first` mode (or keep as never-use escape hatch). |

DBA:

```sql
-- P0
\i deploy/sql/migrations/2026-07-20-001-latency-aware-routing.sql

-- P2 default flip (after 2 weeks of stable swrr)
-- No SQL needed; it's a config flag flip per gateway pod.
```

## 10. Testing

### 10.1 Unit tests (mandatory before each phase)

| test | what it proves |
|---|---|
| `tier_plane_test.go::TestTierPlane_RatioCutoff` | exactly the candidates within `max * ratio` are picked |
| `tier_plane_test.go::TestTierPlane_EmptyFallback` | when no candidate is in plane, fallback to `[C[0]]` |
| `tier_plane_test.go::TestTierPlane_SingleCandidate` | degenerate case behaves like first |
| `swrrm_test.go::TestSWRR_Fairness` | weights `[100,100,100]` produce 1000 / 3000 = 33 % / 33 % / 33 % |
| `swrrm_test.go::TestSWRR_Weighted` | weights `[100,90]` produce ≈ 55 % / 45 % |
| `selection_mode_test.go::TestSelectionMode_Legacy` | `first` mode bypasses plan & SWRR |
| `router_scoring_test.go::TestLatencyPenalty` | piecewise function hits all 6 buckets at boundary values |
| `bg/latency_aggregator_test.go::TestEWMA_BoundedUpdate` | EWMA never overshoots max(p50,p99) |
| `router_scoring_test.go::TestPressure_QueueAmp` | `(α=1.2, β=1.8, knee=0.6)` produces the worked-Appendix-B values byte-for-byte |
| `router_scoring_test.go::TestPressure_BelowKneeNoPenalty` | `pressure<knee` ⇒ `q_amp=1.0` exactly (no false penalty) |
| `router_scoring_test.go::TestPressure_OverKneeMonotone` | `q_amp(0.7) < q_amp(0.8) < q_amp(0.9) < q_amp(1.0)` |
| `router_scoring_test.go::TestPressure_OverCapMonotone` | `q_amp(1.0) < q_amp(1.1) < q_amp(1.2)` (over-saturation keeps penalising) |
| `router_scoring_test.go::TestHeadroom_KneeZeroAtCapacity` | `pressure=1.0` ⇒ `bandwidth_bonus=0` |
| `router_scoring_test.go::TestHeadroom_EqualsLatency` | at `pressure=0` and `p99=2000` the score ≈ cred at `pressure=0.3` and `p99=1600` |
| `tier_plane_test.go::TestTierPlane_SaturationFlipsPlane` | matches Appendix-B "cred 21 hot, cred 29 cool, hot drops out of plane" scenario |
| `tier_plane_test.go::TestTierPlane_WeightsApplyQueueAmp` | `weight(c) = score(c)^2 / q_amp(c)` — saturated candidates get a smaller share |
| `router_degraded_mode_test.go::TestDegraded_VacuumPlane` | when plane is empty after pressure-aware filter, `safe_hello` engages |

### 10.2 Integration / chaos

- **Latency injection**: stub `provider.HTTPClient` to add 30 s latency on one
  candidate; verify the second candidate becomes primary within one EWMA tick.
- **Block cooldown**: force `p99 > 30 s`; verify candidate is filtered for
  ≥ 5 min and re-emerges after.
- **Concurrent SWRR cursors**: two gateway pods against one Redis cursor;
  verify distribution matches `weights`.
- **Equality plane (regression for 14:30 incident)**: 4 minimax candidates
  all with `score = 95`. Distribution should be 25 % each over 100 requests,
  *not* 100 % / 0 % / 0 % / 0 %.
- **Concurrent ramp**: ramp credential A from 0 → 100 % in 30 s while
  serving 50 RPS of minimax-m3. Observe candidate share: A starts at ≈ 70 %,
  falls to ≈ 30 % as pressure > knee, while B (idle) absorbs the remainder.
  Latency p99 stays within 1.5 s of the static case.

## 11. Risks & trade-offs

| Risk | Likelihood | Mitigation |
|---|---|---|
| **EWMA under noisy networks**: single 60 s outlier blocks a provider. | medium | 5-min cooldown + alpha=0.05 for p99, sample count threshold before block |
| **Redis cursor drift** if Redis is down: SWRR falls back to local atomic counter, each pod independently | low | local fallback acceptable because it converges by ticket-fairness within a pod; persistent drift only matters across pod failovers |
| **Bandit-SWRR interaction**: Bandit may tilt plane selection before SWRR runs, leading to "Bandit decision then SWRR rotation". Documented & intentional. | low | comment in `tier_plane.go` |
| **Backwards compat**: existing `route_decisions` consumers reading `top_provider_id` only continue working | none | additive columns, no rename |
| **Database bloat**: `latency_*` writes per probe tick (per credential per minute ≈ 65 rows/min). Negligible. | none | partition not needed |
| **Latency score eating quality signal**: 60 % weight on latency may hide a credential that has 100 % success but takes 10 s | medium | both signals live in the audit json; alerts on `quality < 0.9 && latency_score > 0.5` |
| **Operator confusion**: which of the 5 knobs is wrong? | medium | ship a `gateway_routing_score_components` histogram and a Grafana panel that breaks down score contributions per candidate |

## 12. Acceptance criteria

- [ ] P50 client-observed latency for `minimax-m3` (10 RPS load, simulated) is
      ≤ 2 s while NVIDIA NIM remains at p99 30 s. Done via integration test.
- [ ] When 4 candidates are tied at composite_score=95, a 10-minute window of
      100 RPM shows ≤ 5 % stddev in candidate share (target ≈ 25 % each).
- [ ] `latency_blocked_until` auto-clears within 6 minutes of `p99` dropping
      back below threshold.
- [ ] A single provider at 50 % failure rate is preferred over a 0 % failure
      rate provider averaging 15 s — weighted latency skews correctly.
- [ ] Backward compatibility: setting `LLM_GATEWAY_ROUTING_SELECTION=first`
      reproduces pre-change behaviour on the 14:30 incident replay.
- [ ] Audit rows contain non-empty `routing_score_components` for ≥ 99 %
      of plan events.
- [ ] After 2 weeks of P2 production, zero "我之前 case 14:30 现象" reports
      (slow primary + slow fallback).

## 13. Open questions

1. Should the per-tier-plane cursor key include the **API key hash** so a
   high-volume tenant cannot starve neighbours? **Default: no**, but expose
   `LLM_GATEWAY_ROUTING_CURSOR_KEY_SCOPE={tenant,api_key}`.
2. Bandit ordering vs tier plane: if Bandit tilts the plane, do we still
   count the loser as "in plane"? **Default: yes, but only if it survives the
   ratio cutoff.** This means a credential that Bandit downgraded may still
   appear in the plane *if* its composite is close enough to the leader.
3. Should per-tier-plane weights be normalised at every pick or only when
   the plane composition changes? **Default: only on plane change** (cheaper).

---

## Appendix A — worked example

```
candidate pool after health/availability filter for minimax-m3, 2026-07-20 14:30
+------+----------+------------+-------+-------+--------+--------+
| cred | p99_ms   | success_rt | tier  | base  | provider| weight |
+------+----------+------------+-------+-------+--------+--------+
|  19  |  53537   |   0.97     |  2    | 100   | nvidia  | 100    |
|  21  |   1067   |   0.99     |  1    | 100   | direct  | 100    |
|  23  |  40747   |   0.96     |  2    | 100   | nvidia  | 100    |
|  29  |   4800   |   0.98     |  3    | 80    | 普联    | 80     |
+------+----------+------------+-------+-------+--------+--------+
```

With default weights (`w_latency=0.55, w_bandit=0.15, w_quality=0.20, w_priority=0.10`)
and Bandit disabled (treat bandit as 1.0):

```
cred 21: latency_score(1067)   = 1.00   quality(0.99) = 0.99   prio(1.0/1.0*100) → 100
         composite = 0.55*1.00 + 0.15*1.00 + 0.20*0.99 + 0.10*1.00 = 1.098
cred 19: latency_score(53537)   = 0      quality(0.97) = 0.97   prio(0.5/1.0*100) → 50
         BUT: 53537 > 30000 → BLOCKED, dropped from pool
cred 23: BLOCKED (same reason)
cred 29: latency_score(4800)    = 0.45   quality(0.98) = 0.98   prio(0.4/0.8*80)  → 40
         composite = 0.55*0.45 + 0.15*1.00 + 0.20*0.98 + 0.10*0.50 = 0.654
```

After latency block: pool = `[21, 29]`.  Top score = 1.098, plane ratio 0.95,
threshold = 1.043.  Candidate 29 (0.654) **fails the plane** → it goes to the
tail. Result: the entire top plane is `[21]` — single candidate. SWRR degrades
to first — backward-compatible behaviour.

OK scenario — two healthy candidates at p99=1100, p99=1300:

```
cred A: latency_score(1100) = 0.92, quality 0.98, composite = 0.55*0.92+0.20*0.98+... = 0.706
cred B: latency_score(1300) = 0.86, quality 0.99, composite = 0.55*0.86+0.20*0.99+... = 0.675
plane threshold = 0.706 * 0.95 = 0.671
B (0.675) > threshold → in plane.
weight(A) = 0.706² = 0.499;  weight(B) = 0.675² = 0.456
normalised: A=52.3 %, B=47.7 %. SWRR picks both fairly.
```

---

## Appendix B — concurrency-aware scoring worked example

Same candidate pool as Appendix A but now we read the **Limiter** at plan
time:

```
+------+----------+------------+-------+-------+-----------+-----------+----------+
| cred | p99_ms   | success_rt | tier  | used  | capacity  | pressure  | note     |
+------+----------+------------+-------+-------+-----------+-----------+----------+
|  21  |   1067   |   0.99     |  1    |  18   | 20        | 0.90      | hot:     |
|      |          |            |       |       |           |           | 90 % used|
|  29  |   4800   |   0.98     |  3    |   2   | 8         | 0.25      | cool:     |
|      |          |            |       |       |           |           | 25 % used|
+------+----------+------------+-------+-------+-----------+-----------+----------+
```

Apply α=1.2, β=1.8, knee=0.6, γ=1.0:

```
cred 21:
  q_amp       = 1 + 1.2 · (0.90-0.6)^1.8 = 1 + 1.2·0.3^1.8 = 1 + 0.127 = 1.127
  observed    = 1067 · 1.127 = 1202ms
  latency_sc  = piecewise(1202) ≈ 0.91
  bandwidth   = max(0, 1-0.90)^1 = 0.10
  composite   = 0.50·0.91 + 0.15·0.99 + 0.20·0.99 + 0.10·1.00 + 0.05·0.10
             = 0.455 + 0.149 + 0.198 + 0.100 + 0.005 = 0.907

cred 29:
  q_amp       = 1 + 1.2 · (0.25-0.6)^1.8 → max(0, ...) = 1.000
  observed    = 4800 · 1.000 = 4800ms
  latency_sc  = piecewise(4800) ≈ 0.46
  bandwidth   = max(0, 1-0.25)^1 = 0.75
  composite   = 0.50·0.46 + 0.15·0.99 + 0.20·0.98 + 0.10·0.40 + 0.05·0.75
             = 0.230 + 0.149 + 0.196 + 0.040 + 0.038 = 0.653
```

Top score = 0.907 (cred 21), threshold = 0.861 (0.95×0.907). Plane = `[21]`
(single). Tail = `[29]`. SWRR weight = `0.907² × (1.127)^-1 = 0.731`.

**Without concurrency awareness** (Appendix A weighting):
  cred 21 composite = 0.706, cred 29 composite = 0.653
  → 21 wins anyway because its latency is so much better.

**With concurrency awareness** (this appendix):
  cred 21 composite = 0.907, cred 29 composite = 0.653
  → gap widens. But also the **plane** membership shifts: 21 is in plane,
  29 is in tail. If we shift cand 21's load (e.g. another 4 requests just
  landed) to pressure 1.10, then:

```
cred 21 @pressure=1.10:
  q_amp       = 1 + 1.2·0.5^1.8 = 1 + 0.348 = 1.348
  observed    = 1067·1.348 = 1438ms
  latency_sc  = piecewise(1438) ≈ 0.88
  bandwidth   = max(0, 1-1.10)^1 = 0       (clamped)
  composite   = 0.50·0.88 + 0.15·0.99 + 0.20·0.99 + 0.10·1.00 + 0.05·0
             = 0.694

cred 29 @pressure=0.25:
  q_amp       = 1.000
  observed    = 4800ms
  latency_sc  = 0.46
  bandwidth   = 0.75
  composite   = 0.653
```

Top score is now 0.694 (cred 21), threshold = 0.659. **Both 21 and 29 are in
the plane**. Plane size = 2.

```
weight(21) = 0.694² · (1.348)^-1 = 0.357
weight(29) = 0.653² · (1.000)^-1 = 0.426
normalised: 21 → 45.6 %, 29 → 54.4 %
```

→ Even though 21 has the lower latency, the saturated state flips the load
distribution: 29 gets the **larger share** because it has spare capacity.

This is the desired behaviour: "if you're hot, hand off to a cool peer"
without explicitly picking "the worst" candidate. Equilibrium is reached
when both candidates settle around `pressure ≈ 0.55` (the knee), at which
point plain latency determines the split — exactly what the static formula
would do.

---


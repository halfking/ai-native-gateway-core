// Package statesource owns the routing_state_source (S-3) field that the
// request-flow-audit design (docs/superpowers/specs/2026-07-27-request-flow-audit-design.md
// §8.2 and §11.4) requires the live data plane to expose.
//
// The value categorises which source actually answered the routing
// decision for a single request:
//
//   - StateSourceNodeMirrorHit   — NodeMirror LRU returned a fresh entry
//     (soft-expire not yet elapsed). Fastest path; no Redis IO on the
//     read side.
//   - StateSourceNodeMirrorMiss  — NodeMirror had no entry; the v2
//     pipeline read Redis to backfill (whether or not the keys were
//     present).
//   - StateSourceNodeMirrorStale — NodeMirror had an entry but it was
//     soft-expired; v2 still treated it as a miss and consulted Redis.
//     Kept distinct from Miss so operators can see how often the LRU
//     is actually serving traffic vs. backfilling.
//   - StateSourceFallback        — URSM v2 authoritative was selected
//     but the read path errored (Redis down, recovery gate flipped,
//     FilterAndScore returned an error). The router failed-open to
//     the legacy state source / DB-only path.
//   - StateSourceOutageMirror    — URSM v2 authoritative served a
//     degraded read-only decision from soft-expired NodeMirror entries
//     because Redis was unreachable (availability gear, 2026-09-04;
//     window bounded by URSM_V2_OUTAGE_GRACE_SECONDS).
//   - StateSourceOff             — URSM v2 was not configured (nil
//     manager) or ModeOff / ModeShadow. No v2 evaluation attempted.
//   - StateSourceCanary          — URSM v2 canary mode used v2
//     selection for this request (rollout.ShouldUseV2 == true).
//   - StateSourceAuthoritative   — URSM v2 authoritative + Ready +
//     FilterAndScore success path. The legacy state source is by
//     contract NOT consulted (spec §8.2: 旧 StateManager 不得参与
//     authoritative 健康判定).
//   - StateSourceSkipped         — Router chose not to record a source
//     (e.g., empty candidate set, no state backend selected, or an
//     internal test helper path). Carrying its own label keeps the
//     counter from being silently dropped on the call site.
//
// Counters are in-process; production scraping is done by the
// Prometheus collector wired in router.go (slog.Warn for
// routing_state_source="fallback" is preserved for backward compatibility
// with the existing log pipeline; the metric surface is the new
// observability channel for fail-open ratio).
package statesource

import (
	"sync"
	"sync/atomic"
)

// RoutingStateSource is the enum value carried in the
// routing_state_source metric label and in structured logs. Keep the
// underlying string values stable: dashboards and alerts depend on
// them. To add a new label, extend the const block AND the allSources
// slice below so ResetForTest / Snapshot cover the new entry.
type RoutingStateSource string

const (
	StateSourceNodeMirrorHit   RoutingStateSource = "node_mirror_hit"
	StateSourceNodeMirrorMiss  RoutingStateSource = "node_mirror_miss"
	StateSourceNodeMirrorStale RoutingStateSource = "node_mirror_stale"
	StateSourceFallback        RoutingStateSource = "fallback"
	StateSourceOutageMirror    RoutingStateSource = "outage_mirror"
	StateSourceOff             RoutingStateSource = "off"
	StateSourceCanary          RoutingStateSource = "canary"
	StateSourceAuthoritative   RoutingStateSource = "authoritative"
	StateSourceSkipped         RoutingStateSource = "skipped"
)

// allSources is the exhaustive set of RoutingStateSource values the
// counters know about. Snapshot() returns a fresh map keyed by every
// entry here (zero if never recorded), which keeps the metric surface
// stable: dashboards never see a label disappear mid-window because
// nobody has hit it yet.
var allSources = []RoutingStateSource{
	StateSourceNodeMirrorHit,
	StateSourceNodeMirrorMiss,
	StateSourceNodeMirrorStale,
	StateSourceFallback,
	StateSourceOutageMirror,
	StateSourceOff,
	StateSourceCanary,
	StateSourceAuthoritative,
	StateSourceSkipped,
}

// counters is the live counter store. We use atomic.Int64 so the
// router hot path (many concurrent PlanCandidates calls) does not
// serialise on a mutex. ResetForTest is the only writer that mutates
// the map topology; the per-source values are atomic.Ints and
// survive a Reset (ResetForTest calls .Store(0) on each).
var (
	countersMu sync.Mutex
	counters   map[RoutingStateSource]*atomic.Int64
)

func init() {
	counters = make(map[RoutingStateSource]*atomic.Int64, len(allSources))
	for _, s := range allSources {
		counters[s] = new(atomic.Int64)
	}
}

// RecordRoutingStateSource increments the per-source counter. Safe to
// call from any goroutine. Unknown sources are clamped to StateSourceSkipped
// to keep the metric label set closed (typos do not silently grow the
// label cardinality).
//
// The router call sites record EXACTLY ONE source per PlanCandidates
// invocation, on the path that actually decided the routing outcome
// (e.g. authoritative success → authoritative; authoritative +
// FilterAndScore error → fallback; canary hit → canary). Recording
// twice for the same request would double-count the fail-open ratio
// and is a test failure rather than a runtime error.
func RecordRoutingStateSource(source RoutingStateSource) {
	c := counterFor(source)
	if c == nil {
		source = StateSourceSkipped
		c = counterFor(source)
		if c == nil {
			return
		}
	}
	c.Add(1)
	// 2026-08-03 (spec §13 #10): mirror the in-process counter into
	// the Prometheus collector so /metrics exposes the fail-open ratio
	// (spec §11.4: routing_state_source 可在日志和指标中统计).
	routingStateSourceTotal.WithLabelValues(string(source)).Inc()
}

// counterFor returns the counter for a known source, or nil for an
// unknown value. ResetForTest is the only place that can register a
// new label; it is test-only.
func counterFor(source RoutingStateSource) *atomic.Int64 {
	countersMu.Lock()
	defer countersMu.Unlock()
	return counters[source]
}

// Snapshot returns a copy of the current counter values, keyed by every
// known source (zero-initialised entries are included so the metric
// surface is stable). The returned map is safe for the caller to mutate
// but contains pointer-stable values only via this snapshot — do not
// rely on it being live.
func Snapshot() map[RoutingStateSource]int64 {
	countersMu.Lock()
	defer countersMu.Unlock()
	out := make(map[RoutingStateSource]int64, len(counters))
	for k, v := range counters {
		out[k] = v.Load()
	}
	return out
}

// ResetForTest zeros every counter. Test-only; the production router
// does not call this. Hooking the reset into a single function (rather
// than a public Reset API) keeps the surface area minimal and makes
// the test/contract intent explicit.
func ResetForTest() {
	countersMu.Lock()
	defer countersMu.Unlock()
	for _, v := range counters {
		v.Store(0)
	}
}

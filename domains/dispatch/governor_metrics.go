package dispatch

// governor_metrics.go — Stage C closed-enum metric allowlist + emit helpers.
//
// ADR-0003 §"Stage C — Metrics & snapshot pipeline" locks four closed-enum
// label sets for the governor metric surface:
//
//   - backend ∈ {local, redis_enforce, redis_shadow}   (mirrors GovernorBackendKind)
//   - mode    ∈ {concurrency, rpm, tpm, disabled}      (mirrors queued_request.go Mode*)
//   - result  ∈ {ready, saturated, invalid_token, unknown}  (mirrors Lua return codes)
//   - state   ∈ {ready, queue_full, governor_saturated, unknown}  (mirrors SnapshotState)
//
// All governor metric registrations in this package MUST go through the
// typed wrappers below. The wrappers verify Opts.VariableLabels at
// construction (panic on mismatch) AND warm every known value at init time
// (panic on unknown value). At emit time, helpers reject off-list values
// with a slog warn — callers should never panic their hot-path goroutine
// over a label typo; the registration guard is where the typo surfaces.
//
// Out-of-tree dashboards/alerts that key on these labels MUST be kept in
// sync with the constants; renaming a value is a coordinate break.

import (
	"fmt"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Closed-enum value sets. Append-only. Coordinated with:
//
//	GovernorBackendKind (governor_backend.go),
//	ModeConcurrency / ModeRPM / ModeTPM / ModeDisabled (queued_request.go),
//	SnapshotState*     (governor_snapshot.go).
var (
	backendValues = []GovernorBackendKind{
		BackendLocal,
		BackendRedisEnforce,
		BackendRedisShadow,
	}
	modeValues = []string{
		ModeConcurrency,
		ModeRPM,
		ModeTPM,
		ModeDisabled,
	}
	// resultValues covers the four Lua / snapshot outcomes the governor
	// surface reports. "unknown" is the Sentinel used when the snapshot
	// itself was rejected (BackendErr != nil) — emit helpers should skip
	// it, not surface it.
	resultValues = []string{
		"ready",
		"saturated",
		"invalid_token",
		"unknown",
	}
	stateValues = []string{
		string(SnapshotStateReady),
		string(SnapshotStateQueueFull),
		string(SnapshotStateGovernorSaturated),
		string(SnapshotStateUnknown),
	}
)

// mustEqualLabelSet panics if want != got (slice equality, order-sensitive
// because Prometheus labels are positional in registration).
func mustEqualLabelSet(labelName string, want, got []string) {
	if len(want) != len(got) {
		panic(fmt.Sprintf(
			"dispatch: governor metric label %q variableLabels mismatch: got=%v want=%v",
			labelName, got, want))
	}
	for i := range want {
		if want[i] != got[i] {
			panic(fmt.Sprintf(
				"dispatch: governor metric label %q variableLabels mismatch at index %d: got=%q want=%q",
				labelName, i, got[i], want[i]))
		}
	}
}

// warmCounterValues runs WithLabelValues(v).Add(0) for every value in
// want so each known label combination exists at registration time. This
// mirrors domains/ursm/v2/shadow/metrics.go:42-46 and is required for the
// cardinality guard's Describe-channel emit to report every label.
func warmCounterValues(vec *prometheus.CounterVec, want []string, panicLabel string) {
	for _, v := range want {
		func() {
			defer func() {
				if r := recover(); r != nil {
					panic(fmt.Sprintf(
						"dispatch: governor metric warming failed for label %q value=%q: %v",
						panicLabel, v, r))
				}
			}()
			vec.WithLabelValues(v).Add(0)
		}()
	}
}

// warmGaugeValues mirrors warmCounterValues for GaugeVec.
func warmGaugeValues(vec *prometheus.GaugeVec, want []string, panicLabel string) {
	for _, v := range want {
		func() {
			defer func() {
				if r := recover(); r != nil {
					panic(fmt.Sprintf(
						"dispatch: governor metric warming failed for label %q value=%q: %v",
						panicLabel, v, r))
				}
			}()
			vec.WithLabelValues(v).Set(0)
		}()
	}
}

// warmHistogramValues mirrors warmCounterValues for HistogramVec.
func warmHistogramValues(vec *prometheus.HistogramVec, want []string, panicLabel string) {
	for _, v := range want {
		func() {
			defer func() {
				if r := recover(); r != nil {
					panic(fmt.Sprintf(
						"dispatch: governor metric warming failed for label %q value=%q: %v",
						panicLabel, v, r))
				}
			}()
			vec.WithLabelValues(v).Observe(0)
		}()
	}
}

// ─── typed wrappers ──────────────────────────────────────────────────────

// MustNewBackendCounterVec builds a CounterVec with the closed-enum
// backend label set. Panics if variableLabels does not equal {backend}
// (single-entry set, value drawn from backendValues).
func MustNewBackendCounterVec(opts prometheus.CounterOpts, variableLabels []string) *prometheus.CounterVec {
	want := []string{"backend"}
	mustEqualLabelSet("backend", want, variableLabels)
	vec := promautoMustNewCounterVec(opts, variableLabels)
	warmCounterValues(vec, backendStrings(), "backend")
	return vec
}

// MustNewModeHistogramVec builds a HistogramVec with the closed-enum mode
// label set. Panics on mismatch with {mode}.
func MustNewModeHistogramVec(opts prometheus.HistogramOpts, variableLabels []string) *prometheus.HistogramVec {
	want := []string{"mode"}
	mustEqualLabelSet("mode", want, variableLabels)
	vec := promautoMustNewHistogramVec(opts, variableLabels)
	warmHistogramValues(vec, modeValues, "mode")
	return vec
}

// MustNewStateGaugeVec builds a GaugeVec with the closed-enum state label
// set. Panics on mismatch with {state}.
func MustNewStateGaugeVec(opts prometheus.GaugeOpts, variableLabels []string) *prometheus.GaugeVec {
	want := []string{"state"}
	mustEqualLabelSet("state", want, variableLabels)
	vec := promautoMustNewGaugeVec(opts, variableLabels)
	warmGaugeValues(vec, stateValues, "state")
	return vec
}

// MustNewResultCounterVec builds a CounterVec with the closed-enum result
// label set. Panics on mismatch with {result}.
func MustNewResultCounterVec(opts prometheus.CounterOpts, variableLabels []string) *prometheus.CounterVec {
	want := []string{"result"}
	mustEqualLabelSet("result", want, variableLabels)
	vec := promautoMustNewCounterVec(opts, variableLabels)
	warmCounterValues(vec, resultValues, "result")
	return vec
}

// MustNewBackendModeHistogramVec builds a HistogramVec with {backend, mode}.
// Panics on label-set mismatch.
func MustNewBackendModeHistogramVec(opts prometheus.HistogramOpts, variableLabels []string) *prometheus.HistogramVec {
	want := []string{"backend", "mode"}
	mustEqualLabelSet("backend+mode", want, variableLabels)
	vec := promautoMustNewHistogramVec(opts, variableLabels)
	warmHistogramVec2D(vec, backendStrings(), modeValues)
	return vec
}

// MustNewBackendModeStateGaugeVec builds a GaugeVec with {backend, mode, state}.
func MustNewBackendModeStateGaugeVec(opts prometheus.GaugeOpts, variableLabels []string) *prometheus.GaugeVec {
	want := []string{"backend", "mode", "state"}
	mustEqualLabelSet("backend+mode+state", want, variableLabels)
	vec := promautoMustNewGaugeVec(opts, variableLabels)
	warmGaugeVec3D(vec, backendStrings(), modeValues, stateValues)
	return vec
}

// MustNewBackendModeResultCounterVec builds a CounterVec with {backend, mode, result}.
func MustNewBackendModeResultCounterVec(opts prometheus.CounterOpts, variableLabels []string) *prometheus.CounterVec {
	want := []string{"backend", "mode", "result"}
	mustEqualLabelSet("backend+mode+result", want, variableLabels)
	vec := promautoMustNewCounterVec(opts, variableLabels)
	warmCounterVec3D(vec, backendStrings(), modeValues, resultValues)
	return vec
}

// ─── emit helpers ────────────────────────────────────────────────────────

var (
	metricGovernorAcquire = MustNewBackendModeResultCounterVec(prometheus.CounterOpts{
		Name: "dispatch_governor_acquire_total",
		Help: "Acquisitions attempted by (backend, mode, result).",
	}, []string{"backend", "mode", "result"})

	metricGovernorAcquireLatency = MustNewBackendModeResultCounterVec(prometheus.CounterOpts{
		Name: "dispatch_governor_acquire_seconds_total",
		Help: "Cumulative seconds spent waiting on governor Acquire (counter of observation-time · count).",
	}, []string{"backend", "mode", "result"})

	metricGovernorRelease = MustNewBackendModeResultCounterVec(prometheus.CounterOpts{
		Name: "dispatch_governor_release_total",
		Help: "Releases attempted by (backend, mode, result).",
	}, []string{"backend", "mode", "result"})

	metricGovernorSnapshotState = MustNewBackendModeStateGaugeVec(prometheus.GaugeOpts{
		Name: "dispatch_governor_snapshot_state",
		Help: "Latest per-credGovernorSnapshot.State value (1 for the current state, 0 elsewhere).",
	}, []string{"backend", "mode", "state"})

	metricGovernorSnapshotAgeMS = MustNewBackendModeHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_governor_snapshot_age_ms",
		Help:    "Millisecond age of each emitted GovernorSnapshot.",
		Buckets: prometheus.ExponentialBuckets(50, 2, 10), // 50ms .. ~25s
	}, []string{"backend", "mode"})

	metricGovernorUnknownLabelDrops = promautoMustNewCounter(prometheus.CounterOpts{
		Name: "dispatch_governor_unknown_label_drops_total",
		Help: "Emit attempts dropped because a label value was outside the closed-enum allowlist.",
	})
)

// RecordAcquisitionResult emits a governor acquisition outcome. backend
// must be one of backendValues; mode one of modeValues; result one of
// resultValues. Off-list values are slog-warned and dropped (the counter
// is incremented so operators can spot mislabel regressions in dashboards).
func RecordAcquisitionResult(backend GovernorBackendKind, mode, result string, durSeconds float64) {
	if !isBackend(string(backend)) || !isMode(mode) || !isResult(result) {
		slog.Warn("dispatch: RecordAcquisitionResult skipped off-list label",
			"backend", string(backend), "mode", mode, "result", result)
		metricGovernorUnknownLabelDrops.Inc()
		return
	}
	metricGovernorAcquire.WithLabelValues(string(backend), mode, result).Inc()
	if durSeconds > 0 {
		metricGovernorAcquireLatency.WithLabelValues(string(backend), mode, result).Add(durSeconds)
	}
}

// RecordRelease emits a governor release outcome. Off-list labels are
// warned-and-dropped, same as RecordAcquisitionResult.
func RecordRelease(backend GovernorBackendKind, mode, result string) {
	if !isBackend(string(backend)) || !isMode(mode) || !isResult(result) {
		slog.Warn("dispatch: RecordRelease skipped off-list label",
			"backend", string(backend), "mode", mode, "result", result)
		metricGovernorUnknownLabelDrops.Inc()
		return
	}
	metricGovernorRelease.WithLabelValues(string(backend), mode, result).Inc()
}

// RecordSnapshot emits the snapshot's state into the {backend, mode, state}
// gauge (1 for the current state, 0 elsewhere so panels can still show
// "never observed = X" rather than missing series) and records its age
// into the histogram. State == SnapshotStateUnknown is dropped here —
// callers should consult snap.BackendErr first; an unknown-state snapshot
// has no valid downstream consumer and Validate() already panicked.
func RecordSnapshot(snap GovernorSnapshot) {
	if !isBackend(snap.Backend) || !isMode(snap.Mode) {
		slog.Warn("dispatch: RecordSnapshot skipped off-list label",
			"backend", snap.Backend, "mode", snap.Mode, "state", string(snap.State))
		metricGovernorUnknownLabelDrops.Inc()
		return
	}
	if snap.State == SnapshotStateUnknown {
		// Validate() guarantees State=Unknown ⇔ BackendErr!=nil; we never
		// emit an unknown row. No warn here — it's the normal "backend
		// faulted" path that Stage D treats as not-saturated.
		return
	}
	for _, s := range stateValues {
		if s == string(snap.State) {
			metricGovernorSnapshotState.WithLabelValues(snap.Backend, snap.Mode, s).Set(1)
		} else {
			metricGovernorSnapshotState.WithLabelValues(snap.Backend, snap.Mode, s).Set(0)
		}
	}
	if snap.AgeMS > 0 {
		metricGovernorSnapshotAgeMS.WithLabelValues(snap.Backend, snap.Mode).Observe(float64(snap.AgeMS))
	}
}

// ─── allowlist membership ────────────────────────────────────────────────

func isBackend(s string) bool {
	for _, v := range backendStrings() {
		if v == s {
			return true
		}
	}
	return false
}

func isMode(s string) bool {
	for _, v := range modeValues {
		if v == s {
			return true
		}
	}
	return false
}

func isResult(s string) bool {
	for _, v := range resultValues {
		if v == s {
			return true
		}
	}
	return false
}

func backendStrings() []string {
	out := make([]string, len(backendValues))
	for i, v := range backendValues {
		out[i] = string(v)
	}
	return out
}

// ─── promauto adapters ──────────────────────────────────────────────────
//
// promauto.MustRegister and promauto.New*Vec panic on registration
// conflicts (e.g. duplicate name). Routing them through small wrappers
// keeps the panic message prefix uniform with the allowlist panics above.

// ─── promauto adapters ──────────────────────────────────────────────────
//
// promauto.New*Vec panic on registration conflicts (e.g. duplicate name).
// Routing them through small wrappers keeps the panic message prefix
// uniform with the allowlist panics above.

func promautoMustNewCounterVec(opts prometheus.CounterOpts, variableLabels []string) *prometheus.CounterVec {
	return promauto.NewCounterVec(opts, variableLabels)
}

func promautoMustNewHistogramVec(opts prometheus.HistogramOpts, variableLabels []string) *prometheus.HistogramVec {
	return promauto.NewHistogramVec(opts, variableLabels)
}

func promautoMustNewGaugeVec(opts prometheus.GaugeOpts, variableLabels []string) *prometheus.GaugeVec {
	return promauto.NewGaugeVec(opts, variableLabels)
}

func promautoMustNewCounter(opts prometheus.CounterOpts) prometheus.Counter {
	return promauto.NewCounter(opts)
}

// ─── multi-dimensional warm helpers ─────────────────────────────────────

func warmHistogramVec2D(vec *prometheus.HistogramVec, a, b []string) {
	for _, x := range a {
		for _, y := range b {
			func() {
				defer func() {
					if r := recover(); r != nil {
						panic(fmt.Sprintf(
							"dispatch: governor metric warming failed for (backend=%s, mode=%s): %v",
							x, y, r))
					}
				}()
				vec.WithLabelValues(x, y).Observe(0)
			}()
		}
	}
}

func warmGaugeVec3D(vec *prometheus.GaugeVec, a, b, c []string) {
	for _, x := range a {
		for _, y := range b {
			for _, z := range c {
				func() {
					defer func() {
						if r := recover(); r != nil {
							panic(fmt.Sprintf(
								"dispatch: governor metric warming failed for (backend=%s, mode=%s, state=%s): %v",
								x, y, z, r))
						}
					}()
					vec.WithLabelValues(x, y, z).Set(0)
				}()
			}
		}
	}
}

func warmCounterVec3D(vec *prometheus.CounterVec, a, b, c []string) {
	for _, x := range a {
		for _, y := range b {
			for _, z := range c {
				func() {
					defer func() {
						if r := recover(); r != nil {
							panic(fmt.Sprintf(
								"dispatch: governor metric warming failed for (backend=%s, mode=%s, result=%s): %v",
								x, y, z, r))
						}
					}()
					vec.WithLabelValues(x, y, z).Add(0)
				}()
			}
		}
	}
}

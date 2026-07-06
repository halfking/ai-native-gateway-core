// Package compressor - guard.go (rtk borrowing, 2026-07-06)
//
// NeverWorse mirrors rtk's core safety contract (rtk src/core/guard.rs
// `never_worse`): a compression/transform MUST NEVER emit more bytes than
// the raw input. If the "compressed" output is >= raw length, the transform
// regressed — discard it and forward the raw body. This is the fallback
// safety net that prevents a broken rebuilder/summary path from silently
// INFLATING every request and eroding the very token savings the compressor
// exists to deliver.
//
// Why this lives here (not in middleware/):
//
//	The guard needs to compare the ORIGINAL client body against the
//	post-compression/post-stabilization body, both of which exist only inside
//	the streaming.ChatHandler request lifecycle. An HTTP middleware sees the
//	body either before OR after, never both. Keeping it in the compression
//	package also lets the guard share the compression_triggered_total /
//	compression_regressed_total metric family.
//
// Threshold note: rtk uses strict >= (any growth is a regression). We keep
// that here — `>=` not `>`. Rationale: a transform that produced an
// IDENTICAL-length body did no useful work (no compression) and should also
// be skipped so we don't pay the re-serialization cost.

package compression

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// guardRegressed counts how many times NeverWorse discarded a processed
// body because it failed to actually shrink the raw input. A non-zero,
// growing value is an operator signal that a compression/rebuilder path
// is inflating requests — investigate via the X-Gw-Never-Worse-Regressed
// response header or slog warnings.
var guardRegressed = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "compression_regressed_total",
		Help: "Requests where a compression/transform produced output >= raw length and was discarded by the never_worse guard. Labelled by the stage that regressed (compress|stabilize|inject). Compress guards the session-compressor output; inject guards cache-control-marker injection. Stabilize is intentionally unguarded (it's a reorder, not a shrink).",
	},
	[]string{"stage"},
)

func init() {
	// Tolerate AlreadyRegisteredError for the same R1.6 coexistence reason
	// as the metrics.init() above (legacy compressor/ may be linked in the
	// same process during the migration window).
	if err := prometheus.Register(guardRegressed); err != nil {
		// already registered → silently skip; not an error condition.
		_ = err
	}
}

// GuardStage labels which pipeline stage is being guarded, so operators can
// tell whether regressions come from compression, prefix stabilization, or
// cache-control injection.
type GuardStage string

const (
	GuardStageCompress  GuardStage = "compress"
	GuardStageStabilize GuardStage = "stabilize"
	GuardStageInject    GuardStage = "inject"
)

// NeverWorse enforces the rtk safety contract for a single transform stage.
//
// It compares len(processed) against len(raw). If processed is shorter, the
// transform worked → return processed unchanged. If processed is equal-or-
// longer, the transform regressed → return raw (the original) and report
// regressed=true so the caller can log + set a response header for diagnosis.
//
// Edge cases:
//   - empty raw: returns processed (a transform that turned "" into something
//     isn't a regression; there was nothing to lose).
//   - empty processed: returns processed (an empty body is always <= raw, so
//     this only happens when raw is also empty — covered above).
//   - nil processed: treated as length 0, returns nil.
//
// NeverWorse is intentionally byte-length based (not token-estimated). Token
// estimation is a heuristic (chars/3.5) with per-language variance; byte
// length is exact and is what rtk uses for the same reason. A transform that
// shrinks bytes almost always shrinks tokens; the reverse is not guaranteed,
// so byte-length is the safe (pessimistic) choice.
func NeverWorse(raw, processed []byte, stage GuardStage) (out []byte, regressed bool) {
	// No raw to compare against → nothing to guard. Forward processed.
	if len(raw) == 0 {
		return processed, false
	}
	// processed is empty/nil → trivially not worse.
	if len(processed) == 0 {
		return processed, false
	}
	// The contract: processed must be STRICTLY shorter than raw to be kept.
	if len(processed) < len(raw) {
		return processed, false
	}
	// Regression: processed >= raw. Discard the transform output.
	guardRegressed.WithLabelValues(string(stage)).Inc()
	slog.Warn("never_worse guard: transform regressed, reverting to raw body",
		"stage", string(stage),
		"raw_bytes", len(raw),
		"processed_bytes", len(processed),
		"delta_bytes", len(processed)-len(raw),
	)
	return raw, true
}

// GuardRegressedCount is a test helper returning the current counter value
// for a stage. Production callers should scrape the Prometheus endpoint.
func GuardRegressedCount(stage GuardStage) float64 {
	m, err := guardRegressed.GetMetricWithLabelValues(string(stage))
	if err != nil || m == nil {
		return 0
	}
	pb := &dto.Metric{}
	_ = m.(prometheus.Metric).Write(pb)
	if pb.Counter != nil && pb.Counter.Value != nil {
		return *pb.Counter.Value
	}
	return 0
}

// ResetGuardMetrics is a test-only helper that wipes the guard counter so
// per-test assertions run against a clean slate.
func ResetGuardMetrics() {
	guardRegressed.Reset()
}

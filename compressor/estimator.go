// Package compressor - estimator.go (Round 47 / v7 T4)
//
// Internal estimator that bridges transform.ThresholdBytes with the
// mode=1 (auto_threshold) decision path. Lives inside compressor/ rather
// than transform/ because it pulls from environment configuration
// (LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION) which is compressor-specific
// concern, not a general transform primitive.
//
// Layering:
//
//	executor_chat.go (pre-request check)
//	  -> compressor.Estimator.NeedsCompression(body, cand)
//	  -> transform.ThresholdBytes(window, fraction)
//	  -> bool
//
// estimator.go is intentionally tiny (no caching, no state): every call
// hits the env once via envFraction() and reads the candidate's context
// window. The hot path is O(1) and bounded by os.Getenv latency (~50ns).
//
// See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §2 (env
// config) and §3.4 (pre-request trigger flow).

package compressor

import (
	"os"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/transform"
)

// defaultWindowFraction matches LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION
// default (v7 §2). Tuned to leave a 5% buffer over the in-place soft-limit
// default (0.85) for upstream response generation + model internal overhead.
const defaultWindowFraction = 0.8

// envFraction reads LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION. Falls back to
// defaultWindowFraction on parse error or unset. Mirrors the existing
// LLM_GATEWAY_COMPACTION_DISABLE / LLM_GATEWAY_COMPACTION_MODELS pattern
// in routing/context_summarize.go - env read is per-call and never cached,
// so live config changes via reload.sh / kubectl rollout restart take
// effect immediately.
func envFraction() float64 {
	raw := os.Getenv("LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION")
	if raw == "" {
		return defaultWindowFraction
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || v > 1.0 {
		return defaultWindowFraction
	}
	return v
}

// Estimator carries the env-derived fraction once so the hot path
// doesn't re-read os.Getenv on every request. Construct via NewEstimator()
// at executor init time. The struct is tiny (one float64) so passing by
// value is cheap.
type Estimator struct {
	fraction float64
}

// NewEstimator builds an Estimator with the current env fraction. Cheap
// enough to construct per-request if needed, but typically built once at
// startup and held on the Executor struct.
func NewEstimator() *Estimator {
	return &Estimator{fraction: envFraction()}
}

// Fraction returns the active fraction (read-only).
func (e *Estimator) Fraction() float64 {
	if e == nil {
		return defaultWindowFraction
	}
	return e.fraction
}

// NeedsCompression is the mode=1 (auto_threshold) pre-request gate.
// Returns true when the body exceeds the threshold for the given context
// window. Returns false when the window is unknown (caller falls through
// to the 4xx recovery path - see v7 §3.4).
//
// Pure delegation to transform.NeedsCompression - kept as a method so
// callers can swap the underlying rule without touching the executor.
func (e *Estimator) NeedsCompression(body []byte, contextWindow int) bool {
	frac := defaultWindowFraction
	if e != nil && e.fraction > 0 {
		frac = e.fraction
	}
	return transform.NeedsCompression(body, contextWindow, frac)
}

// ThresholdBytes exposes the byte threshold for a given window under the
// current fraction. Useful for instrumentation / metrics - e.g.
// emitting compression_triggered_total{ratio = bytes/threshold}.
func (e *Estimator) ThresholdBytes(contextWindow int) int {
	frac := defaultWindowFraction
	if e != nil && e.fraction > 0 {
		frac = e.fraction
	}
	return transform.ThresholdBytes(contextWindow, frac)
}

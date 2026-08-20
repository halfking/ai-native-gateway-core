package streaming

import (
	"net/http"
	"os"
	"sync/atomic"
)

// attempt_gate_wiring.go — SR-W1 Phase 0B bridge integration (doc 18 §9.3).
//
// The gate is enabled in buffered mode by default. Transport heartbeat comments
// still pass through immediately, while attempt-specific metadata and errors stay
// discardable until the first semantic frame commits the selected supplier.
// LLM_GATEWAY_ATTEMPT_COMMIT_GATE=false is the emergency legacy rollback.

var (
	attemptGateEnabledOverride atomic.Value // bool
	attemptGateModeOverride    atomic.Value // GateMode
)

func envStringOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func attemptGateEnabled() bool {
	if v, ok := attemptGateEnabledOverride.Load().(bool); ok {
		return v
	}
	return envBool("LLM_GATEWAY_ATTEMPT_COMMIT_GATE", true)
}

func attemptGateMode() GateMode {
	if v, ok := attemptGateModeOverride.Load().(GateMode); ok {
		return v
	}
	if envStringOrDefault("LLM_GATEWAY_ATTEMPT_COMMIT_GATE_MODE", "buffered") == "immediate" {
		return GateModeImmediate
	}
	return GateModeBuffered
}

// setAttemptGateForTest overrides the enabled flag and gate mode for the
// duration of a test. The returned restore function must be deferred.
func setAttemptGateForTest(enabled bool, mode GateMode) (restore func()) {
	oldEnabled := attemptGateEnabled()
	oldMode := attemptGateMode()
	attemptGateEnabledOverride.Store(enabled)
	attemptGateModeOverride.Store(mode)
	return func() {
		attemptGateEnabledOverride.Store(oldEnabled)
		attemptGateModeOverride.Store(oldMode)
	}
}

// wrapAttemptWriter wraps the client writer of a streaming bridge in a commit
// gate for the given CLIENT protocol (the gate classifies client-facing
// frames regardless of the upstream dialect the bridge converts from).
//
// A writer that is already gated (the SurvivalCoordinator pre-wrapped it
// with its per-attempt buffered gate) passes through unchanged with the
// existing gate — commit ownership stays with the coordinator.
func wrapAttemptWriter(w http.ResponseWriter, protocol ClientProtocol) (http.ResponseWriter, *AttemptCommitGate) {
	if gw, ok := w.(*GateWriter); ok {
		return w, gw.UnderlyingAttemptGate()
	}
	var firstSemanticByte func()
	if source, ok := w.(interface{ FirstSemanticByteCallback() func() }); ok {
		firstSemanticByte = source.FirstSemanticByteCallback()
	}
	if !attemptGateEnabled() && firstSemanticByte == nil {
		return w, nil
	}
	mode := attemptGateMode()
	if !attemptGateEnabled() {
		mode = GateModeImmediate
	}
	sw := NewSerializedStreamWriter(w)
	// 2026-08-19 observability: the handler sets X-Request-Id on w before
	// invoking the bridge, so it is already present here and correlates the
	// gate's commit/discard log lines with the owning request (mirrors the
	// survival path which passes params.RequestID explicitly).
	reqID := w.Header().Get("X-Request-Id")
	// Holdback is deliberately not wired here. This path serves standalone
	// bridges that do not have the SurvivalCoordinator's discard/retry owner;
	// enabling L1 here can strand successful output in the gate or flush an
	// interrupted attempt to the client. The coordinator is the sole owner of
	// the opt-in recovery window.
	gate := NewAttemptCommitGate(protocol, sw, GateOptions{Mode: mode, RequestID: reqID, FirstSemanticByte: firstSemanticByte})
	return NewGateWriterWithResponse(gate, w), gate
}
